package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Willkommenskarte (REQ-676, P20): nach dem ersten Konto einer Instanz und nach
// jedem Beitritt in eine Organisation zeigt der Posteingang (bei der leeren
// Instanz die Einrichtung) eine kleine Karte mit dem Namen der Organisation und
// den nächsten Schritten. Sie verschwindet, wenn die Schritte erledigt sind, oder
// wenn man sie wegklickt. Der Store merkt sich nur, dass es sie gibt und ob sie
// weggeklickt wurde (account_welcome); was erledigt ist, wird bei jedem Anzeigen
// aus dem Zustand gelesen.

type welcomeStep struct {
	Label, Href string
	Done        bool
}

type welcomeView struct {
	OrgID   int64
	OrgName string
	Steps   []welcomeStep
	// Pending is the masked address that still waits for its confirmation.
	Pending string
}

// welcomeCards liefert die Karten, die jetzt anzuzeigen sind. Erledigte Karten
// werden dabei beendet.
func (a *app) welcomeCards(r *http.Request, l lang) []welcomeView {
	me := browserPrincipal(r)
	if me.TokenKind != store.WebSessionKind {
		return nil
	}
	cards := a.store.WelcomeCards(me.ID)
	if len(cards) == 0 {
		return nil
	}
	devices, _ := a.deviceTokens(r)
	pending := a.store.PendingEmail(me.ID)
	var out []welcomeView
	for _, c := range cards {
		owner := a.store.OrgRole(c.OrgID, me.ID) == store.OrgOwner
		v := welcomeView{OrgID: c.OrgID, OrgName: c.OrgName}
		open := 0 // Schritte, die noch zu tun sind und die Karte halten
		if pending != "" {
			v.Pending = store.MaskEmail(pending)
			open++
		}
		if owner {
			invites, _ := a.store.ListInvitations(me.ID, c.OrgID)
			members, _ := a.store.ListOrgMembers(c.OrgID)
			done := len(invites) > 0 || len(members) > 1
			v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.invite"), Href: "/ui/orgs?org=" + c.OrgSlug + "#invitations", Done: done})
			if !done {
				open++
			}
		}
		connected := len(devices) > 0
		v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.device"), Href: "/join/pair", Done: connected})
		if !connected {
			open++
		}
		// Das Repo zu öffnen ist ein Weg, kein Zustand: der Schritt hält die Karte nicht.
		v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.repo"), Href: "/ui/orgs?org=" + c.OrgSlug + "#projects"})
		if open == 0 {
			_ = a.store.DismissWelcome(me.ID, c.OrgID)
			continue
		}
		out = append(out, v)
	}
	return out
}

// welcomeDismiss blendet eine Karte aus.
func (a *app) welcomeDismiss(w http.ResponseWriter, r *http.Request) {
	if id, err := strconv.ParseInt(r.FormValue("org"), 10, 64); err == nil {
		_ = a.store.DismissWelcome(browserPrincipal(r).ID, id)
	}
	back := "/ui/inbox"
	if b := r.FormValue("back"); strings.HasPrefix(b, "/ui/") && !strings.HasPrefix(b, "//") {
		back = b
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// principalNumber is the number in a principal id ("person:7").
func principalNumber(principal string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(principal, "person:"), 10, 64)
}
