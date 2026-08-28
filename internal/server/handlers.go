package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
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
	s.Scope = scope.CanonicalAxes(s.Scope)
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
	k.Scope = scope.CanonicalAxes(k.Scope)
	if req.AutoScope != nil {
		req.AutoScope.Context = scope.CanonicalAxes(req.AutoScope.Context)
	}
	if k.Type == "" || k.Title == "" {
		writeErr(w, http.StatusBadRequest, "type and title are required")
		return
	}
	if k.Scope.IsGlobal() && req.AutoScope != nil {
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
		ks, err = a.st.KnowledgeForProject(scope.NormalizeRemote(r.URL.Query().Get("project")))
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
	in.Knowledge.Scope = scope.CanonicalAxes(in.Knowledge.Scope)
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
	id, err := a.st.BeginMigration(scope.NormalizeRemote(body.Project), body.Artifacts)
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

func (a *api) insertDocumentMigration(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad migration id")
		return
	}
	var body struct {
		Source     string `json:"source"`
		Digest     string `json:"digest"`
		DocumentID int64  `json:"document_id"`
		Revision   int    `json:"revision"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Source == "" || body.Digest == "" || body.DocumentID == 0 || body.Revision < 1 {
		writeErr(w, http.StatusBadRequest, "source, digest, document_id and revision are required")
		return
	}
	if err := a.st.InsertDocumentMigration(id, body.Source, body.Digest, body.DocumentID, body.Revision); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) completedMigrationArtifacts(w http.ResponseWriter, r *http.Request) {
	out, err := a.st.CompletedMigrationArtifacts(scope.NormalizeRemote(r.URL.Query().Get("project")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) completedDocumentArtifacts(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	out, err := a.st.CompletedDocumentArtifacts(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PendingEntry is a knowledge entry plus what a human needs to judge it.
type PendingEntry struct {
	Knowledge         store.Knowledge          `json:"knowledge"`
	Evidence          []store.Evidence         `json:"evidence"`
	MigrationEvidence *store.MigrationEvidence `json:"migration_evidence,omitempty"`
	Recurrence        int                      `json:"recurrence"`
}

func (a *api) pendingKnowledge(w http.ResponseWriter, r *http.Request) {
	ks, err := a.st.PendingKnowledge(r.URL.Query().Get("project"), intParam(r, "limit", 50))
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
		proof, proofErr := a.st.MigrationEvidenceForKnowledge(k.ID)
		var migrationProof *store.MigrationEvidence
		if proofErr == nil {
			migrationProof = &proof
		} else if proofErr != sql.ErrNoRows {
			writeErr(w, http.StatusInternalServerError, proofErr.Error())
			return
		}
		out = append(out, PendingEntry{Knowledge: k, Evidence: ev, MigrationEvidence: migrationProof, Recurrence: n})
	}
	writeJSON(w, 200, out)
}

// getKnowledge answers with one entry and its untouched body. Every other read
// path abbreviates: the bootstrap folds against a budget, a search hit shows a
// snippet. Somewhere the whole thing has to come back the way it went in, or
// storing a long document is a promise the archive does not keep.
func (a *api) getKnowledge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	k, err := a.st.KnowledgeByID(id)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "no such knowledge entry")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, k)
}

func (a *api) knowledgeHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	history, err := a.st.KnowledgeHistory(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, history)
}

// setRegressionCover trägt ein, womit ein Eintrag abgesichert ist. Eigener
// Endpunkt statt eines Feldes in patchKnowledge: eine Aussage ÜBER den Text ist
// keine Korrektur des Textes und darf keine neue Fassung in der Historie
// anlegen.
func (a *api) setRegressionCover(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad knowledge id")
		return
	}
	var in struct {
		State string `json:"state"`
		Test  string `json:"test"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.SetRegressionCover(id, in.State, in.Test); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) regressionGaps(w http.ResponseWriter, r *http.Request) {
	gaps, unreviewed, err := a.st.RegressionGaps(axesFromQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Die Zahl der Unbeurteilten reist mit: eine kurze Lückenliste ohne sie
	// liest sich als Entwarnung, obwohl niemand hingesehen hat.
	writeJSON(w, http.StatusOK, map[string]any{"gaps": gaps, "unreviewed": unreviewed})
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
	// Der Token ist die einzige vertrauenswürdige Quelle für den Bestätiger;
	// ein mitgesendeter Name darf keine fremde Freigabe vortäuschen.
	delete(patch, "confirmed_by")
	if confidence, ok := patch["confidence"]; ok {
		if confidence == "verified" {
			patch["confirmed_by"] = personOf(r)
		} else {
			patch["confirmed_by"] = ""
		}
	}
	if err := a.st.UpdateKnowledgeBy(id, patch, personOf(r)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type searchResult struct {
	Knowledge []store.Knowledge         `json:"knowledge"`
	Sessions  []store.SessionHit        `json:"sessions"`
	Requests  []requestdomain.SearchHit `json:"requests"`
}

func (a *api) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	filter := axesFromQuery(r)
	limit := intParam(r, "limit", 20)
	res := searchResult{Knowledge: []store.Knowledge{}, Sessions: []store.SessionHit{}, Requests: []requestdomain.SearchHit{}}
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
		hits, err := a.st.SearchSessions(q, filter, r.URL.Query().Get("exclude_session"), limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Sessions = hits
	}
	if kind == "requests" || kind == "all" {
		page, err := a.st.SearchRequests(requestdomain.SearchFilter{Query: q, Scope: scope.Axes{Project: filter.Project}, Limit: limit})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res.Requests = page.Results
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
	fmt.Fprint(w, RenderBootstrap(entries, intParam(r, "budget", defaultBudget)))
	if openRequests > 0 {
		plural := "request"
		if openRequests != 1 {
			plural = "requests"
		}
		fmt.Fprintf(w, "\n## Work ledger (ghosttree)\n\n%d open %s in this scope. For substantial feature, architecture, migration, or multi-session work, search the request ledger first; continue a match or create one with explicit acceptance criteria. Trivial local fixes and routine maintenance do not require a request.\n", openRequests, plural)
	}
	// Der Zähler sagt, dass es etwas gibt; erst diese Zeile sagt, dass etwas
	// angefangen und liegengeblieben ist. Ein Fehler kostet nur die Auskunft:
	// der Bootstrap ist bis hierhin schon geschrieben.
	if threads, err := a.st.InterruptedWork(axesFromQuery(r),
		time.Now().UTC().Add(-interruptedWindow).Format(time.RFC3339),
		r.URL.Query().Get("session"), maxInterruptedThreads); err == nil {
		fmt.Fprint(w, renderInterrupted(threads, time.Now().UTC()))
	}
}

func (a *api) interrupted(w http.ResponseWriter, r *http.Request) {
	threads, err := a.st.InterruptedWork(axesFromQuery(r),
		time.Now().UTC().Add(-interruptedWindow).Format(time.RFC3339),
		r.URL.Query().Get("session"), maxInterruptedThreads)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

// maxRelevantEntries caps what one prompt may pull in. Three is enough to
// answer a sentence and few enough that a wrong guess stays cheap.
const maxRelevantEntries = 3

// relevant answers with knowledge the text gives a reason to deliver, or with
// nothing at all — which is the usual case, and is an empty body rather than an
// error.
func (a *api) relevant(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", maxRelevantEntries)
	if limit > maxRelevantEntries {
		limit = maxRelevantEntries
	}
	entries, err := a.st.RelevantKnowledge(r.URL.Query().Get("q"), axesFromQuery(r), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(200)
	if len(entries) == 0 {
		return
	}
	// A different heading from the bootstrap on purpose. This arrived because of
	// what was just said, and a reader who cannot tell the two apart cannot judge
	// why it is in front of them.
	fmt.Fprint(w, "## Possibly relevant to what you just said (ghosttree)\n\n")
	for _, k := range entries {
		fmt.Fprintf(w, "- [%s|%s] %s — %s\n", k.Type, scopeLabel(k.Scope), k.Title, truncate(oneLine(k.Body), 400))
	}
}

// renderBootstrap builds the auto-injected context package. Binding
// instructions are always complete and first; other confirmed knowledge comes
// before unconfirmed knowledge so a tight budget cuts uncertain material first.
func RenderBootstrap(entries []store.Knowledge, budget int) string {
	return renderBootstrap(entries, budget, false)
}

// RenderBootstrapPreview includes staged entries, clearly separated from the
// binding context. It is for operator inspection, never automatic injection.
func RenderBootstrapPreview(entries []store.Knowledge, budget int) string {
	return renderBootstrap(entries, budget, true)
}

func renderBootstrap(entries []store.Knowledge, budget int, includeStaged bool) string {
	if budget <= 0 {
		budget = defaultBudget
	}
	if len(entries) == 0 {
		return ""
	}
	var instructions, confirmed, staged []store.Knowledge
	held := map[string]int{}
	for _, k := range entries {
		if k.Confidence == "staged" {
			if includeStaged {
				staged = append(staged, k)
			}
		} else if k.Type == "instruction" {
			instructions = append(instructions, k)
		} else if pushedTypes[k.Type] {
			confirmed = append(confirmed, k)
		} else {
			held[k.Type]++
		}
	}
	var b strings.Builder
	b.WriteString("## Known context (ghosttree)\n")
	if len(instructions) > 0 {
		b.WriteString("\n### Instructions (binding)\n")
		for _, k := range instructions {
			label := scopeLabel(k.Scope)
			if gate := activationLabel(k.Activation); gate != "" {
				label += " | " + gate
			}
			fmt.Fprintf(&b, "- [%s] %s — %s\n", label, k.Title, oneLine(k.Body))
		}
	}
	// Instructions do not compete for the context budget. Preserve the same
	// allowance for all remaining groups regardless of instruction length.
	contentLimit := budget + b.Len()
	broad, projectScoped := splitByScopeBreadth(confirmed)
	// Global and machine knowledge is bounded by construction: adding a
	// repository does not add machine facts. Project knowledge is unbounded and
	// will always win a straight contest, which is how a distiller release
	// displaced the two machine notes that were the only entries with a
	// measured effect. Broad knowledge therefore goes first, and its ceiling
	// applies only while there is project knowledge to protect. Whatever the
	// reserve does not use stays available to the project.
	broadLimit := contentLimit
	if len(projectScoped) > 0 {
		broadLimit = min(b.Len()+budget/broadScopeReserveDivisor, contentLimit)
	}
	// The two passes exist for the reserve, not for the reader: one shared set
	// of headings keeps them from looking like two separate sections.
	headings := map[string]bool{}
	truncated := writeGroups(&b, broad, broadLimit, "", headings)
	truncated = writeGroups(&b, projectScoped, contentLimit, "", headings) || truncated
	if len(staged) > 0 && !truncated {
		truncated = writePreviewGroup(&b, staged, contentLimit)
	}
	if truncated {
		b.WriteString("…(truncated, use context_search for more)\n")
	}
	writeHeldIndex(&b, held)
	return b.String()
}

// writeHeldIndex names what was deliberately not sent. Withholding silently
// would not defer the knowledge, it would hide it: an agent cannot search for a
// kind of material it has no reason to think exists. A line of counts costs
// almost nothing and turns the omission into an invitation.
func writeHeldIndex(b *strings.Builder, held map[string]int) {
	if len(held) == 0 {
		return
	}
	var parts []string
	for _, t := range []string{"decision", "note", "plan"} {
		n := held[t]
		if n == 0 {
			continue
		}
		label := t
		if n != 1 {
			label += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	if len(parts) == 0 {
		return
	}
	fmt.Fprintf(b, "\nAlso in scope, not shown: %s. These answer questions you will "+
		"know you have — use context_search for them.\n", strings.Join(parts, ", "))
}

func writePreviewGroup(b *strings.Builder, entries []store.Knowledge, limit int) bool {
	b.WriteString("\n## Unconfirmed preview (not binding; approve before agent delivery)\n")
	for _, k := range entries {
		line := fmt.Sprintf("- [preview only | %s] %s — %s\n", scopeLabel(k.Scope), k.Title, oneLine(k.Body))
		if b.Len()+len(line) > limit {
			return true
		}
		b.WriteString(line)
	}
	return false
}

func activationFromQuery(r *http.Request) (activation.Context, error) {
	return activation.NormalizeContext(activation.Context{
		RepoPath: r.URL.Query().Get("repo_path"),
		Paths:    r.URL.Query()["path"],
	})
}

func activationLabel(r activation.Rule) string {
	var parts []string
	if len(r.Paths) > 0 {
		parts = append(parts, "paths:"+strings.Join(r.Paths, ","))
	}
	return strings.Join(parts, " | ")
}

// pushedTypes decides what the bootstrap carries into every session, as opposed
// to what waits to be asked for.
//
// The test is not importance, it is whether the reader could know to look. A
// pitfall fires before a mistake nobody has made yet, so it cannot be searched
// for: not knowing about it is precisely the condition it addresses.
// Instructions bind whether or not anyone reads them, so they have to arrive
// with the session too.
//
// Everything else is reference. A decision explains why something is the way it
// is, which you want when you are about to change that thing; a note records how
// things stand; a plan records where work got to. In each case the moment of
// need is recognisable from inside the work, and search reaches them.
//
// Measured on 2026-08-24, which is what settled it: the two machine-scoped
// entries in the archive had each been delivered 62 times that day and matched
// a search zero times. One of them, an inventory of the local Ollama models,
// went into every session of every project — a fact about one workstation that
// changes nothing until somebody picks a local model.
var pushedTypes = map[string]bool{"pitfall": true}

// broadScopeReserveDivisor bounds what global and machine knowledge may take of
// the content budget. A quarter is enough for the handful of facts that hold
// everywhere and small enough that a project keeps most of its own allowance.
const broadScopeReserveDivisor = 4

// splitByScopeBreadth separates knowledge that holds regardless of repository
// from knowledge about one repository.
func splitByScopeBreadth(entries []store.Knowledge) (broad, projectScoped []store.Knowledge) {
	for _, k := range entries {
		if k.Scope.Project == "" {
			broad = append(broad, k)
		} else {
			projectScoped = append(projectScoped, k)
		}
	}
	return broad, projectScoped
}

// writeGroups appends entries grouped by type and reports whether the budget
// ran out. header is written lazily, so an empty group prints nothing.
// headings is shared across calls so a type printed by one pass is not printed
// again by the next.
func writeGroups(b *strings.Builder, entries []store.Knowledge, budget int, header string, headings map[string]bool) bool {
	if len(entries) == 0 {
		return false
	}
	byType := map[string][]store.Knowledge{}
	for _, k := range entries {
		byType[k.Type] = append(byType[k.Type], k)
	}
	wroteHeader := header == ""
	// Pitfalls first: one stops a mistake that is about to be made, while a
	// decision explains one already made. Under a budget the explanation is
	// what can wait for a search.
	for _, t := range []string{"pitfall", "decision", "note", "plan"} {
		group := byType[t]
		if len(group) == 0 {
			continue
		}
		for _, k := range group {
			label := scopeLabel(k.Scope)
			if provenance := store.KnowledgeProvenance(k); provenance != "" {
				label += " | " + provenance
			}
			line := fmt.Sprintf("- [%s] %s — %s\n", label, k.Title, truncate(oneLine(k.Body), 200))
			if !headings[t] {
				line = "\n### " + t + "\n" + line
			}
			if !wroteHeader {
				line = header + line
			}
			if b.Len()+len(line) > budget {
				return true
			}
			b.WriteString(line)
			wroteHeader, headings[t] = true, true
		}
	}
	return false
}

func scopeLabel(ax scope.Axes) string { return ax.Label() }

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

func (a *api) putGhost(w http.ResponseWriter, r *http.Request) {
	var g store.GhostFile
	if err := readJSON(r, &g); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if g.Project == "" {
		writeErr(w, http.StatusBadRequest, "project is required")
		return
	}
	g.Person = personOf(r)
	id, err := a.st.PutGhostFile(g)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}

// ghostsForPath ist der Auslieferungspfad. Er hat einen Nebeneffekt — er merkt
// sich, was gesagt wurde — und ist deshalb bewusst nicht als reines GET zu
// lesen. Ein zweiter Umlauf zum Quittieren wäre sauberer und passt nicht in das
// 900-ms-Budget des Hooks.
func (a *api) ghostsForPath(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	entries, err := a.st.GhostFilesForDelivery(q.Get("project"), q.Get("path"), q.Get("session"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, entries)
}

// ghostsMove hängt eine Beschreibung samt Historie auf einen neuen Pfad. Die
// Entscheidung, DASS es ein Umzug ist, fällt auf der Client-Seite: nur dort
// liegt die Dateiliste, an der Verschiebung und Kopie zu unterscheiden sind.
func (a *api) ghostsMove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.MoveGhostFile(in.Project, in.From, in.To); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"from": in.From, "to": in.To})
}

func (a *api) ghostHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Der Hook will nur die Zahl. Den ganzen Text zu übertragen, um ihn dann
	// zu zählen, wäre auf einem Pfad mit 900-ms-Budget die falsche Rechnung.
	if q.Get("count") != "" {
		n, err := a.st.GhostHistoryCount(q.Get("project"), q.Get("path"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, 200, map[string]int{"count": n})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	read := a.st.GhostFileHistory
	// Die Kette nimmt die aktuelle Fassung als Kopf dazu. Ohne sie hat die
	// neueste abgeloeste Fassung keinen Nachfolger, und genau der Vergleich
	// mit ihm ist die Frage, die jemand an eine Historie stellt.
	if q.Get("chain") != "" {
		read = a.st.GhostFileChain
	}
	versions, err := read(q.Get("project"), q.Get("path"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, versions)
}

func (a *api) ghostTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	entries, err := a.st.GhostFilesUnder(q.Get("project"), q.Get("prefix"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, entries)
}

func (a *api) searchGhosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	entries, err := a.st.SearchGhostFiles(q.Get("q"), q.Get("project"), intParam(r, "limit", 20))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, entries)
}
