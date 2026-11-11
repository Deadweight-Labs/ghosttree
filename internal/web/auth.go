package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const sessionCookie = "gt_session"

// multipartMemory is how much of a form with a file stays in memory; the rest
// goes to a temporary file that net/http removes after the request.
const multipartMemory = 1 << 20

type personKey struct{}
type csrfKey struct{}
type browserSession struct {
	principal store.Principal
	csrf      string
	expires   time.Time
}
type sessions struct {
	mu     sync.Mutex
	values map[string]browserSession
}

func newSessions() *sessions { return &sessions{values: map[string]browserSession{}} }
func (s *sessions) create(principal store.Principal) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.values[id] = browserSession{principal: principal, csrf: hex.EncodeToString(raw), expires: time.Now().Add(30 * 24 * time.Hour)}
	s.mu.Unlock()
	return id, nil
}
func (s *sessions) get(id string) (browserSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[id]
	if !ok || time.Now().After(v.expires) {
		delete(s.values, id)
		return browserSession{}, false
	}
	v.expires = time.Now().Add(30 * 24 * time.Hour)
	s.values[id] = v
	return v, true
}

// relabel gives every session of an account its new display name.
func (s *sessions) relabel(accountID, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.values {
		if v.principal.ID == accountID {
			v.principal.Label = label
			s.values[id] = v
		}
	}
}
func (s *sessions) remove(id string) { s.mu.Lock(); delete(s.values, id); s.mu.Unlock() }
func browserPrincipal(r *http.Request) store.Principal {
	v, _ := r.Context().Value(personKey{}).(store.Principal)
	return v
}
func personOf(r *http.Request) string { return browserPrincipal(r).Label }
func csrfOf(r *http.Request) string {
	v, _ := r.Context().Value(csrfKey{}).(string)
	return v
}
func (a *app) requirePerson(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if session, ok := a.sessions.get(cookie.Value); ok {
				// Das Token wurde beim Login geprüft; hier prüfen wir, ob es und
				// das Konto noch gelten, sonst überlebte ein Widerruf bis zum
				// Neustart.
				if !a.store.PrincipalValid(session.principal) {
					a.sessions.remove(cookie.Value)
					http.SetCookie(w, a.sessionCookieFor(r, "", -1))
					a.loginRedirect(w, r)
					return
				}
				ctx := context.WithValue(r.Context(), personKey{}, session.principal)
				ctx = context.WithValue(ctx, csrfKey{}, session.csrf)
				ctx = context.WithValue(ctx, shellMemoKey{}, &shellMemo{})
				r = r.WithContext(ctx)
				a.rememberProject(w, r)
				// A plain 404 or 403 of a page becomes a page in the shell.
				r, keep := withFormKeep(r)
				ew := &errorPageWriter{ResponseWriter: w, a: a, r: r, keep: keep}
				next.ServeHTTP(ew, r)
				ew.finish()
				return
			}
		}
		a.loginRedirect(w, r)
	})
}

// interactive sagt, ob die Sitzung aus einem Login stammt (OIDC, Login-Link,
// Bootstrap-, Claim- oder Einladungscode) und nicht aus einem eingefügten
// Token. Ein Token liegt in der Konfiguration jedes Rechners, auf dem ein Agent
// läuft; wer es einfügt, bekommt eine lesende Sitzung.
func interactive(r *http.Request) bool {
	return browserPrincipal(r).TokenKind == store.WebSessionKind
}

