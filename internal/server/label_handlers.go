package server

import (
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Labels, Themen, gespeicherte Suchen (REQ-600) und Ideen (REQ-606). Jede Route
// fragt ProjectAccess; wer das Objekt, das Label oder das Projekt nicht sehen
// darf, bekommt dieselbe Antwort wie bei einer unbekannten Nummer.
var labelRoutes = map[string]routeClass{
	"GET /api/labels":                 classProject,
	"POST /api/labels":                classProject,
	"PATCH /api/labels/{id}":          classProject,
	"DELETE /api/labels/{id}":         classProject,
	"GET /api/label-assignments":      classProject,
	"POST /api/label-assignments":     classProject,
	"GET /api/topics":                 classProject,
	"POST /api/topics":                classProject,
	"DELETE /api/topics":              classProject,
	"GET /api/search-presets":         classProject,
	"POST /api/search-presets":        classProject,
	"DELETE /api/search-presets/{id}": classProject,
	"GET /api/search-presets/run":     classProject,
	"POST /api/ideas":                 classProject,
	"GET /api/ideas":                  classProject,
	"GET /api/ideas/{id}":             classProject,
	"POST /api/ideas/{id}/state":      classProject,
	"POST /api/ideas/{id}/requests":   classProject,
}

func init() {
	for pattern, class := range labelRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerLabelRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/labels", a.listLabels)
	a.routeFunc(mux, "POST /api/labels", a.createLabel)
	a.routeFunc(mux, "PATCH /api/labels/{id}", a.updateLabel)
	a.routeFunc(mux, "DELETE /api/labels/{id}", a.deleteLabel)
	a.routeFunc(mux, "GET /api/label-assignments", a.getLabelAssignments)
	a.routeFunc(mux, "POST /api/label-assignments", a.setLabelAssignments)
	a.routeFunc(mux, "GET /api/topics", a.listTopics)
	a.routeFunc(mux, "POST /api/topics", a.saveTopic)
	a.routeFunc(mux, "DELETE /api/topics", a.deleteTopic)
	a.routeFunc(mux, "GET /api/search-presets", a.listPresets)
	a.routeFunc(mux, "POST /api/search-presets", a.savePreset)
	a.routeFunc(mux, "DELETE /api/search-presets/{id}", a.deletePreset)
	a.routeFunc(mux, "GET /api/search-presets/run", a.runPreset)
	a.routeFunc(mux, "POST /api/ideas", a.createIdea)
	a.routeFunc(mux, "GET /api/ideas", a.searchIdeas)
	a.routeFunc(mux, "GET /api/ideas/{id}", a.getIdea)
	a.routeFunc(mux, "POST /api/ideas/{id}/state", a.setIdeaState)
	a.routeFunc(mux, "POST /api/ideas/{id}/requests", a.linkIdeaRequest)
}

// labelKind nimmt die Namen aus der Oberfläche an: Issue heißt Request.
func labelKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "issue" {
		return "request"
	}
	return k
}

func projectFromBody(w http.ResponseWriter, s string) (string, bool) {
	project := scope.CanonicalAxes(scope.Axes{Project: s}).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return "", false
	}
	return project, true
}

