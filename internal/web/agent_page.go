package web

import (
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Agenten auf der neuen Oberfläche (REQ-599, Phase 2 B): die Liste nach
// Rechnern gruppiert, die Seite eines Agenten mit einem Composer, der Nachricht,
// Bitte, Pausieren und Unterbrechen als Signal kennt. Alles nutzt, was es gibt:
// listAgents und agentCards für die Daten, die Raum-Nachricht und die
// Agentensteuerung (agentcontrol.go) für das Senden.

// agentGroup is the agents of one machine, with how the machine is doing.
type agentGroup struct {
	Machine, State, StateText string
	Active                    int
	Cards                     []agentCard
}

// groupAgents puts the cards under their machine, in the order the machines first
// appear; the state of a machine is the one the viewer's own devices report.
func groupAgents(cards []agentCard, machines []machineRow) []agentGroup {
	var out []agentGroup
	index := map[string]int{}
	for _, c := range cards {
		i, ok := index[c.Machine]
		if !ok {
			g := agentGroup{Machine: c.Machine}
			for _, m := range machines {
				if m.Name == c.Machine {
					g.State, g.StateText = m.State, m.StateText
				}
			}
			out = append(out, g)
			i = len(out) - 1
			index[c.Machine] = i
		}
		out[i].Cards = append(out[i].Cards, c)
		if c.Presence != "off" && c.Presence != "idle" {
			out[i].Active++
		}
	}
	return out
}

// agentPageView is the page of one agent.
type agentPageView struct {
	Card     agentCard
	Composer composerView
	Timeline []tlItem
	Control  *coordControlView
	Back     string
}

// findAgent looks the agent up among those the viewer may see. The page answers
// an unknown agent and a hidden one alike.
func (a *app) findAgent(r *http.Request, id string) (agentCard, bool) {
	pa := a.access(r)
	now := overviewNow().UTC()
	rows, err := a.listAgents(r, pa, "", now, agentsMax*4)
	if err != nil {
		return agentCard{}, false
	}
	for _, row := range rows {
		if row.ExternalID == id {
			cards, err := a.agentCards(r, pa, []agentRow{row}, now)
			if err != nil || len(cards) == 0 {
				return agentCard{}, false
			}
			return cards[0], true
		}
	}
	return agentCard{}, false
}

func agentURL(id string) string { return "/ui/agents/" + url.PathEscape(id) }

func (a *app) agentPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	card, ok := a.findAgent(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := &agentPageView{Card: card, Back: agentURL(id)}
	placeholder := l.msg("agent.compose.placeholder", card.Address())
	v.Composer = composerView{ID: "agent-signal", Action: agentURL(id) + "/signal", Token: csrfOf(r), Name: "body", Placeholder: placeholder, ModeLabel: l.msg("agent.signal"),
		Modes: []composerMode{
			{Value: "message", Label: l.msg("agent.signal.message"), Submit: l.msg("coord.send"), Class: "pri", Placeholder: placeholder, Selected: true},
			{Value: "request", Label: l.msg("agent.signal.request"), Submit: l.msg("room.send_ask"), Class: "", Placeholder: placeholder},
			{Value: "pause", Label: l.msg("agent.signal.pause"), Submit: l.msg("age.control.pause"), Class: "", Placeholder: l.msg("age.control.reason"), Tip: l.msg("agent.signal.next_call")},
			{Value: "interrupt", Label: l.msg("agent.signal.interrupt"), Submit: l.msg("age.control.interrupt"), Class: "dangerf", Placeholder: l.msg("age.control.reason"), Tip: l.msg("agent.signal.next_call")},
		}}
	// The control of the agent: its state, and who asked for it.
	for _, p := range a.controlViews(r, card) {
		v.Control = p
	}
	if c, ok, err := a.store.LatestAgentControl(id); err == nil && ok {
		v.Timeline = controlTimeline(l, c)
	}
	data := pageData{Title: card.Address(), AgentV: v}
	// An agent belongs to the organization, not to one repository: the head is the
	// organization's, with the tab Agents open.
	if org, ok := a.orgOfProject(r, a.projectOfRoom(card.RoomKey)); ok {
		data.OrgName, data.OrgBand = org.Name, a.orgBandFor(r, org, "agents")
	}
	a.renderBrowser(w, r, "agent", data)
}

// Address is how an agent is named in a sentence: "claude@laptop", or just the
// name while its machine is not known.
func (r agentRow) Address() string {
	name := strings.ToLower(r.ShortName())
	if r.Machine == "" {
		return name
	}
	return name + "@" + r.Machine
}

// orgOfProject is the organization of the project the viewer can see; without a
// project (or one outside any organization) it is the viewer's default one.
func (a *app) orgOfProject(r *http.Request, remote string) (store.Org, bool) {
	orgs, err := a.store.ListOrgs(browserPrincipal(r).ID)
	if err != nil || len(orgs) == 0 {
		return store.Org{}, false
	}
	if remote != "" {
		if list, err := a.store.ListProjects(browserPrincipal(r).ID, 0); err == nil {
			for _, p := range list {
				if p.Remote == remote {
					for _, o := range orgs {
						if o.ID == p.OrgID {
							return o, true
						}
					}
				}
			}
		}
	}
	for _, o := range orgs {
		if o.Default {
			return o, true
		}
	}
	return orgs[0], true
}

// projectOfRoom is the project behind a project room key, "" for other rooms.
func (a *app) projectOfRoom(key string) string {
	remote, _ := strings.CutPrefix(key, "project:")
	if remote == key {
		return ""
	}
	return remote
}

// controlViews builds the control state of the agent as the participant list does.
func (a *app) controlViews(r *http.Request, card agentCard) []*coordControlView {
	parts := []coordParticipantView{{ID: card.ExternalID, Provider: card.Provider}}
	a.applyParticipantControls(r, card.RoomKey, parts)
	if parts[0].Control == nil {
		return nil
	}
	return []*coordControlView{parts[0].Control}
}

// controlTimeline turns the latest control of an agent into entries of the timeline.
func controlTimeline(l lang, c store.AgentControl) []tlItem {
	name := l.msg("age.control.pause")
	if c.Action == store.ControlInterrupt {
		name = l.msg("age.control.interrupt")
	}
	var out []tlItem
	if c.State == store.ControlResumed {
		out = append(out, tlItem{Who: c.ResumedByLabel, Icon: "play", Tone: "ok", Head: template.HTML(html.EscapeString(l.msg("agent.tl.resumed"))), Age: shortAge(l, overviewNow().UTC(), parseTime(c.ResumedAt))})
	}
	label, detail := controlView(l, c)
	if c.State == store.ControlEffective || c.State == store.ControlAcknowledged || c.State == store.ControlResumed {
		if label != "" && c.State != store.ControlResumed {
			out = append(out, tlItem{Icon: "check", Tone: "acc", Head: template.HTML("<b>" + html.EscapeString(label) + "</b>"), Body: template.HTML(html.EscapeString(detail)), Age: shortAge(l, overviewNow().UTC(), parseTime(firstNonEmpty(c.EffectiveAt, c.AckedAt)))})
		}
	}
	item := tlItem{Who: c.RequestedByLabel, Icon: "pause", Tone: "warn", Head: template.HTML(html.EscapeString(l.msg("agent.tl.requested", name))), Age: shortAge(l, overviewNow().UTC(), parseTime(c.RequestedAt))}
	if c.Reason != "" {
		item.Body = template.HTML(html.EscapeString("„" + c.Reason + "“"))
	}
	return append(out, item)
}

// agentSignal takes the composer of the agent page. A message and a request go to
// the room of the agent as a post that names it; pause and interrupt are the
// agent control. The store checks the rights, as it does for every other route.
func (a *app) agentSignal(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	card, ok := a.findAgent(r, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	back := agentURL(id)
	switch mode := r.FormValue("mode"); mode {
	case "message", "request":
		if body == "" {
			http.Error(w, "a text is required", http.StatusBadRequest)
			return
		}
		if !a.mayEnter(w, r, card.RoomKey) {
			return
		}
		msg := store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: card.RoomKey, ClientID: newFormClientID(r), Body: body, Mentions: []string{card.ExternalID}}
		if mode == "request" {
			msg.Intent = store.IntentQuestion
		}
		mid, err := a.browserCoord(r).Send(msg)
		if err != nil {
			coordHTTPError(l, w, err)
			return
		}
		a.stampActor(r, actor.Message, mid)
		http.Redirect(w, r, back, http.StatusSeeOther)
	case store.ControlPause, store.ControlInterrupt:
		r.Form.Set("agent", id)
		r.Form.Set("room", card.RoomKey)
		r.Form.Set("action", mode)
		r.Form.Set("reason", body)
		r.Form.Set("back", back)
		a.coordAgentControl(w, r)
	default:
		http.Error(w, "unknown signal", http.StatusBadRequest)
	}
}

// ActiveCount is how many of the listed agents are at work or waiting.
func (v agentsView) ActiveCount() int {
	n := 0
	for _, g := range v.Groups {
		n += g.Active
	}
	return n
}

// ShortName is the name an agent goes by: "Claude · Robin · laptop" is Claude.
func (r agentRow) ShortName() string {
	name, _, _ := strings.Cut(r.BareName, " · ")
	return name
}