// requireInteractive schützt Verwaltungsformulare: Rollen, Mitgliedschaften,
// Einladungen, Projektverschiebungen und die Freigabe von Geräten. Eine Sitzung
// aus eingefügtem Token bekommt 403 mit einer Erklärung.
func (a *app) requireInteractive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !interactive(r) {
			a.notInteractive(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) notInteractive(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	a.renderBrowser(w, r, "interactive", pageData{Title: l.msg("adm.login.title"), OIDC: a.oidc != nil})
}

func (a *app) requireCSRF(next http.Handler) http.Handler { return a.csrfGate(next, false) }

// requireCSRFMultipart is requireCSRF for the one kind of form that carries a
// file (the image upload): its token is a field of the multipart body, which
// ParseForm does not read. Only routes that wrap the body in limitBodyN first use
// it; every other route never parses multipart, so a large multipart body cannot
// reach the temporary files of net/http there. A body over the limit is 413.
func (a *app) requireCSRFMultipart(next http.Handler) http.Handler { return a.csrfGate(next, true) }

func (a *app) csrfGate(next http.Handler, multipart bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			var err error
			if multipart && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				err = r.ParseMultipartForm(multipartMemory)
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
			} else {
				err = r.ParseForm()
			}
			tokenOK := err == nil && subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(csrfOf(r))) == 1
			// What was typed is only kept for a form of this site with a valid token: a
			// refused token (or a body that did not parse) keeps nothing in the server's memory.
			if tokenOK && a.sameOrigin(r) {
				rememberForm(r)
			}
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				a.formTooLarge(w, r)
				return
			}
			if !tokenOK || !a.sameOrigin(r) {
				w.Header().Set(rejectHeader, "csrf")
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *app) requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *app) sameOrigin(r *http.Request) bool {
	origin, err := url.Parse(strings.TrimSpace(r.Header.Get("Origin")))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	if a.publicOrigin != "" && strings.EqualFold(origin.Scheme+"://"+origin.Host, a.publicOrigin) {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	forwardedProto, protoSet := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	forwardedHost, hostSet := singleForwardedValue(r.Header, "X-Forwarded-Host")
	if protoSet || hostSet {
		if !a.proxies.Trusts(r.RemoteAddr) || !protoSet || !hostSet ||
			(forwardedProto != "http" && forwardedProto != "https") || !validForwardedHost(forwardedHost) {
			return false
		}
		scheme, host = forwardedProto, forwardedHost
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, host)
}
func singleForwardedValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return "", true
	}
	value := strings.TrimSpace(values[0])
	return value, true
}
func validForwardedHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\\@\t\r\n") {
		return false
	}
	u, err := url.Parse("https://" + host)
	return err == nil && u.Host == host && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

// pasteLoginAllowed regelt den Token-Login der Weboberfläche. Ohne OIDC bleibt
// er wie bisher. Mit OIDC bleibt er, bis irgendein Konto eine Identität hat:
// sonst sperrte sich eine bestehende Instanz aus, bevor jemand sein Konto per
// Claim-Code verbunden hat.
func (a *app) pasteLoginAllowed() bool {
	return a.oidc == nil || !a.store.HasIdentities()
}

func (a *app) loginData(title, errMsg string) pageData {
	return pageData{
		Title: title, Error: errMsg, OIDC: a.oidc != nil, Paste: a.pasteLoginAllowed(),
		ProviderName: a.oidcName, Bootstrap: a.oidc == nil && a.bootstrapPending(),
	}
}

// bootstrapPending sagt, ob die Instanz noch auf ihren ersten Anmeldecode
// wartet; die Datei verschwindet mit der ersten Anmeldung.
func (a *app) bootstrapPending() bool {
	if a.bootstrapFile == "" {
		return false
	}
	_, err := os.Stat(a.bootstrapFile)
	return err == nil
}

// codeHeaders gelten für jede Seite mit einem Codefeld: strict-origin, damit
// der POST seinen Origin behält (PR #59), und kein Zwischenspeichern.
func codeHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "strict-origin")
}

// authView trägt, was die Anmelde- und Einmalcode-Seite über pageData hinaus
// brauchen.
type authView struct {
	// Linked: der Code stammt aus der Adresse (Login-Link); das Feld bleibt dann
	// verborgen, die Person bestätigt nur noch.
	Linked bool
}

