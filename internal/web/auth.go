package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const sessionCookie = "gt_session"

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
					http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
					return
				}
				ctx := context.WithValue(r.Context(), personKey{}, session.principal)
				ctx = context.WithValue(ctx, csrfKey{}, session.csrf)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
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
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	a.renderBrowser(w, r, "interactive", pageData{Title: "Login required"})
}

func (a *app) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := r.ParseForm(); err != nil ||
				subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(csrfOf(r))) != 1 ||
				!a.sameOrigin(r) {
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
	return pageData{Title: title, Error: errMsg, OIDC: a.oidc != nil, Paste: a.pasteLoginAllowed()}
}
func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login", a.loginData("Login", ""))
}
func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !parseLoginForm(w, r) {
		return
	}
	if !a.pasteLoginAllowed() {
		a.loginMessage(w, http.StatusForbidden, "Token login is disabled", "Sign in with your identity provider, or ask the operator for a one-time login link.")
		return
	}
	principal, ok := a.store.AuthenticatePrincipal(r.FormValue("token"))
	if !ok {
		a.render(w, "login", a.loginData("Login", "Invalid token"))
		return
	}
	a.startSession(w, r, principal)
}

// finishLogin startet eine Websitzung für ein Konto, ohne Token. Die Sitzung
// gilt, solange das Konto aktiv ist (TokenKind "web").
func (a *app) finishLogin(w http.ResponseWriter, r *http.Request, account store.Account) {
	a.startSession(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind})
}

// finishJoinLogin ist finishLogin für eine Anmeldung über die Join-Seite: das
// Konto kommt aus der Einladung, die Join-Sitzung dieses Browsers (und damit ein
// schon wartender Installer) wird an das Konto gebunden, und die Seite danach
// ist die Freigabe des Geräts.
func (a *app) finishJoinLogin(w http.ResponseWriter, r *http.Request, account store.Account, inviteCode string) {
	next := "/ui/requests"
	if err := a.store.Join().Bind(inviteCode, a.joinCookieValue(r), account.ID); err == nil {
		next = "/join/pair"
	}
	http.SetCookie(w, a.joinCookieFor(r, "", -1))
	a.startSessionAt(w, r, store.Principal{ID: account.ID, Label: account.Name, TokenKind: store.WebSessionKind}, next)
}

// startSession vergibt immer eine neue Sitzungs-ID und verwirft eine
// mitgebrachte (keine Session-Fixation).
func (a *app) startSession(w http.ResponseWriter, r *http.Request, principal store.Principal) {
	a.startSessionAt(w, r, principal, "/ui/requests")
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
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "strict-origin")
	data := a.loginData("One-time code", "")
	data.Code = code
	kind := ""
	if code != "" {
		kind = a.store.CodeKindFor(code)
	}
	// Eine Einladung braucht lokal einen Namen; mit OIDC trägt der IdP ihn bei.
	data.Invite = kind == store.CodeInvitation
	data.NeedsName = kind == store.CodeBootstrap || (data.Invite && a.oidc == nil)
	a.render(w, "logincode", data)
}
func (a *app) codeSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "strict-origin")
	if !parseLoginForm(w, r) {
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	if len(code) > maxCodeLength || len(r.FormValue("name")) > maxNameLength {
		http.Error(w, "field too long", http.StatusBadRequest)
		return
	}
	var account store.Account
	var err error
	kind := a.store.CodeKindFor(code)
	switch kind {
	case store.CodeBootstrap:
		if a.oidc != nil {
			a.loginMessage(w, http.StatusForbidden, "Use the identity provider",
				"On an instance with OIDC the bootstrap code is entered in the code field of the OIDC sign-in.")
			return
		}
		if account, err = a.store.BootstrapLocal(code, r.FormValue("name")); err == nil {
			a.dropBootstrapFile()
		}
	case store.CodeLogin:
		account, err = a.store.RedeemLoginLink(code)
	case store.CodeInvitation:
		if a.oidc != nil {
			a.loginMessage(w, http.StatusForbidden, "Use the identity provider",
				"On an instance with OIDC an invitation is redeemed by signing in with your identity provider.")
			return
		}
		account, err = a.store.InviteLocal(code, r.FormValue("name"))
	default:
		err = store.ErrCodeInvalid
	}
	if err != nil {
		a.identityRejected(w, err)
		return
	}
	if kind == store.CodeInvitation && r.FormValue("join") == "1" {
		a.finishJoinLogin(w, r, account, code)
		return
	}
	a.finishLogin(w, r, account)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, a.sessionCookieFor(r, "", -1))
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}
