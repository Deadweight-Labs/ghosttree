package web

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// languageSwitch is the switcher DE | EN (REQ-715). A signed-in person gets the
// choice on the account, so it holds on every device; everyone gets the cookie,
// which is all a page without a sign-in (login, join) has. The answer goes back
// to the page the switcher was on.
//
// The route is public because the login and join pages carry the switcher too;
// the same-origin check stands in for a session there. The account is only
// written with the CSRF token of the session.
func (a *app) languageSwitch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil || !a.sameOrigin(r) {
		w.Header().Set(rejectHeader, "csrf")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	choice, ok := parseChoice(r.PostFormValue("language"))
	if !ok {
		http.Error(w, "unknown language", http.StatusBadRequest)
		return
	}
	signedIn := false
	if c, err := a.sessionCookieOf(r); err == nil {
		if session, ok := a.sessions.get(c.Value); ok && a.store.PrincipalValid(session.principal) {
			// A form without a token (the switcher of a sign-in or join page a
			// signed-in browser happens to see) only sets the cookie; a wrong
			// token is refused.
			if token := r.PostFormValue("csrf_token"); token != "" {
				if subtle.ConstantTimeCompare([]byte(token), []byte(session.csrf)) != 1 {
					w.Header().Set(rejectHeader, "csrf")
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				if err := a.store.SetAccountLanguage(session.principal.ID, string(choice)); err != nil {
					http.Error(w, a.langFor(r).msg("adm.err.generic"), http.StatusInternalServerError)
					return
				}
				signedIn = true
			}
		}
	}
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: string(choice), Path: "/", HttpOnly: true,
		Secure: a.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: langCookieTTL})
	w.Header().Set("Cache-Control", "no-store")
	fallback := "/ui/login"
	if signedIn {
		fallback = "/ui/profile"
	}
	http.Redirect(w, r, languageBack(r, fallback), http.StatusSeeOther)
}

// languageBack is the page the switcher returns to: the field "back" of the form,
// else the page the browser came from, else fallback. Only a path of this site is
// ever used, never the switcher itself or the sign-out.
func languageBack(r *http.Request, fallback string) string {
	if back := safeBack(r.PostFormValue("back")); back != "" {
		return back
	}
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && strings.EqualFold(ref.Host, r.Host) {
		if back := safeBack(ref.RequestURI()); back != "" {
			return back
		}
	}
	return fallback
}

// safeBack is a path of the interface or the join pages, login pages included.
func safeBack(raw string) string {
	u, ok := cleanLocal(raw)
	if !ok {
		return ""
	}
	if u.Path == "/ui/language" || u.Path == "/ui/logout" {
		return ""
	}
	if rest, ok := strings.CutPrefix(u.Path, "/join/"); ok {
		if u.RawQuery == "" && u.Fragment == "" && (rest == "pair" || (wellFormedJoinCode(strings.TrimSuffix(rest, "/login")) && !strings.Contains(raw, "%"))) {
			return raw
		}
		return ""
	}
	if !strings.HasPrefix(u.Path, "/ui/") || strings.Contains(u.Path, "/../") || strings.HasSuffix(u.Path, "/..") {
		return ""
	}
	return raw
}

// adoptLanguageCookie keeps a choice made on the sign-in page: the first sign-in
// of an account without a language of its own takes the one the cookie holds.
func (a *app) adoptLanguageCookie(r *http.Request, principal store.Principal) {
	c, err := r.Cookie(langCookie)
	if err != nil || principal.ID == "" {
		return
	}
	if l, ok := parseChoice(c.Value); ok && a.store.AccountLanguage(principal.ID) == "" {
		_ = a.store.SetAccountLanguage(principal.ID, string(l))
	}
}
