package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
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
					http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
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
func (a *app) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := r.ParseForm(); err != nil ||
				subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(csrfOf(r))) != 1 ||
				!sameOrigin(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func sameOrigin(r *http.Request) bool {
	origin, err := url.Parse(strings.TrimSpace(r.Header.Get("Origin")))
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	forwardedProto, protoSet := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	forwardedHost, hostSet := singleForwardedValue(r.Header, "X-Forwarded-Host")
	if protoSet || hostSet {
		if !remoteIsLoopback(r.RemoteAddr) || !protoSet || !hostSet ||
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
func remoteIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validForwardedHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\\@\t\r\n") {
		return false
	}
	u, err := url.Parse("https://" + host)
	return err == nil && u.Host == host && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login", pageData{Title: "Login"})
}
func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	principal, ok := a.store.AuthenticatePrincipal(r.FormValue("token"))
	if !ok {
		a.render(w, "login", pageData{Title: "Login", Error: "Invalid token"})
		return
	}
	id, err := a.sessions.create(principal)
	if err != nil {
		http.Error(w, "could not create secure session", http.StatusInternalServerError)
		return
	}
	// Secure is intentionally omitted because the supported private network deployment
	// currently serves plain HTTP. HttpOnly and SameSite still constrain access.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
	http.Redirect(w, r, "/ui/requests", http.StatusSeeOther)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}
