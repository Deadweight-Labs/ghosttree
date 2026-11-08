package server

import (
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// PR und Wissen (REQ-597, Kriterium 4). Die Route hängt am Projekt wie die
// Pakete: wer das Wissen des Projekts lesen darf, sieht auch, was zu einem PR
// gesagt wurde, und jeder Eintrag läuft durch dieselbe Sichtprüfung wie in der
// Suche. Ein PR, den es nicht gibt, und einer, zu dem nichts gesagt wurde,
// antworten gleich.
func init() { accessRoutes["GET /api/pulls/{n}/knowledge"] = classProject }

func (a *api) registerPullRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/pulls/{n}/knowledge", a.pullKnowledge)
}

// maxPullKnowledge begrenzt die Liste einer Antwort.
const maxPullKnowledge = 500

func (a *api) pullKnowledge(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n <= 0 {
		writeCoded(w, http.StatusBadRequest, "invalid_pr", "pr must be a positive pull request number")
		return
	}
	if !a.listGate(w, r, project, store.ResKnowledge) {
		return
	}
	brief, err := a.st.PullRequestBriefOf(project, n)
	if err != nil {
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	pa := a.access(r)
	pa.Filtered()
	seen := brief.Knowledge[:0:0]
	for _, k := range brief.Knowledge {
		if len(seen) >= maxPullKnowledge {
			break
		}
		full, err := a.st.KnowledgeByID(k.ID)
		if err != nil || !pa.CanSeeKnowledge(full) {
			continue
		}
		seen = append(seen, k)
	}
	brief.Knowledge = seen
	if len(brief.Ghosts) > 0 && !pa.CanSeeGhost(store.GhostFile{Project: project}) {
		brief.Ghosts = []string{}
	}
	if len(brief.Ghosts) > maxPullKnowledge {
		brief.Ghosts = brief.Ghosts[:maxPullKnowledge]
	}
	writeJSON(w, http.StatusOK, brief)
}
