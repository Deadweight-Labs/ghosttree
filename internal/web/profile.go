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
	if r.URL.Query().Get("notice") == "saved" {
		v.Notice = l.msg("profile.saved")
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
		http.Redirect(w, r, "/ui/profile?notice=saved", http.StatusSeeOther)
	case errors.Is(err, store.ErrAccountNameTaken):
		a.renderProfile(w, r, http.StatusConflict, r.FormValue("name"), l.msg("profile.err.taken"))
	case errors.Is(err, store.ErrNameRateLimited):
		a.renderProfile(w, r, http.StatusTooManyRequests, "", l.msg("profile.err.rate"))
	case errors.Is(err, store.ErrNameNotAllowed):
		a.renderProfile(w, r, http.StatusForbidden, "", l.msg("profile.err.locked"))
	case errors.Is(err, store.ErrInvalidInput):
		a.renderProfile(w, r, http.StatusBadRequest, r.FormValue("name"), l.msg("profile.err.invalid"))
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
