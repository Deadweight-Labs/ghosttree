package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
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

// rawSession reconstructs the original transcript as newline-delimited JSON.
// The harnesses expire their own transcripts, so this is what makes ghosttree
// the long-term copy rather than just an index of one.
func (a *api) rawSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	lines, err := a.st.SessionRaw(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.WriteHeader(200)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
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
	var ks []store.Knowledge
	var err error
	if r.URL.Query().Get("include_archived") == "1" {
		ks, err = a.st.KnowledgeForProject(r.URL.Query().Get("project"))
	} else {
		ks, err = a.st.KnowledgeForContext(axesFromQuery(r))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, ks)
}

func (a *api) insertMigratedKnowledge(w http.ResponseWriter, r *http.Request) {
	var in store.MigratedEntry
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Knowledge.Person = personOf(r)
	saved, err := a.st.InsertMigrated(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *api) beginMigration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project   string            `json:"project"`
		Artifacts map[string]string `json:"artifacts"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := a.st.BeginMigration(body.Project, body.Artifacts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (a *api) completeMigration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad migration id")
		return
	}
	if err := a.st.CompleteMigration(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) completedMigrationArtifacts(w http.ResponseWriter, r *http.Request) {
	out, err := a.st.CompletedMigrationArtifacts(r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PendingEntry is a knowledge entry plus what a human needs to judge it.
type PendingEntry struct {
	Knowledge  store.Knowledge  `json:"knowledge"`
	Evidence   []store.Evidence `json:"evidence"`
	Recurrence int              `json:"recurrence"`
}

func (a *api) pendingKnowledge(w http.ResponseWriter, r *http.Request) {
	ks, err := a.st.PendingKnowledge(intParam(r, "limit", 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []PendingEntry{}
	for _, k := range ks {
		ev, err := a.st.EvidenceFor(k.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, err := a.st.Recurrence(k.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, PendingEntry{Knowledge: k, Evidence: ev, Recurrence: n})
	}
	writeJSON(w, 200, out)
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
	actx, err := activationFromQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := a.st.KnowledgeForActivatedContext(axesFromQuery(r), actx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	openRequests, err := a.st.CountOpenRequests(axesFromQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	fmt.Fprint(w, renderBootstrap(entries, intParam(r, "budget", defaultBudget)))
	if openRequests > 0 {
		plural := "request"
		if openRequests != 1 {
			plural = "requests"
		}
		fmt.Fprintf(w, "\n## Work ledger (ghosttree)\n\n%d open %s in this scope. For substantial feature, architecture, migration, or multi-session work, search the request ledger first; continue a match or create one with explicit acceptance criteria. Trivial local fixes and routine maintenance do not require a request.\n", openRequests, plural)
	}
}

// renderBootstrap builds the auto-injected context package. Binding
// instructions are always complete and first; other confirmed knowledge comes
// before unconfirmed knowledge so a tight budget cuts uncertain material first.
func renderBootstrap(entries []store.Knowledge, budget int) string {
	if budget <= 0 {
		budget = defaultBudget
	}
	if len(entries) == 0 {
		return ""
	}
	var instructions, confirmed, staged []store.Knowledge
	for _, k := range entries {
		if k.Type == "instruction" {
			instructions = append(instructions, k)
		} else if k.Confidence == "staged" {
			staged = append(staged, k)
		} else {
			confirmed = append(confirmed, k)
		}
	}
	var b strings.Builder
	b.WriteString("## Known context (ghosttree)\n")
	if len(instructions) > 0 {
		b.WriteString("\n### Instructions (binding)\n")
		for _, k := range instructions {
			mark := ""
			if k.Confidence == "staged" || k.Confidence == "quarantined" {
				mark = " [unconfirmed]"
			}
			label := scopeLabel(k.Scope)
			if gate := activationLabel(k.Activation); gate != "" {
				label += " | " + gate
			}
			fmt.Fprintf(&b, "- [%s]%s %s — %s\n", label, mark, k.Title, oneLine(k.Body))
		}
	}
	// Instructions do not compete for the context budget. Preserve the same
	// allowance for all remaining groups regardless of instruction length.
	contentLimit := budget + b.Len()
	truncated := writeGroups(&b, confirmed, contentLimit, "")
	if len(staged) > 0 && !truncated {
		truncated = writeGroups(&b, staged, contentLimit,
			"\n## Unconfirmed (distilled, not yet approved — verify before relying on it)\n")
	}
	if truncated {
		b.WriteString("…(truncated, use context_search for more)\n")
	}
	return b.String()
}

func activationFromQuery(r *http.Request) (activation.Context, error) {
	return activation.NormalizeContext(activation.Context{
		RepoPath: r.URL.Query().Get("repo_path"),
		Paths:    r.URL.Query()["path"],
		Task:     r.URL.Query().Get("task"),
	})
}

func activationLabel(r activation.Rule) string {
	var parts []string
	if len(r.Paths) > 0 {
		parts = append(parts, "paths:"+strings.Join(r.Paths, ","))
	}
	if len(r.Tasks) > 0 {
		parts = append(parts, "tasks:"+strings.Join(r.Tasks, ","))
	}
	return strings.Join(parts, " | ")
}

// writeGroups appends entries grouped by type and reports whether the budget
// ran out. header is written lazily, so an empty group prints nothing.
func writeGroups(b *strings.Builder, entries []store.Knowledge, budget int, header string) bool {
	if len(entries) == 0 {
		return false
	}
	byType := map[string][]store.Knowledge{}
	for _, k := range entries {
		byType[k.Type] = append(byType[k.Type], k)
	}
	wroteHeader := header == ""
	for _, t := range []string{"decision", "pitfall", "note", "plan"} {
		group := byType[t]
		if len(group) == 0 {
			continue
		}
		for i, k := range group {
			line := fmt.Sprintf("- [%s] %s — %s\n", scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), 200))
			if i == 0 {
				line = "\n### " + t + "\n" + line
			}
			if !wroteHeader {
				line = header + line
			}
			if b.Len()+len(line) > budget {
				return true
			}
			b.WriteString(line)
			wroteHeader = true
		}
	}
	return false
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
