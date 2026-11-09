package server

import (
	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Features (REQ-612): lebende Beschreibung dessen, was das Produkt tut, mit
// Fassungen. Jede Route läuft durch ProjectAccess: wer das Feature nicht lesen
// darf, bekommt dieselbe Antwort wie bei einer Nummer, die es nicht gibt. Ob ein
// Agent schreibt, entscheidet das Token (TokenKind), nicht ein Feld im Körper.
var featureRoutes = map[string]routeClass{
	"POST /api/features":                    classProject,
	"GET /api/features":                     classProject,
	"GET /api/features/checklist":           classProject,
	"GET /api/features/context":             classProject,
	"GET /api/features/export":              classProject,
	"POST /api/features/pr-paths":           classProject,
	"GET /api/features/{id}":                classProject,
	"GET /api/features/{id}/versions":       classProject,
	"GET /api/features/{id}/version":        classProject,
	"POST /api/features/{id}/versions":      classProject,
	"POST /api/features/{id}/state":         classProject,
	"POST /api/features/{id}/links":         classProject,
	"DELETE /api/features/{id}/links":       classProject,
	"POST /api/features/{id}/checks":        classProject,
	"POST /api/ideas/{id}/features":         classProject,
	"DELETE /api/ideas/{id}/features/{fid}": classProject,
}

func init() {
	for pattern, class := range featureRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerFeatureRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/features", a.createFeature)
	a.routeFunc(mux, "GET /api/features", a.searchFeatures)
	a.routeFunc(mux, "GET /api/features/checklist", a.featureChecklist)
	a.routeFunc(mux, "GET /api/features/context", a.featureContext)
	a.routeFunc(mux, "GET /api/features/export", a.exportFeatures)
	a.routeFunc(mux, "POST /api/features/pr-paths", a.reportPullPaths)
	a.routeFunc(mux, "GET /api/features/{id}", a.getFeature)
	a.routeFunc(mux, "GET /api/features/{id}/versions", a.featureVersions)
	a.routeFunc(mux, "GET /api/features/{id}/version", a.featureVersion)
	a.routeFunc(mux, "POST /api/features/{id}/versions", a.updateFeature)
	a.routeFunc(mux, "POST /api/features/{id}/state", a.setFeatureState)
	a.routeFunc(mux, "POST /api/features/{id}/links", a.linkFeature)
	a.routeFunc(mux, "DELETE /api/features/{id}/links", a.unlinkFeature)
	a.routeFunc(mux, "POST /api/features/{id}/checks", a.confirmFeature)
	a.routeFunc(mux, "POST /api/ideas/{id}/features", a.linkIdeaFeature)
	a.routeFunc(mux, "DELETE /api/ideas/{id}/features/{fid}", a.unlinkIdeaFeature)
}

// featureActor ist der Schreiber: Anker und Entstehungskontext aus der Anfrage,
// Mensch nur aus einer Websitzung.
func (a *api) featureActor(r *http.Request, sessionID int64) feature.Actor {
	act := store.FeatureActorOf(a.ideaActor(r, sessionID))
	act.Agent = principalOf(r).TokenKind != store.WebSessionKind
	return act
}

func featureIDFrom(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad feature id")
	}
	return id, ok
}

