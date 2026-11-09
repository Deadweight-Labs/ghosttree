package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Registrieren (REQ-599): /ui/register fragt nur nach dem Einladungslink. Ein
// Konto entsteht erst über die Einladungsseite /join/{code}. Die Seite erkennt die
// Gestalt des Links und prüft ihn wie /join/{code} (gültig, nicht abgelaufen, nicht
// verbraucht), bevor sie dorthin leitet; alles, was nicht taugt, antwortet gleich.

var joinLinkPattern = regexp.MustCompile(`(?i)/join/([0-9a-f]{` + strconv.Itoa(joinCodeLen) + `})`)

func (a *app) registerPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	codeHeaders(w)
	a.render(l, w, "register", pageData{Title: l.msg("register.title")})
}

func (a *app) registerSubmit(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	codeHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	link := strings.TrimSpace(r.FormValue("link"))
	code := ""
	if m := joinLinkPattern.FindStringSubmatch(link); m != nil && wellFormedJoinCode(strings.ToLower(m[1])) {
		code = strings.ToLower(m[1])
	} else if wellFormedJoinCode(strings.ToLower(link)) {
		// A bare code is as good as the whole link.
		code = strings.ToLower(link)
	}
	if code != "" {
		// The link is checked the way /join/{code} checks it (valid, not expired,
		// not used up, inviter still allowed) before it leads there. Whatever is
		// wrong with it gives the one answer, so the page cannot be used to tell an
		// unknown code from an expired or used one; the same limit as for /join
		// holds the guessing back.
		if !a.joinGate(w, r) {
			return
		}
		if _, ok := a.joinPreview(code); !ok {
			w.WriteHeader(http.StatusNotFound)
			a.render(l, w, "register", pageData{Title: l.msg("register.title"), Error: l.msg("register.unknown_link"), Code: link})
			return
		}
		http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	a.render(l, w, "register", pageData{Title: l.msg("register.title"), Error: l.msg("register.not_link"), Code: link})
}
