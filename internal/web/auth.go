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

// hostSessionCookie is the name of the session cookie where it is Secure: the
// __Host- prefix makes a browser refuse a cookie of that name that was not set
// with Secure, from this host, for the whole path, so a sibling subdomain cannot
// plant one.
const hostSessionCookie = "__Host-gt_session"

// sessionCookieOf reads the session cookie under the name the request is served
// with: where cookies are Secure only the __Host- one counts. A plain gt_session
// that a sibling subdomain or a man in the middle planted over http is not read,
// or the prefix would protect nothing.
func (a *app) sessionCookieOf(r *http.Request) (*http.Cookie, error) {
	if a.secureCookies(r) {
		return r.Cookie(hostSessionCookie)
	}
	return r.Cookie(sessionCookie)
}

// multipartMemory is how much of a form with a file stays in memory; the rest
// goes to a temporary file that net/http removes after the request.
const multipartMemory = 1 << 20

type personKey struct{}
type csrfKey struct{}

// linkSessionKey trägt, wann eine Link-Sitzung endet; es fehlt in jeder anderen.
type linkSessionKey struct{}
type browserSession struct {
	principal store.Principal
	csrf      string
	expires   time.Time
	// link: the session came from a one-time login link (an owner's, or ctx web).
	// Whoever holds such a link must not be able to change how the account signs
	// in, so the profile refuses address and password changes in it.
	link bool
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

// markLink kennzeichnet die Sitzung als eine aus einem Einmal-Link und gibt ihr
// eine feste Laufzeit, die bei Nutzung nicht nachrückt.
func (s *sessions) markLink(id string) {
	s.mu.Lock()
	if v, ok := s.values[id]; ok {
		v.link = true
		v.expires = time.Now().Add(linkSessionMaxAge)
		s.values[id] = v
	}
	s.mu.Unlock()
}

// linkSession says whether the request's session started from a login link.
func (a *app) linkSession(r *http.Request) bool {
	c, err := a.sessionCookieOf(r)
	if err != nil {
		return false
	}
	a.sessions.mu.Lock()
	defer a.sessions.mu.Unlock()
	return a.sessions.values[c.Value].link
}

func (s *sessions) get(id string) (browserSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[id]
	if !ok || time.Now().After(v.expires) {
		delete(s.values, id)
		return browserSession{}, false
	}
	// Eine Link-Sitzung behält ihr festes Ende; jede andere rückt bei Nutzung nach.
	if !v.link {
		v.expires = time.Now().Add(30 * 24 * time.Hour)
		s.values[id] = v
	}
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

// dropAccount ends every session of an account (a new password, a reset).
func (s *sessions) dropAccount(accountID string) { s.dropAccountExcept(accountID, "") }

// dropAccountExcept ends every session of an account but the one named keep.
func (s *sessions) dropAccountExcept(accountID, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.values {
		if v.principal.ID == accountID && id != keep {
			delete(s.values, id)
		}
	}
}
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
		if cookie, err := a.sessionCookieOf(r); err == nil {
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
				if session.link {
					ctx = context.WithValue(ctx, linkSessionKey{}, session.expires)
				}
				r = r.WithContext(ctx)
				r = a.withLang(r, session.principal.ID)
				a.rememberProject(w, r)
				// A plain 404 or 403 of a page becomes a page in the shell.
				r, keep := withFormKeep(r)
				ew := &errorPageWriter{ResponseWriter: w, a: a, r: r, keep: keep}
				// A session from a one-time link does ordinary work only; what changes
				// rights or access needs a sign-in with a real proof.
				if session.link && linkNeedsProof(r) {
					a.proofRequired(ew, r)
					ew.finish()
					return
				}
				next.ServeHTTP(ew, r)
				ew.finish()
				return
			}
		}
		a.loginRedirect(w, r)
	})
}

// fromSignIn sagt, ob die Sitzung aus einem Login stammt (OIDC, Login-Link,
// Bootstrap-, Claim- oder Einladungscode) und nicht aus einem eingefügten
// Token. Nur eine solche Sitzung nutzt Menschen-Rechte (requireSignIn); eine
// Token-Sitzung darf dasselbe wie das Token über die API. Außerdem entscheidet
// es, ob etwas als Mensch oder als Agent geschrieben wird.
func fromSignIn(r *http.Request) bool { return browserPrincipal(r).SignedIn() }

