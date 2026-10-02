package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// coordControlView is the pause control of one agent in the participant list.
// The state text only says "paused" for an effective control: a hook
// acknowledgement and the transcript entry for the same tool call (AC-1229).
type coordControlView struct {
	Agent, Room, CSRFToken string
	// CanControl: the viewer is an interactive human who may pause this agent.
	CanControl bool
	// Unsupported names the harness gap instead of showing a dead button.
	Unsupported bool
	// GapText names why the agent cannot be paused (Unsupported).
	GapText string
	// CanResume: the viewer may lift the active control (requester or equal rank).
	CanResume bool
	Active    bool
	Label     string
	Detail    string
	Gap       string
	Blocked   string
	Who       string
}

func controlView(c store.AgentControl) (label, detail string) {
	name := msg("age.control.pause")
	if c.Action == store.ControlInterrupt {
		name = msg("age.control.interrupt")
	}
	switch c.State {
	case store.ControlRequested:
		return msg("age.control.requested", name), msg("age.control.requested.detail")
	case store.ControlAcknowledged:
		return msg("age.control.acknowledged"), msg("age.control.acknowledged.detail")
	case store.ControlEffective:
		return msg("age.control.effective"), msg("age.control.effective.detail")
	case store.ControlResumed:
		return msg("age.control.resumed"), msg("age.control.resumed.detail")
	}
	return "", ""
}

// applyParticipantControls ergänzt jeden Claude-Agenten um seinen Pausenstand.
func (a *app) applyParticipantControls(r *http.Request, roomKey string, participants []coordParticipantView) {
	actor := browserPrincipal(r)
	for i := range participants {
		p := &participants[i]
		if p.Current || p.Provider == "" {
			continue
		}
		if p.Provider != "claude" && p.Provider != "codex" {
			continue
		}
		if gap := a.store.AgentControlGap(p.ID); gap != "" {
			p.Control = &coordControlView{Agent: p.ID, Unsupported: true, GapText: gap}
			continue
		}
		v := &coordControlView{Agent: p.ID, Room: roomKey, CSRFToken: csrfOf(r), CanControl: a.store.MayControlAgent(actor, p.ID),
			CanResume: a.store.MayResumeAgentControl(actor, p.ID)}
		if c, ok, err := a.store.LatestAgentControl(p.ID); err == nil && ok {
			v.Label, v.Detail = controlView(c)
			v.Active = c.State != store.ControlResumed
			v.Who = c.RequestedByLabel
			if c.State == store.ControlResumed {
				v.Who = c.ResumedByLabel
			}
			if c.Gap != "" && v.Active {
				v.Gap = msg("age.control.gap")
			}
			v.Blocked = blockedSummary(c)
		}
		p.Control = v
	}
}

func blockedSummary(c store.AgentControl) string {
	acks := 0
	subs := map[string]bool{}
	var names []string
	for _, e := range c.Events {
		if e.Kind != store.ControlEventAck {
			continue
		}
		acks++
		if e.AgentID != "" && !subs[e.AgentID] {
			subs[e.AgentID] = true
			names = append(names, e.AgentID)
		}
	}
	if acks == 0 {
		return ""
	}
	if len(names) > 0 {
		return msg("age.control.blocked_by", acks, strings.Join(names, ", "))
	}
	return msg("age.control.blocked", acks)
}

// coordAgentControl nimmt Pause, Unterbrechung und Fortsetzen entgegen. Die
// Route läuft hinter requireInteractive; der Store prüft Rolle und Sitzungsart
// noch einmal.
func (a *app) coordAgentControl(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor := browserPrincipal(r)
	agent := strings.TrimSpace(r.FormValue("agent"))
	var err error
	switch action := r.FormValue("action"); action {
	case store.ControlPause, store.ControlInterrupt:
		_, err = a.store.RequestAgentControl(actor, agent, action, r.FormValue("reason"), store.RoleViaWeb)
	case "resume":
		_, err = a.store.ResumeAgentControl(actor, agent)
	default:
		http.Error(w, "action must be pause, interrupt or resume", http.StatusBadRequest)
		return
	}
	switch {
	case errors.Is(err, store.ErrControlForbidden), errors.Is(err, store.ErrControlResumeRank):
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case errors.Is(err, store.ErrControlNotFound):
		http.Error(w, "agent or control not found", http.StatusNotFound)
		return
	case errors.Is(err, store.ErrControlUnsupported):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrControlActive):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, store.ErrInvalidInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/coord?room="+url.QueryEscape(r.FormValue("room")), http.StatusSeeOther)
}
