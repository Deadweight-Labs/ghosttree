package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (a *api) createSession(w http.ResponseWriter, r *http.Request) {
	var s store.Session
	if err := readJSON(r, &s); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Harness == "" || s.ExternalID == "" {
		writeErr(w, http.StatusBadRequest, "harness and external_id are required")
		return
	}
	id, err := a.st.UpsertSession(s)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Scope.Machine != "" {
		a.st.TouchMachine(s.Scope.Machine)
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *api) appendChunks(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	var body struct {
		Chunks []store.Chunk `json:"chunks"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.AppendChunks(id, body.Chunks); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := a.st.ListSessions(axesFromQuery(r), intParam(r, "limit", 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, sessions)
}

func (a *api) readSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	chunks, err := a.st.ReadSession(id, intParam(r, "from", 0), intParam(r, "limit", 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, chunks)
}

// knowledgeRequest is store.Knowledge plus the auto_scope envelope: when the
// caller sends no scope at all, the server applies the write defaults for the
// context it was given.
type knowledgeRequest struct {
	store.Knowledge
	AutoScope *struct {
		Context scope.Axes `json:"context"`
	} `json:"auto_scope"`
}

func (a *api) createKnowledge(w http.ResponseWriter, r *http.Request) {
	var req knowledgeRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	k := req.Knowledge
	if k.Type == "" || k.Title == "" {
		writeErr(w, http.StatusBadRequest, "type and title are required")
		return
	}
	if k.Scope == (scope.Axes{}) && req.AutoScope != nil {
		k.Scope = scope.DefaultAxes(k.Type, req.AutoScope.Context)
	}
	k.Person = personOf(r)
	id, err := a.st.InsertKnowledge(k)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := a.st.KnowledgeByID(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, saved)
}

func (a *api) listKnowledge(w http.ResponseWriter, r *http.Request) {
	ks, err := a.st.KnowledgeForContext(axesFromQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, ks)
}

func (a *api) patchKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var patch map[string]string
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.UpdateKnowledge(id, patch); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type searchResult struct {
	Knowledge []store.Knowledge  `json:"knowledge"`
	Sessions  []store.SessionHit `json:"sessions"`
}

func (a *api) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	filter := axesFromQuery(r)
	limit := intParam(r, "limit", 20)
	res := searchResult{Knowledge: []store.Knowledge{}, Sessions: []store.SessionHit{}}
	if kind == "knowledge" || kind == "all" {
		// scope=union searches what the session would read, not an exact match.
		search := a.st.SearchKnowledge
		if r.URL.Query().Get("scope") == "union" {
			search = a.st.SearchKnowledgeForContext
		}
		ks, err := search(q, filter, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Knowledge = ks
	}
	if kind == "sessions" || kind == "all" {
		hits, err := a.st.SearchSessions(q, filter, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Sessions = hits
	}
	writeJSON(w, 200, res)
}

const defaultBudget = 4000

func (a *api) bootstrap(w http.ResponseWriter, r *http.Request) {
	entries, err := a.st.KnowledgeForContext(axesFromQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	fmt.Fprint(w, renderBootstrap(entries, intParam(r, "budget", defaultBudget)))
}

// renderBootstrap builds the auto-injected context package: active entries of
// the scope, grouped by type, verified before observation, hard char budget.
func renderBootstrap(entries []store.Knowledge, budget int) string {
	if budget <= 0 {
		budget = defaultBudget
	}
	if len(entries) == 0 {
		return ""
	}
	byType := map[string][]store.Knowledge{}
	for _, k := range entries {
		byType[k.Type] = append(byType[k.Type], k)
	}
	var b strings.Builder
	b.WriteString("## Known context (ghosttree)\n")
	truncated := false
	for _, t := range []string{"decision", "pitfall", "note", "plan"} {
		group := byType[t]
		if len(group) == 0 {
			continue
		}
		header := "\n### " + t + "\n"
		for i, k := range group {
			line := fmt.Sprintf("- [%s] %s — %s\n", scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), 200))
			if i == 0 {
				line = header + line
			}
			if b.Len()+len(line) > budget {
				truncated = true
				break
			}
			b.WriteString(line)
		}
		if truncated {
			break
		}
	}
	if truncated {
		b.WriteString("…(truncated, use context_search for more)\n")
	}
	return b.String()
}

func scopeLabel(ax scope.Axes) string {
	var parts []string
	if ax.Project != "" {
		p := ax.Project
		if ax.Branch != "" {
			p += "@" + ax.Branch
		}
		parts = append(parts, p)
	}
	if ax.Machine != "" {
		parts = append(parts, "machine:"+ax.Machine)
	}
	if len(parts) == 0 {
		return "global"
	}
	return strings.Join(parts, " ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