func (a *app) notInteractive(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	a.renderBrowser(w, r, "interactive", pageData{Title: l.msg("adm.login.title"), OIDC: a.oidc != nil})
}

// proofRequired is the page for an action a session from a one-time link may not
// do: it says so and leads to the sign-in, which comes back to the page the person
// was on. The session of the link stays until the new sign-in replaces it.
func (a *app) proofRequired(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	a.renderBrowser(w, r, "interactive", pageData{Title: l.msg("adm.relogin.title"), OIDC: a.oidc != nil, ReloginHref: reloginHref(r)})
}

// requireCredentialSession refuses a session that started from a one-time login
// link (an owner's, or ctx web) on every route that hands out or approves a
// credential: pairing and approving a device. Such a session would otherwise turn
// a link of minutes into a token that lasts. requirePerson applies the same rule
// to every route of linkRules; this gate stays for the routes that name it.
func (a *app) requireCredentialSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.linkSession(r) {
			a.proofRequired(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	d := pageData{
		Title: title, Error: errMsg, OIDC: a.oidc != nil, Paste: a.pasteLoginAllowed(),
		ProviderName: a.oidcName, Bootstrap: a.oidc == nil && a.bootstrapPending(),
		Mail: a.mailAvailable(), PasswordLogin: a.passwordsOn(),
	}
	// Without a mail setup (and without an identity provider) the page names the
	// ways back in that exist: a login link from an owner, or ctx web.
	d.NoMailHint = !d.Mail && !d.OIDC && !d.Bootstrap
	return d
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
	// Who is the person a login link opens; Device says a device asked for it
	// (ctx web) and Machine names that device.
	Who     string
	Device  bool
	Machine string
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
	data := a.loginData(l.msg("login.tab"), "")
	// After signing out the page says so; the note leaves the address with the first view.
	if r.URL.Query().Get("notice") == "signed_out" {
		data.Notice = l.msg("login.signed_out")
	}
	a.render(l, w, "login", data)
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
	if land := a.guestLanding(account.ID); land != "" {
		// A guest has no computer to pair: they go to the project shared with them.
		next = land
	} else if _, err := a.store.Join().Create(account.ID); err == nil {
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
	a.startSessionLink(w, r, principal, next, false)
}

func (a *app) startSessionLink(w http.ResponseWriter, r *http.Request, principal store.Principal, next string, link bool) {
	if old, err := a.sessionCookieOf(r); err == nil {
		a.sessions.remove(old.Value)
	}
	id, err := a.sessions.create(principal)
	if err != nil {
		http.Error(w, "could not create secure session", http.StatusInternalServerError)
		return
	}
	if link {
		a.sessions.markLink(id)
	}
	maxAge := 30 * 24 * 60 * 60
	if link {
		maxAge = int(linkSessionMaxAge.Seconds())
	}
	http.SetCookie(w, a.sessionCookieFor(r, id, maxAge))
	a.adoptLanguageCookie(r, principal)
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
			if name, machine, device, ok := a.store.LoginLinkPreview(code); ok {
				data.Auth.Who, data.Auth.Device = name, device
				data.Auth.Machine = firstNonEmpty(machine, l.msg("adm.this_device"))
			}
		}
	}
	if data.Invite {
		data.InviteV = a.orgInviteView(r, l, code)
	}
	a.render(l, w, "logincode", data)
}

