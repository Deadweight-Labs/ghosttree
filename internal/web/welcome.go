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
	orgs := a.orgsOf(r)
	work := a.pickOrg(r, orgs)
	var out []welcomeView
	shown := map[int64]bool{}
	for _, c := range cards {
		// The card of an empty standard organization nobody uses names the one the
		// person works in (REQ-683): "You are in Default" after creating an own
		// organization is wrong. The stored card is still the one that is dismissed.
		org := store.Org{ID: c.OrgID, Name: c.OrgName, Slug: c.OrgSlug}
		for _, o := range orgs {
			if o.ID == c.OrgID && a.placeholderOrg(me.ID, o) && work.ID != 0 && work.ID != o.ID && !work.IsUser() && !work.Guest {
				org = work
			}
		}
		if shown[org.ID] {
			continue
		}
		owner := store.OrgRoleHas(a.store.OrgRole(org.ID, me.ID), store.RightOrgManage)
		v := welcomeView{OrgID: c.OrgID, OrgName: org.Name}
		open := 0 // Schritte, die noch zu tun sind und die Karte halten
		if pending != "" {
			v.Pending = store.MaskEmail(pending)
			open++
		}
		// A step that is done is not shown again: the card lists what is left.
		if owner {
			invites, _ := a.store.ListInvitations(me.ID, org.ID)
			members, _ := a.store.ListOrgMembers(org.ID)
			if done := len(invites) > 0 || len(members) > 1; !done {
				v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.invite"), Href: "/ui/orgs?org=" + org.Slug + "#invitations"})
				open++
			}
		}
		if len(devices) == 0 {
			v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.device"), Href: "/ui/overview?connect=1"})
			open++
		}
		if open == 0 {
			_ = a.store.DismissWelcome(me.ID, c.OrgID)
			continue
		}
		// Das Repo zu öffnen ist ein Weg, kein Zustand: der Schritt hält die Karte nicht.
		v.Steps = append(v.Steps, welcomeStep{Label: l.msg("welcome.repo"), Href: "/ui/orgs?org=" + org.Slug + "#projects"})
		shown[org.ID] = true
		out = append(out, v)
	}
	return out
}

// welcomeDismiss blendet eine Karte aus.
func (a *app) welcomeDismiss(w http.ResponseWriter, r *http.Request) {
	if id, err := strconv.ParseInt(r.FormValue("org"), 10, 64); err == nil {
		_ = a.store.DismissWelcome(browserPrincipal(r).ID, id)
	}
	// Back to one of the two pages that show the card; a value from the form never
	// becomes the address itself.
	back := "/ui/inbox"
	if r.FormValue("back") == "/ui/overview" {
		back = "/ui/overview"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// principalNumber is the number in a principal id ("person:7").
func principalNumber(principal string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(principal, "person:"), 10, 64)
}
