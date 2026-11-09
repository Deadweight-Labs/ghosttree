package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Registrieren (REQ-599, Phase 2 B): /ui/register fragt nur nach dem
// Einladungslink. Ein Konto entsteht erst über die Einladungsseite /join/{code};
// die Prüfung des Links selbst liefert später der Server (Orchestrator). Bis dahin
// erkennt die Seite die Gestalt des Links und leitet auf die vorhandene Seite.

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
	if m := joinLinkPattern.FindStringSubmatch(link); m != nil && wellFormedJoinCode(strings.ToLower(m[1])) {
		http.Redirect(w, r, "/join/"+strings.ToLower(m[1]), http.StatusSeeOther)
		return
	}
	// A bare code is as good as the whole link.
	if wellFormedJoinCode(strings.ToLower(link)) {
		http.Redirect(w, r, "/join/"+strings.ToLower(link), http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	a.render(l, w, "register", pageData{Title: l.msg("register.title"), Error: l.msg("register.not_link"), Code: link})
}