func (a *api) createFeature(w http.ResponseWriter, r *http.Request) {
	var body struct {
		feature.Patch
		Project   string  `json:"project"`
		Message   string  `json:"message"`
		Ideas     []int64 `json:"ideas"`
		Requests  []int64 `json:"requests"`
		FromIdea  int64   `json:"from_idea"`
		PR        int64   `json:"pr"`
		SessionID int64   `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok || !a.gateProject(w, r, project) {
		return
	}
	f, err := a.access(r).CreateFeature(store.CreateFeatureInput{Project: project, Content: body.Patch, Message: body.Message,
		Ideas: body.Ideas, Requests: body.Requests, FromIdea: body.FromIdea, PR: body.PR, Actor: a.featureActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	a.stampActor(r, actor.Feature, f.ID, body.SessionID)
	writeJSON(w, http.StatusCreated, f)
}

func (a *api) searchFeatures(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGateEntries(w, r, project, store.ResFeature) {
		return
	}
	list, err := a.access(r).SearchFeatures(feature.Filter{Project: project, State: q.Get("state"), Query: q.Get("q"), Path: q.Get("path"), Limit: intParam(r, "limit", 100)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) getFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	f, err := a.access(r).FeatureView(id)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) featureVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	list, err := a.access(r).FeatureVersions(id, r.URL.Query().Get("full") == "1")
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) featureVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	v, err := a.access(r).FeatureVersion(id, intParam(r, "revision", 0))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *api) updateFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		feature.Patch
		Message   string  `json:"message"`
		Base      int     `json:"base"`
		Idea      int64   `json:"idea"`
		Requests  []int64 `json:"requests"`
		PR        int64   `json:"pr"`
		SessionID int64   `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	v, unchanged, err := a.access(r).UpdateFeature(id, store.UpdateFeatureInput{Patch: body.Patch, Message: body.Message, Base: body.Base,
		Idea: body.Idea, Requests: body.Requests, PR: body.PR, Actor: a.featureActor(r, body.SessionID)})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "unchanged": unchanged})
}

func (a *api) setFeatureState(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
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
	f, err := a.access(r).SetFeatureState(id, body.State, body.Reason)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) linkFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind string `json:"kind"`
		Ref  int64  `json:"ref"`
		Rel  string `json:"rel"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if body.Rel == "" {
		body.Rel = feature.RelOrigin
	}
	f, err := a.access(r).LinkFeature(id, body.Kind, body.Ref, body.Rel, personOf(r))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) unlinkFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ref, _ := strconv.ParseInt(q.Get("ref"), 10, 64)
	rel := q.Get("rel")
	if rel == "" {
		rel = feature.RelOrigin
	}
	f, err := a.access(r).UnlinkFeature(id, q.Get("kind"), ref, rel)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) confirmFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := featureIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		PR   int64  `json:"pr"`
		Note string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if err := a.access(r).ConfirmFeatureUnchanged(id, body.PR, body.Note, personOf(r)); err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature": id, "pr": body.PR, "state": "unchanged"})
}

func (a *api) reportPullPaths(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string   `json:"project"`
		PR      int64    `json:"pr"`
		Paths   []string `json:"paths"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := projectFromBody(w, body.Project)
	if !ok {
		return
	}
	pa := a.access(r)
	stored, dropped, err := pa.ReportPullPaths(project, body.PR, body.Paths, personOf(r))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	checks, err := pa.FeatureChecklist(project, body.PR)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stored": stored, "dropped": dropped, "checklist": checks})
}

func (a *api) featureChecklist(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	checks, err := a.access(r).FeatureChecklist(project, int64(intParam(r, "pr", 0)))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checks)
}

// featureContext ist der Weg des Hooks und von context_get: die Features eines
// Bereichs in Kurzform. Ohne Leserecht kommt eine leere Antwort, der Hook darf an
// einem Projekt, das er nicht lesen darf, nicht scheitern.
func (a *api) featureContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	ctx, err := a.access(r).FeaturesForPaths(project, q["path"], q.Get("session"), intParam(r, "budget", 0))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ctx)
}

// exportFeatures liefert die aktiven Features als Seite (docs/features.md).
func (a *api) exportFeatures(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGateEntries(w, r, project, store.ResFeature) {
		return
	}
	list, err := a.access(r).SearchFeatures(feature.Filter{Project: project, Limit: 500})
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(feature.RenderDoc("", list)))
}

func (a *api) linkIdeaFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	var body struct {
		Feature int64 `json:"feature"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	i, err := a.access(r).LinkIdeaFeature(id, body.Feature, personOf(r))
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}

func (a *api) unlinkIdeaFeature(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ideaIDFrom(w, r)
	if !ok {
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad feature id")
		return
	}
	i, err := a.access(r).UnlinkIdeaFeature(id, fid)
	if err != nil {
		writeIdeaError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, i)
}