// loginRejected zeigt die Anmeldeseite mit dem Fehler über der Karte. Ein
// Formularfehler ist 422: die Seite ist richtig ausgeliefert, nur die Eingabe
// taugt nicht (Konsole und Zugriffslog sehen keinen Fehlschlag der Seite).
func (a *app) loginRejected(l lang, w http.ResponseWriter, status int, errKey string, tokenOpen bool) {
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := a.loginData(l.msg("login.tab"), l.msg(errKey))
	data.TokenOpen = tokenOpen
	a.render(l, w, "login", data)
}

// codeRejected bleibt auf der Einmalcode-Seite: Fehler über der Karte, der
// getippte Code und, wenn die Seite danach fragte, der Name stehen noch im Formular.
func (a *app) codeRejected(l lang, w http.ResponseWriter, status int, errKey, code string, needsName bool) {
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := a.loginData(l.msg("login.code_title"), l.msg(errKey))
	data.Code, data.NeedsName = code, needsName
	a.render(l, w, "logincode", data)
}

// rejectedCode wählt die Meldung für einen Code, der nichts einlöst. Ein Link,
// den man angeklickt hat, lässt sich nicht "prüfen"; auf einer frischen Instanz
// steht der Code im Log des Servers.
func (a *app) rejectedCode(r *http.Request) string {
	switch {
	case a.oidc == nil && a.bootstrapPending():
		return "login.error_bootstrap"
	case r.FormValue("linked") == "1" || strings.Contains(r.FormValue("code"), "://"):
		return "login.error_link"
	}
	return "login.error_code"
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	codeHeaders(w)
	a.rememberNext(w, r)
	// Wer schon angemeldet ist, sieht kein Anmeldeformular, sondern landet dort,
	// wo die Anmeldung hinführt.
	if _, ok := a.joinSession(r); ok {
		http.Redirect(w, r, a.takeNext(w, r, "/ui/inbox"), http.StatusSeeOther)
		return
	}
	a.render(l, w, "login", a.loginData(l.msg("login.tab"), ""))
}
func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if !parseLoginForm(w, r) {
		return
	}
	if !a.pasteLoginAllowed() {
		a.loginMessage(l, w, http.StatusForbidden, "auth.token_disabled.title", "auth.token_disabled.text")
		return
	}
	principal, ok := a.store.AuthenticatePrincipal(r.FormValue("token"))
	if !ok {
		a.loginRejected(l, w, http.StatusUnprocessableEntity, "login.error_token", true)
		return
	}
	a.store.TouchToken(principal.TokenID)
	a.startSession(w, r, principal)
}

// finishLogin startet eine Websitzung für ein Konto, ohne Token. Die Sitzung
// gilt, solange das Konto aktiv ist (TokenKind "web").
func (a *app) finishLogin(w http.ResponseWriter, r *http.Request, account store.Account) {
	a.finishLoginTo(w, r, account, "/ui/inbox")
}

// finishLoginTo ist finishLogin mit einem eigenen Ziel für den Fall, dass keine
// Rücksprungadresse gemerkt ist.
func (a *app) finishLoginTo(w http.ResponseWriter, r *http.Request, account store.Account, fallback string) {
	a.startSessionAt(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind}, a.takeNext(w, r, fallback))
}

// finishJoinLogin ist finishLogin für eine Anmeldung über die Join-Seite: das
// Konto kommt aus der Einladung, für das Konto entsteht die Join-Sitzung, und die
// Seite danach zeigt den Paarungscode und später die Freigabe des Geräts.
func (a *app) finishJoinLogin(w http.ResponseWriter, r *http.Request, account store.Account) {
	next := "/ui/inbox"
	if _, err := a.store.Join().Create(account.ID); err == nil {
		next = "/join/pair"
	}
	a.startSessionAt(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind}, next)
}

// startSession vergibt immer eine neue Sitzungs-ID und verwirft eine
// mitgebrachte (keine Session-Fixation).
func (a *app) startSession(w http.ResponseWriter, r *http.Request, principal store.Principal) {
	a.startSessionAt(w, r, principal, a.takeNext(w, r, "/ui/inbox"))
}