// orgInviteView ist die Vorschau einer Einladung in eine Organisation, oder nil,
// wenn sie sich nicht zeigen lässt. Eine an eine Adresse gebundene Einladung
// zeigt sich auch dann, wenn diese Instanz keine Adresse bestätigen kann: dann
// mit Blocked und der maskierten Adresse, damit die Seite den Weg nennt statt
// eines Kreislaufs (P05).
func (a *app) orgInviteView(r *http.Request, l lang, code string) *joinView {
	p, err := a.store.PreviewJoin(code, a.store.AccessEnforced(), a.oidc != nil || a.mailAvailable())
	if err != nil && !errors.Is(err, store.ErrInvitationEmail) {
		return nil
	}
	target := p.Project
	if target == "" {
		target = p.Org
	}
	v := &joinView{Inviter: p.Inviter, Target: target, Short: shortName(target), Org: p.Org, ExpiresAt: p.ExpiresAt,
		ExpiresIn: expiresIn(l, p.ExpiresAt), Code: code, Signup: a.oidc != nil && a.oidc.signupOffered(r.Context()),
		Provider: a.oidcName, OIDC: a.oidc != nil, OrgMark: initialOf(firstNonEmpty(p.Org, shortName(target))),
		Mail: a.oidc == nil && a.mailAvailable(), Bound: store.MaskEmail(p.BoundEmail), Blocked: err != nil}
	if p.Project == "" {
		v.Org = ""
	}
	return v
}

// invitationEmailRejected is the page for an invitation that is bound to an
// address the person cannot show: it names the masked address and the way on.
func (a *app) invitationEmailRejected(l lang, w http.ResponseWriter, code string) {
	p, _ := a.store.PreviewJoin(code, a.store.AccessEnforced(), true)
	masked := store.MaskEmail(p.BoundEmail)
	inviter := firstNonEmpty(p.Inviter, l.msg("join.the_inviter"))
	if masked == "" {
		a.loginMessage(l, w, http.StatusForbidden, "auth.invitation_email.title", "auth.invitation_email.text")
		return
	}
	a.loginMessage(l, w, http.StatusForbidden, "auth.invitation_email.title", "auth.invitation_email.bound", masked, inviter)
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
	// restrictedLink: an owner or a device made this login link. Its session may not
	// change how the account signs in or hand out credentials.
	restrictedLink := false
	// device: the device token that asked for this link (ctx web), if any.
	var device store.Principal
	kind := a.store.CodeKindFor(code)
	switch kind {
	case store.CodeBootstrap:
		if a.oidc != nil {
			a.beginOIDC(w, r, code)
			return
		}
		if account, err = a.store.BootstrapLocal(code, r.FormValue("name")); err == nil {
			a.dropBootstrapFile()
			// The first account also gets an address to sign in with, confirmed by mail.
			if a.mailAvailable() {
				a.confirmFirstAddress(r, l, account)
			}
		}
	case store.CodeLogin:
		restrictedLink = a.store.LoginLinkRestricted(code)
		account, device, err = a.store.RedeemLoginLinkDevice(code)
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
		if a.mailAvailable() {
			a.joinByMail(w, r, l, code, r.FormValue("join") == "1")
			return
		}
		// Eine an eine Adresse gebundene Einladung geht ohne Mail nicht: die
		// Seite sagt, an welche Adresse sie gebunden ist und was zu tun ist.
		if _, perr := a.store.PreviewJoin(code, a.store.AccessEnforced(), false); errors.Is(perr, store.ErrInvitationEmail) {
			a.invitationEmailRejected(l, w, code)
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
	if kind == store.CodeLogin && device.TokenID != 0 {
		// ctx web: the session has the rights of the device token that asked for
		// the link (reading and ordinary work), never those of a person signing in.
		a.startSessionLink(w, r, device, a.takeNext(w, r, "/ui/inbox"), true)
		return
	}
	if kind == store.CodeLogin && restrictedLink {
		a.startSessionLink(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind}, a.takeNext(w, r, "/ui/inbox"), true)
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
	if cookie, err := a.sessionCookieOf(r); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, a.sessionCookieFor(r, "", -1))
	http.SetCookie(w, a.projectCookieFor(r, "", -1))
	http.Redirect(w, r, "/ui/login?notice=signed_out", http.StatusSeeOther)
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

// confirmFirstAddress sends the confirmation of the address the first account
// typed on the setup page; a missing or broken address leaves the account as is
// (it can add one on its profile).
func (a *app) confirmFirstAddress(r *http.Request, l lang, account store.Account) {
	typed := strings.TrimSpace(r.FormValue("email"))
	if typed == "" || len(typed) > 320 {
		return
	}
	if ch, err := a.store.IssueConfirmChallenge(account.ID, typed); err == nil {
		a.sendChallengeMail(r, l, ch)
	}
}
