package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "gt_session"

type personKey struct{}
type browserSession struct {
	person  string
	expires time.Time
}
type sessions struct {
	mu     sync.Mutex
	values map[string]browserSession
}

func newSessions() *sessions { return &sessions{values: map[string]browserSession{}} }
func (s *sessions) create(person string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	s.mu.Lock()
	s.values[id] = browserSession{person: person, expires: time.Now().Add(30 * 24 * time.Hour)}
	s.mu.Unlock()
	return id, nil
}
func (s *sessions) get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[id]
	if !ok || time.Now().After(v.expires) {
		delete(s.values, id)
		return "", false
	}
	v.expires = time.Now().Add(30 * 24 * time.Hour)
	s.values[id] = v
	return v.person, true
}
func (s *sessions) remove(id string)  { s.mu.Lock(); delete(s.values, id); s.mu.Unlock() }
func personOf(r *http.Request) string { v, _ := r.Context().Value(personKey{}).(string); return v }
func (a *app) requirePerson(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if person, ok := a.sessions.get(cookie.Value); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, person)))
				return
			}
		}
		http.Redirect(w, r, "/ui/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}
func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login", pageData{Title: "Login"})
}
func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	person, ok := a.store.Authenticate(r.FormValue("token"))
	if !ok {
		a.render(w, "login", pageData{Title: "Login", Error: "Invalid token"})
		return
	}
	id, err := a.sessions.create(person)
	if err != nil {
		http.Error(w, "could not create secure session", http.StatusInternalServerError)
		return
	}
	// Secure is intentionally omitted because the supported private network deployment
	// currently serves plain HTTP. HttpOnly and SameSite still constrain access.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 60 * 60})
	next := r.FormValue("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/ui/requests"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}
