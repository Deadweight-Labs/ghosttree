package web

import (
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// nextCookie hält die Seite, auf die der Login zurückführen soll. Ein Cookie
// statt eines Formularfelds, damit jeder Anmeldeweg (OIDC, Code, Token) ohne
// Änderung seiner Seite dorthin zurückkehrt. Der Pfad deckt /ui/login und alles
// darunter ab, auch den OIDC-Callback.
const nextCookie = "gt_next"

const nextTTL = 10 * 60

// safeNext lässt nur interne Pfade der Oberfläche durch (kein Schema, kein Host,
// kein "//", kein Backslash, keine Steuer- und Formatzeichen) und nie die
// Anmeldeseiten selbst. Alles andere ist leer: dann gilt das normale Ziel.
func safeNext(raw string) string {
	if raw == "" || len(raw) > 2048 || raw[0] != '/' {
		return ""
	}
	if strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\") {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/ui/") {
		return ""
	}
	if u.Path == "/ui/login" || strings.HasPrefix(u.Path, "/ui/login/") || u.Path == "/ui/logout" {
		return ""
	}
	if strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") {
		return ""
	}
	return raw
}

// loginRedirect schickt einen nicht angemeldeten Browser zur Anmeldung und
// merkt sich dabei die angeforderte Seite, sofern es eine lesende Anfrage auf
// einen internen Pfad ist. Die Adresse bleibt "/ui/login": die Rücksprungadresse
// steht im Cookie, nicht in der URL.
func (a *app) loginRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.setNext(w, r, r.URL.RequestURI())
	}
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// rememberNext legt die Rücksprungadresse aus ?next= ab, wenn sie sicher ist.
func (a *app) rememberNext(w http.ResponseWriter, r *http.Request) {
	a.setNext(w, r, r.URL.Query().Get("next"))
}

func (a *app) setNext(w http.ResponseWriter, r *http.Request, raw string) {
	next := safeNext(raw)
	if next == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: nextCookie, Value: url.QueryEscape(next), Path: "/ui/login", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: nextTTL})
}

// takeNext liest und löscht die Rücksprungadresse; ohne gültige gilt fallback.
// Der Wert wird beim Lesen erneut geprüft, das Cookie ist Nutzereingabe.
func (a *app) takeNext(w http.ResponseWriter, r *http.Request, fallback string) string {
	c, err := r.Cookie(nextCookie)
	if err != nil {
		return fallback
	}
	http.SetCookie(w, &http.Cookie{Name: nextCookie, Value: "", Path: "/ui/login", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	value, err := url.QueryUnescape(c.Value)
	if err != nil {
		return fallback
	}
	if next := safeNext(value); next != "" {
		return next
	}
	return fallback
}
