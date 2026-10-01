package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// coordControlView is the pause control of one agent in the participant list.
// The state text only says "pausiert" for an effective control: a hook
// acknowledgement and the transcript entry for the same tool call (AC-1229).
type coordControlView struct {
	Agent, Room, CSRFToken string
	// CanControl: the viewer is an interactive human who may pause this agent.
	CanControl bool
	// Unsupported names the harness gap instead of showing a dead button.
	Unsupported bool
	Active      bool
	Label       string
	Detail      string
	Gap         string
	Blocked     string
	Who         string
}

const (
	controlGapText    = "Pause am nächsten Werkzeugaufruf; ein laufender Aufruf wird nicht abgebrochen (Lücke)"
	controlIdleHint   = "Wirkt am nächsten Werkzeugaufruf. Ein laufender Aufruf und eine Antwort ohne Werkzeug werden nicht abgebrochen (Lücke)."
	controlResumeHint = "Fortsetzen hebt die Sperre auf; der Agent arbeitet erst nach einer neuen Eingabe weiter."
)

func controlView(c store.AgentControl) (label, detail string) {
	name := "Pause"
	if c.Action == store.ControlInterrupt {
		name = "Unterbrechung"
	}
	switch c.State {
	case store.ControlRequested:
		return name + " angefordert", "Noch nicht bestätigt, nicht pausiert: der Channel des Agenten hat den Hook noch nicht melden sehen, oder der Agent ruft gerade keine Werkzeuge auf."
	case store.ControlAcknowledged:
		return "Bestätigt (Hook)", "Der Hook hat einen Werkzeugaufruf blockiert. Das Transkript belegt den Stopp noch nicht, deshalb gilt der Agent noch nicht als pausiert."
	case store.ControlEffective:
		return "Pausiert (belegt)", "Hook-Bestätigung und Transkript-Beleg (hook_stopped_continuation) für denselben Aufruf liegen vor."
	case store.ControlResumed:
		return "Fortgesetzt", "Die Sperre ist aufgehoben."
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
		if p.Provider == "codex" {
			p.Control = &coordControlView{Agent: p.ID, Unsupported: true}
			continue
		}
		if p.Provider != "claude" {
			continue
		}
		v := &coordControlView{Agent: p.ID, Room: roomKey, CSRFToken: csrfOf(r), CanControl: a.store.MayControlAgent(actor, p.ID)}
		if c, ok, err := a.store.LatestAgentControl(p.ID); err == nil && ok {
			v.Label, v.Detail = controlView(c)
			v.Active = c.State != store.ControlResumed
			v.Who = c.RequestedByLabel
			if c.State == store.ControlResumed {
				v.Who = c.ResumedByLabel
			}
			if c.Gap != "" && v.Active {
				v.Gap = controlGapText
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
	s := "Blockierte Aufrufe: " + itoa(acks)
	if len(names) > 0 {
		s += " (Subagent: " + strings.Join(names, ", ") + ")"
	}
	return s
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{digits[n%10]}, b...)
	}
	return string(b)
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
	case errors.Is(err, store.ErrControlForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case errors.Is(err, store.ErrControlNotFound):
		http.Error(w, "agent or control not found", http.StatusNotFound)
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
