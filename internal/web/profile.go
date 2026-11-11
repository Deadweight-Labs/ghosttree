package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// profileView is what the profile page shows: the name as the store has it.
type profileView struct {
	Name   string
	Notice string
	Locked bool // guests cannot rename themselves
	// Agents are the agents of this account, by machine.
	Agents     []agentGroup
	AgentCount int
	// Devices are the device tokens of the account, newest contact first.
	Devices []profileDevice
	// GitHub is the GitHub link block; nil when there is nothing to show.
	GitHub *profileGitHub
}

type profileDevice struct{ Name, Seen string }

// profilePage shows the account's display name and, in an interactive session,
// the form to change it.
func (a *app) profilePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.renderProfile(w, r, http.StatusOK, "", "")
}

func (a *app) renderProfile(w http.ResponseWriter, r *http.Request, status int, typed, errMsg string) {
	l := a.langFor(r)
	me := browserPrincipal(r)
	acct, err := a.store.AccountByPrincipalID(me.ID)
	if err != nil {
		http.Error(w, l.msg("adm.load_failed"), http.StatusInternalServerError)
		return
	}
	v := profileView{Name: acct.Name, Locked: a.store.NameLocked(me.ID)}
	if typed != "" {
		v.Name = typed
	}
	if who := a.shellBaseFor(r).who; who.kind != viewerGuest {
		v.Agents = a.ownAgentGroups(r, acct.Name)
		for _, g := range v.Agents {
			v.AgentCount += len(g.Cards)
		}
		if tokens, err := a.deviceTokens(r); err == nil {
			now := overviewNow().UTC()
			for _, t := range tokens {
				name := t.Label
				if name == "" {
					name = t.Machine
				}
				at := parseTime(t.LastUsedAt)
				if at.IsZero() {
					at = parseTime(t.CreatedAt)
				}
				v.Devices = append(v.Devices, profileDevice{Name: name, Seen: shortAgo(l, now, at)})
			}
		}
	}
	if gh, ok := a.githubBlock(r); ok {
		v.GitHub = &gh
	}
	switch r.URL.Query().Get("notice") {
	case "saved":
		v.Notice = l.msg("profile.saved")
	case "renamed":
		v.Notice = l.msg("profile.saved_as", v.Name)
	case "github_linked":
		v.Notice = l.msg("profile.gh.linked")
	case "github_unlinked":
		v.Notice = l.msg("profile.gh.unlinked")
	case "github_failed":
		if errMsg == "" {
			errMsg = l.msg("profile.gh.failed")
		}
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	a.renderBrowser(w, r, "profile", pageData{Title: l.msg("profile.title"), Error: errMsg, Profile: v})
}

// profileSave changes the display name. The store cleans and checks it; a
// rejected name is shown again with the reason, never echoed unescaped.
func (a *app) profileSave(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	me := browserPrincipal(r)
	acct, err := a.store.SetOwnName(me.ID, r.FormValue("name"))
	switch {
	case err == nil:
		a.sessions.relabel(acct.ID, acct.Name)
		// The store may have cleaned the name; then the page says what it became.
		notice := "saved"
		if strings.Join(strings.Fields(r.FormValue("name")), " ") != acct.Name {
			notice = "renamed"
		}
		http.Redirect(w, r, "/ui/profile?notice="+notice, http.StatusSeeOther)
	// A refused name goes back to the form over the field, with what was typed in it;
	// the page keeps showing the name the account has.
	case errors.Is(err, store.ErrAccountNameTaken):
		a.failFormTo(w, r, "/ui/profile", http.StatusConflict, l.msg("profile.err.taken"), "name")
	case errors.Is(err, store.ErrNameRateLimited):
		a.failFormTo(w, r, "/ui/profile", http.StatusTooManyRequests, l.msg("profile.err.rate"), "name")
	case errors.Is(err, store.ErrNameNotAllowed):
		a.failFormTo(w, r, "/ui/profile", http.StatusForbidden, l.msg("profile.err.locked"), "name")
	case errors.Is(err, store.ErrInvalidInput):
		a.failFormTo(w, r, "/ui/profile", http.StatusBadRequest, l.msg("profile.err.invalid"), "name")
	default:
		http.Error(w, l.msg("adm.err.generic"), http.StatusInternalServerError)
	}
}

// ownAgentGroups are the agents that run for this account, whatever project they
// work at, by machine.
func (a *app) ownAgentGroups(r *http.Request, name string) []agentGroup {
	pa := a.access(r)
	now := overviewNow().UTC()
	rows, err := a.listAgents(r, pa, "", now, agentsMax*4)
	if err != nil {
		return nil
	}
	var own []agentRow
	for _, row := range rows {
		if strings.EqualFold(row.Owner, name) {
			own = append(own, row)
		}
	}
	cards, err := a.agentCards(r, pa, own, now)
	if err != nil {
		return nil
	}
	return groupAgents(cards, nil)
}
