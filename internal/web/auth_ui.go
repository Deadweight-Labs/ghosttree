package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Registrieren (REQ-599): /ui/register fragt nur nach dem Einladungslink. Ein
// Konto entsteht erst über die Einladungsseite /join/{code}. Die Seite erkennt die
// Gestalt des Links und prüft ihn wie /join/{code} (gültig, nicht abgelaufen, nicht
// verbraucht), bevor sie dorthin leitet; alles, was nicht taugt, antwortet gleich.

var joinLinkPattern = regexp.MustCompile(`(?i)/join/([0-9a-f]{` + strconv.Itoa(joinCodeLen) + `})`)

// bareCodePattern: ein Code ohne Link besteht aus Zeichen, die in Adressen stehen
// dürfen, und ist lang genug, ein Code zu sein.
var bareCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}$`)

func (a *app) registerPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	codeHeaders(w)
	if _, ok := a.joinSession(r); ok {
		http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
		return
	}
	a.render(l, w, "register", pageData{Title: l.msg("register.title")})
}

// registerRejected zeigt die Seite mit dem Fehler über der Karte und dem, was die
// Person eingegeben hat. 422: die Seite ist in Ordnung, die Eingabe nicht.
func (a *app) registerRejected(l lang, w http.ResponseWriter, errKey, link string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(l, w, "register", pageData{Title: l.msg("register.title"), Error: l.msg(errKey), Code: link})
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
	// Was wie ein Einladungslink aussieht: /join/<Code>, der bloße Code, oder der
	// Link der Oberfläche /ui/login/code?code=<Code> (so gibt ihn die Org-Seite aus).
	code := ""
	if m := joinLinkPattern.FindStringSubmatch(link); m != nil && wellFormedJoinCode(strings.ToLower(m[1])) {
		code = strings.ToLower(m[1])
	} else if wellFormedJoinCode(strings.ToLower(link)) {
		// A bare code is as good as the whole link.
		code = strings.ToLower(link)
	}
	loginCode, _ := codeFromInput(link)
	isURL := strings.Contains(link, "://")
	if code == "" && (loginCode == "" || isURL && loginCode == link || !isURL && !bareCodePattern.MatchString(link)) {
		a.registerRejected(l, w, "register.not_link", link)
		return
	}
	// The link is checked the way /join/{code} checks it (valid, not expired, not
	// used up, inviter still allowed) before it leads there. Whatever is wrong with
	// it gives the one answer, so the page cannot be used to tell an unknown code
	// from an expired or used one; the same limit as for /join holds the guessing
	// back.
	if !a.joinGate(w, r) {
		return
	}
	if code != "" {
		if _, ok := a.joinPreview(code); ok {
			http.Redirect(w, r, "/join/"+code, http.StatusSeeOther)
			return
		}
	}
	// An organisation's invitation is redeemed on the one-time-code page, which
	// shows who invited and asks for the name.
	if loginCode != "" && len(loginCode) <= maxCodeLength && a.store.CodeKindFor(loginCode) == store.CodeInvitation {
		http.Redirect(w, r, "/ui/login/code?code="+url.QueryEscape(loginCode), http.StatusSeeOther)
		return
	}
	a.registerRejected(l, w, "register.unknown_link", link)
}