func (a *app) startSessionAt(w http.ResponseWriter, r *http.Request, principal store.Principal, next string) {
	if old, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(old.Value)
	}
	id, err := a.sessions.create(principal)
	if err != nil {
		http.Error(w, "could not create secure session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.sessionCookieFor(r, id, 30*24*60*60))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// codePage zeigt für einen Login-Link erst eine Bestätigung. Das Einlösen
// geschieht per POST: Mail- und Chat-Vorschauen rufen Links per GET ab und
// würden sonst den Einmal-Code verbrauchen.
func (a *app) codePage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "strict-origin")
	data := a.loginData(l.msg("login.code_title"), "")
	data.Code = code
	kind := ""
	if code != "" {
		kind = a.store.CodeKindFor(code)
	}
	// Eine Einladung braucht lokal einen Namen; mit OIDC trägt der IdP ihn bei.
	data.Invite = kind == store.CodeInvitation
	data.NeedsName = kind == store.CodeBootstrap || (data.Invite && a.oidc == nil)
	if code != "" {
		// Ein Link, der nichts mehr einlöst, sagt es gleich, statt einen Knopf zu
		// zeigen, der ins Leere führt; das Feld bleibt dann zum Korrigieren offen.
		if kind == "" {
			data.Error = l.msg("login.error_link")
		} else {
			data.Auth.Linked = true
		}
	}
	if data.Invite {
		data.InviteV = a.orgInviteView(r, l, code)
	}
	a.render(l, w, "logincode", data)
}

// orgInviteView ist die Vorschau einer Einladung in eine Organisation, oder nil,
// wenn sie sich nicht zeigen lässt.
func (a *app) orgInviteView(r *http.Request, l lang, code string) *joinView {
	p, err := a.store.PreviewOrgInvitation(code, a.store.AccessEnforced(), a.oidc != nil)
	if err != nil {
		return nil
	}
	target := p.Project
	if target == "" {
		target = p.Org
	}
	v := &joinView{Inviter: p.Inviter, Target: target, Short: shortName(target), Org: p.Org, ExpiresAt: p.ExpiresAt,
		ExpiresIn: expiresIn(l, p.ExpiresAt), Code: code, Signup: a.oidc != nil && a.oidc.signupOffered(r.Context()),
		Provider: a.oidcName, OIDC: a.oidc != nil, OrgMark: initialOf(firstNonEmpty(p.Org, shortName(target)))}
	if p.Project == "" {
		v.Org = ""
	}
	return v
}

