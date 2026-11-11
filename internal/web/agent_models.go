package web

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// agentModelsView is the page /ui/agents/models (REQ-620): per harness and
// model what agents did, in the projects the viewer may see in full; people
// form one line for comparison.
type agentModelsView struct {
	Rows  []agentModelRow
	Usage *toolUsageView
}

// toolUsageView is the use of ghosttree's own tools (REQ-436): sessions per
// harness and how many of them called none, then calls per tool and action.
type toolUsageView struct {
	Since            string
	Sessions, Silent int
	Harness          []toolUsageHarnessRow
	Lines            []toolUsageLineRow
}

type toolUsageHarnessRow struct {
	Kind, Name       string
	Sessions, Silent int
}

type toolUsageLineRow struct {
	Tool, Action, Kind, Harness string
	Subagent                    bool
	Calls, Sessions             int
}

type agentModelRow struct {
	// Kind is the avatar kind, Name "Claude" or the people, Model "Opus 5.5"
	// (empty when unknown), Raw the model identifier for the tooltip.
	Kind, Name, Model, Raw string
	Human                  bool
	Entries, Criteria      int
	Requests, Features     int
}

func (a *app) agentModelsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	shell := a.shellFor(r, "agentmodels")
	stats, _, err := a.access(r).ActorStats(selectedProject(shell))
	if err != nil {
		http.Error(w, "could not load", http.StatusInternalServerError)
		return
	}
	v := &agentModelsView{}
	for _, st := range stats {
		row := agentModelRow{Model: st.ModelLabel, Raw: st.Model, Entries: st.Entries, Criteria: st.CriteriaMet,
			Requests: st.RequestsDone, Features: st.FeaturesDone}
		if st.Actor == actor.Human {
			row.Human, row.Kind, row.Name = true, avHuman, l.msg("agents.models_people")
			switch st.Via {
			case actor.ViaWeb:
				row.Name = l.msg("agents.models_people_web")
			case actor.ViaTerminal:
				row.Name = l.msg("agents.models_people_terminal")
			}
		} else {
			row.Kind, row.Name = harnessKind(st.Harness), st.Family
		}
		v.Rows = append(v.Rows, row)
	}
	if rep, err := a.access(r).ToolUseStats(selectedProject(shell), toolUsageDays); err == nil && rep.Since != "" {
		v.Usage = newToolUsageView(rep)
	}
	a.renderBrowser(w, r, "agentmodels", pageData{Title: l.msg("nav.tab_models"), AgentModels: v})
}

// toolUsageDays is the window of the tool statistics.
const toolUsageDays = 30

func newToolUsageView(rep store.ToolUseReport) *toolUsageView {
	u := &toolUsageView{Since: rep.Since, Sessions: rep.Sessions, Silent: rep.Silent}
	if len(u.Since) >= 10 {
		u.Since = u.Since[:10]
	}
	for _, h := range rep.Harness {
		u.Harness = append(u.Harness, toolUsageHarnessRow{Kind: harnessKind(h.Harness), Name: h.Harness, Sessions: h.Sessions, Silent: h.Sessions - h.WithCalls})
	}
	for _, l := range rep.Lines {
		u.Lines = append(u.Lines, toolUsageLineRow{Tool: l.Tool, Action: l.Action, Kind: harnessKind(l.Harness), Harness: l.Harness,
			Subagent: l.Subagent, Calls: l.Calls, Sessions: l.Sessions})
	}
	return u
}
