package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// emptyHint is the sentence of an empty session list and its one action.
type emptyHint struct {
	Text, Action, Href string
}

// sessionsEmptyHint says what an empty list of sessions lacks, without
// contradicting what the viewer already did. An agent that runs, or a machine that
// signed in, is a connected agent: what is missing then is the transcripts, which
// reach the server through ctx watch on that machine. Only with nothing
// connected at all does the list ask to connect an agent.
func (a *app) sessionsEmptyHint(r *http.Request, pa *store.ProjectAccess, l lang, now time.Time) emptyHint {
	var machines []machineRow
	if tokens, err := a.deviceTokens(r); err == nil {
		machines = ownMachines(l, tokens, now)
	}
	var running string
	if rows, err := a.listAgents(r, pa, "", now, agentsMax); err == nil {
		for _, row := range rows {
			if row.Presence != "off" && row.Presence != "ended" {
				running = row.Machine
				if running == "" && len(machines) > 0 {
					running = machines[0].Name
				}
				break
			}
		}
	}
	switch {
	case running != "":
		return emptyHint{Text: l.msg("sessions.empty_agent", running), Action: l.msg("sessions.open_machine", running), Href: machineHrefOf(running)}
	case len(machines) > 0:
		return emptyHint{Text: l.msg("sessions.empty_machine", machines[0].Name), Action: l.msg("sessions.open_machine", machines[0].Name), Href: machines[0].Href}
	}
	return emptyHint{Text: l.msg("sessions.empty"), Action: l.msg("sessions.connect"), Href: "/ui/overview?connect=1"}
}

// machineHrefOf is the page of a machine by its name, as /ui/m/{machine} answers it.
func machineHrefOf(name string) string {
	return "/ui/m/" + url.PathEscape(strings.ToLower(strings.TrimSpace(name)))
}