func (a *api) listLabels(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	if org := strings.TrimSpace(r.URL.Query().Get("org")); org != "" {
		o, err := a.st.OrgByRef(org)
		if err != nil {
			pa.Filtered()
			writeCoded(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		list, err := pa.ListOrgLabels(o.ID)
		if err != nil {
			if !denyAccess(w, err) {
				writeStoreError(w, http.StatusInternalServerError, err)
			}
			return
		}
		writeJSON(w, http.StatusOK, nonNil(list))
		return
	}
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project or an org")
		return
	}
	list, err := pa.ListLabels(project)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func nonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func (a *api) createLabel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project     string `json:"project"`
		Org         string `json:"org"`
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	pa := a.access(r)
	in := store.LabelInput{Name: body.Name, Color: body.Color, Description: body.Description}
	if strings.TrimSpace(body.Org) != "" {
		o, err := a.st.OrgByRef(body.Org)
		if err != nil {
			pa.Filtered()
			writeCoded(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		in.OrgID = o.ID
	} else {
		project, ok := projectFromBody(w, body.Project)
		if !ok || !a.gateProject(w, r, project) {
			return
		}
		in.Project = project
	}
	l, err := pa.CreateLabel(in)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (a *api) updateLabel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad label id")
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Color       *string `json:"color"`
		Description *string `json:"description"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	l, err := a.access(r).UpdateLabel(id, store.LabelPatch{Name: body.Name, Color: body.Color, Description: body.Description})
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *api) deleteLabel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad label id")
		return
	}
	if err := a.access(r).DeleteLabel(id); err != nil {
		writePainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) getLabelAssignments(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad object id")
		return
	}
	labels, err := a.access(r).LabelsOf(labelKind(r.URL.Query().Get("kind")), id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(labels))
}

func (a *api) setLabelAssignments(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string   `json:"kind"`
		ID     int64    `json:"id"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Add) == 0 && len(body.Remove) == 0 {
		writeCoded(w, http.StatusBadRequest, "nothing_to_do", "name labels to add or remove")
		return
	}
	labels, err := a.access(r).SetLabels(labelKind(body.Kind), body.ID, body.Add, body.Remove)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(labels))
}

// ---- Themen

func (a *api) listTopics(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	list, err := a.access(r).ListTopics(project)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) saveTopic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project     string   `json:"project"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Prefixes    []string `json:"prefixes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok || !a.gateProject(w, r, project) {
		return
	}
	t, err := a.access(r).SaveTopic(store.TopicInput{Project: project, Name: body.Name, Description: body.Description, Prefixes: body.Prefixes})
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) deleteTopic(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	ok, err := a.access(r).DeleteTopic(project, r.URL.Query().Get("name"))
	if err != nil {
		writePainError(w, err)
		return
	}
	if !ok {
		writeCoded(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Gespeicherte Suchen

func (a *api) listPresets(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	saved, err := a.access(r).ListPresets(project)
	if err != nil {
		writePainError(w, err)
		return
	}
	out := []store.SearchPreset{}
	for _, b := range query.Builtin {
		out = append(out, store.SearchPreset{Name: b.Name, Description: b.Description, Queries: b.Queries, Args: b.Args, Builtin: true, Scope: "builtin"})
	}
	writeJSON(w, http.StatusOK, append(out, saved...))
}

func (a *api) savePreset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project     string   `json:"project"`
		Scope       string   `json:"scope"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Queries     []string `json:"queries"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok || !a.gateProject(w, r, project) {
		return
	}
	if body.Scope == "" {
		body.Scope = "user"
	}
	p, err := a.access(r).SavePreset(project, body.Scope, body.Name, body.Description, body.Queries)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *api) deletePreset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad search id")
		return
	}
	if err := a.access(r).DeletePreset(id); err != nil {
		writePainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runPreset führt eine gespeicherte oder mitgelieferte Suche mit Argumenten aus.
// Die Zeilen laufen nacheinander durch dieselbe Suche wie /api/search; die
// Treffer werden zusammengelegt. Die Namen der mitgelieferten Suchen sind für
// gespeicherte gesperrt; persönlich geht vor Projekt vor Org.
func (a *api) runPreset(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	pa := a.access(r)
	saved, err := pa.ListPresets(project)
	if err != nil {
		writePainError(w, err)
		return
	}
	name := strings.ToLower(strings.TrimSpace(q.Get("name")))
	var preset query.Preset
	found := false
	for _, s := range saved {
		if strings.EqualFold(s.Name, name) {
			preset, found = query.Preset{Name: s.Name, Description: s.Description, Queries: s.Queries, Args: s.Args}, true
			break
		}
	}
	if !found {
		preset, found = query.BuiltinByName(name)
	}
	if !found {
		writeCoded(w, http.StatusNotFound, "preset_not_found", "no saved search "+strconv.Quote(name))
		return
	}
	args := map[string]string{}
	for _, ph := range query.Placeholders {
		if v := q.Get(ph); v != "" {
			args[ph] = v
		}
	}
	lines, err := query.Expand(preset.Queries, args)
	if err != nil {
		writeCoded(w, http.StatusBadRequest, "preset_args", err.Error())
		return
	}
	limit := intParam(r, "limit", 20)
	merged := searchResult{Knowledge: nonNil[store.Knowledge](nil), Sessions: nonNil[store.SessionHit](nil)}
	merged.Requests = nonNil(merged.Requests)
	for _, line := range lines {
		parsed, err := query.Parse(line, time.Now())
		if err != nil {
			writeCoded(w, http.StatusBadRequest, "invalid_query", err.Error())
			return
		}
		res, ok := a.qualifiedResult(w, r, parsed, "all", scope.Axes{Project: project}, limit, store.AnchorFilter{}, originctx.Filter{})
		if !ok {
			return
		}
		mergeResults(&merged, res)
	}
	merged.Query = strings.Join(lines, " | ")
	writeJSON(w, http.StatusOK, map[string]any{"preset": preset, "queries": lines, "result": merged})
}

// ---- Ideen

func (a *api) ideaActor(r *http.Request, sessionID int64) idea.Actor {
	sid := a.callerSession(r, sessionID)
	ext := a.st.SessionExternalByID(sid)
	return idea.Actor{AccountID: authorIDOf(r), Person: personOf(r), SessionID: sid, SessionRef: ext,
		GitAnchor: anchorFromRequest(r), Origin: a.originFor(r, ext)}
}

func writeIdeaError(w http.ResponseWriter, err error) {
	writePainError(w, err)
}

func (a *api) createIdea(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project        string `json:"project"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		AllowDuplicate bool   `json:"allow_duplicate"`
		SessionID      int64  `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok || !a.gateProject(w, r, project) {
		return
	}
	pa := a.access(r)
	i, err := pa.CreateIdea(store.CreateIdeaInput{Project: project, Title: body.Title, Body: body.Body,
		AllowDuplicate: body.AllowDuplicate, Actor: a.ideaActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	a.stampActor(r, actor.Idea, i.ID, body.SessionID)
	// Die Idee ist abgelegt, ohne Rückfrage. Höchstens einmal je Idee kommt ein
	// Vorschlag, sie auszuarbeiten; er verlangt nichts (REQ-611).
	i.Hint = pa.ElaborationHint(i)
	writeJSON(w, http.StatusCreated, i)
}

func (a *api) searchIdeas(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGateEntries(w, r, project, store.ResIdea) {
		return
	}
	list, err := a.access(r).SearchIdeas(idea.Filter{Project: project, State: q.Get("state"), Query: q.Get("q"), Limit: intParam(r, "limit", 50)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) getIdea(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad idea id")
		return
	}
	i, err := a.access(r).IdeaView(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (a *api) setIdeaState(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad idea id")
		return
	}
	var body struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	i, err := a.access(r).SetIdeaState(id, body.State, body.Reason)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

// linkIdeaRequest verknüpft eine Idee mit einem Request, der aus ihr entstand.
// Der Request muss für den Aufrufer lesbar und bearbeitbar sein (wie bei
// request_start_work); alles andere antwortet wie eine unbekannte Nummer.
func (a *api) linkIdeaRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad idea id")
		return
	}
	var body struct {
		RequestID int64 `json:"request_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	pa := a.access(r)
	// Die Idee zuerst: wer sie nicht sehen darf, erfährt auch nichts über den Request.
	if _, err := pa.IdeaView(id); err != nil {
		writeIdeaError(w, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, body.RequestID, store.ActWork) {
		return
	}
	i, err := pa.LinkIdeaRequest(id, body.RequestID, personOf(r))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

// mergeResults legt die Treffer einer Zeile zu den bisherigen, ohne Doppelte.
func mergeResults(dst *searchResult, src searchResult) {
	addUnique := func(have map[int64]bool, id int64) bool {
		if have[id] {
			return false
		}
		have[id] = true
		return true
	}
	seen := func(ids []int64) map[int64]bool {
		m := map[int64]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	k := seen(idsOf(dst.Knowledge, func(x store.Knowledge) int64 { return x.ID }))
	for _, x := range src.Knowledge {
		if addUnique(k, x.ID) {
			dst.Knowledge = append(dst.Knowledge, x)
		}
	}
	rq := seen(idsOf(dst.Requests, func(x requestdomain.SearchHit) int64 { return x.Request.ID }))
	for _, x := range src.Requests {
		if addUnique(rq, x.Request.ID) {
			dst.Requests = append(dst.Requests, x)
		}
	}
	pn := seen(idsOf(dst.Pains, func(x pain.Pain) int64 { return x.ID }))
	for _, x := range src.Pains {
		if addUnique(pn, x.ID) {
			dst.Pains = append(dst.Pains, x)
		}
	}
	id := seen(idsOf(dst.Ideas, func(x idea.Idea) int64 { return x.ID }))
	for _, x := range src.Ideas {
		if addUnique(id, x.ID) {
			dst.Ideas = append(dst.Ideas, x)
		}
	}
	cr := seen(idsOf(dst.Criteria, func(x store.CriterionHit) int64 { return x.ID }))
	for _, x := range src.Criteria {
		if addUnique(cr, x.ID) {
			dst.Criteria = append(dst.Criteria, x)
		}
	}
	fl := seen(idsOf(dst.Files, func(x store.GhostFile) int64 { return x.ID }))
	for _, x := range src.Files {
		if addUnique(fl, x.ID) {
			dst.Files = append(dst.Files, x)
		}
	}
	tk := seen(idsOf(dst.Tasks, func(x humantask.Task) int64 { return x.ID }))
	for _, x := range src.Tasks {
		if addUnique(tk, x.ID) {
			dst.Tasks = append(dst.Tasks, x)
		}
	}
}