func (a *app) codeSubmit(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Referrer-Policy", "strict-origin")
	if !parseLoginForm(w, r) {
		return
	}
	code, joinCode := codeFromInput(r.FormValue("code"))
	if joinCode != "" {
		http.Redirect(w, r, "/join/"+joinCode, http.StatusSeeOther)
		return
	}
	if len(code) > maxCodeLength || len(r.FormValue("name")) > maxNameLength {
		http.Error(w, "field too long", http.StatusBadRequest)
		return
	}
	onCodePage := r.FormValue("codepage") == "1"
	reject := func(key string) {
		if onCodePage {
			a.codeRejected(l, w, http.StatusUnprocessableEntity, key, code, r.Form.Has("name"))
			return
		}
		a.loginRejected(l, w, http.StatusUnprocessableEntity, key, false)
	}
	if code == "" {
		reject("login.error_empty")
		return
	}
	var account store.Account
	var err error
	kind := a.store.CodeKindFor(code)
	switch kind {
	case store.CodeBootstrap:
		if a.oidc != nil {
			a.beginOIDC(w, r, code)
			return
		}
		if account, err = a.store.BootstrapLocal(code, r.FormValue("name")); err == nil {
			a.dropBootstrapFile()
		}
	case store.CodeLogin:
		account, err = a.store.RedeemLoginLink(code)
	case store.CodeClaim:
		// Ein Claim-Code gehört zum Anbieter-Ablauf; ohne OIDC gibt es ihn nicht.
		if a.oidc == nil {
			err = store.ErrCodeInvalid
			break
		}
		a.beginOIDC(w, r, code)
		return
	case store.CodeInvitation:
		if a.oidc != nil {
			a.beginOIDC(w, r, code)
			return
		}
		// Die Namensprüfung verrät, welche Konten es gibt. Darum zählt jeder Aufruf
		// gegen die Grenze der Beitrittswege, und je Einladung sind nur wenige
		// abgelehnte Namen erlaubt; ist sie gesperrt, wird gar nicht mehr geprüft.
		if !a.joinGate(w, r) {
			return
		}
		if a.inviteNames.locked(code) {
			a.inviteNameRejected(l, w, r, code, "join.error_name_locked", http.StatusTooManyRequests)
			return
		}
		if !a.store.InviteNameFree(r.FormValue("name")) {
			// Kein stilles Umbenennen in "robin-2" oder "user": die Person ändert den
			// Namen selbst.
			key, status := "join.error_name", http.StatusUnprocessableEntity
			if a.inviteNames.miss(code) {
				key, status = "join.error_name_locked", http.StatusTooManyRequests
			}
			a.inviteNameRejected(l, w, r, code, key, status)
			return
		}
		account, err = a.store.InviteLocal(code, r.FormValue("name"))
		if err == nil {
			a.inviteNames.accepted(code)
		}
	default:
		err = store.ErrCodeInvalid
	}
	if errors.Is(err, store.ErrCodeInvalid) {
		reject(a.rejectedCode(r))
		return
	}
	if err != nil {
		a.identityRejected(l, w, err)
		return
	}
	if kind == store.CodeInvitation && r.FormValue("join") == "1" {
		a.finishJoinLogin(w, r, account)
		return
	}
	if kind == store.CodeBootstrap {
		// Die erste Anmeldung einer neuen Instanz führt in die Einrichtung, nicht in
		// einen leeren Posteingang.
		a.finishLoginTo(w, r, account, "/ui/overview")
		return
	}
	a.finishLogin(w, r, account)
}

// inviteNameRejected zeigt dort, wo der Name getippt wurde (Einladungsseite oder
// Einmalcode-Seite), die Meldung am Feld.
func (a *app) inviteNameRejected(l lang, w http.ResponseWriter, r *http.Request, code, msgKey string, status int) {
	name := strings.TrimSpace(r.FormValue("name"))
	if r.FormValue("join") == "1" {
		a.joinNameRejected(l, w, r, code, name, msgKey, status)
		return
	}
	codeHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(int(inviteNameLockFor/time.Second)))
	}
	w.WriteHeader(status)
	data := a.loginData(l.msg("login.code_title"), "")
	data.Code, data.NeedsName, data.Invite = code, true, true
	data.InviteV = a.orgInviteView(r, l, code)
	if data.InviteV != nil {
		data.InviteV.NameError, data.InviteV.NameValue = l.msg(msgKey), name
	} else {
		data.Error = l.msg(msgKey)
	}
	a.render(l, w, "logincode", data)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, a.sessionCookieFor(r, "", -1))
	http.SetCookie(w, a.projectCookieFor(r, "", -1))
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// codeFromInput nimmt den Code aus dem Feld "Code or login link": der Code
// selbst, ein Login-Link mit ?code= oder ein Einladungslink /join/<code>. Bei
// einem Einladungslink kommt der Einladungscode als zweiter Wert zurück; er
// wird nie als Login-Code gewertet.
func codeFromInput(raw string) (code, joinCode string) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") || len(raw) > 2048 {
		return raw, ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw, ""
	}
	if rest, ok := strings.CutPrefix(u.Path, "/join/"); ok && wellFormedJoinCode(rest) {
		return "", rest
	}
	if c := strings.TrimSpace(u.Query().Get("code")); c != "" {
		return c, ""
	}
	return raw, ""
}
