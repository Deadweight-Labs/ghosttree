package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// writePainError answers an access error as 404 or 403 and everything else the
// way the request routes do.
func writePainError(w http.ResponseWriter, err error) {
	if denyAccess(w, err) {
		return
	}
	writeRequestError(w, err)
}

// callerSession keeps a session id from a body only when it is the caller's own
// readable session, so it cannot be used to name someone else's.
func (a *api) callerSession(r *http.Request, id int64) int64 {
	if id <= 0 {
		return 0
	}
	pa := a.access(r)
	if sess, err := a.st.SessionByID(id); err == nil && pa.OwnsSession(sess) {
		return id
	}
	return 0
}

func (a *api) painActor(r *http.Request, sessionID int64) pain.Actor {
	return pain.Actor{AccountID: authorIDOf(r), Person: personOf(r), SessionID: a.callerSession(r, sessionID)}
}

func (a *api) createPain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project   string        `json:"project"`
		Title     string        `json:"title"`
		Body      string        `json:"body"`
		Motivates []pain.Target `json:"motivates"`
		SessionID int64         `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project := scope.CanonicalAxes(scope.Axes{Project: body.Project}).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "a pain belongs to a project: name the project")
		return
	}
	if !a.gateProject(w, r, project) {
		return
	}
	detail, err := a.access(r).CreatePain(store.CreatePainInput{
		Project: project, Title: body.Title, Body: body.Body, Motivates: body.Motivates,
		Actor: a.painActor(r, body.SessionID), AuthorID: authorIDOf(r),
	})
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (a *api) searchPains(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGateEntries(w, r, project, store.ResPain) {
		return
	}
	request, _ := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(q.Get("request_id")), "REQ-"), 10, 64)
	list, err := a.access(r).SearchPains(pain.Filter{
		Project: project, State: q.Get("state"), Query: q.Get("q"), RequestID: request, Limit: intParam(r, "limit", 50),
	})
	if err != nil {
		writePainError(w, err)
		return
	}
	if q.Get("detail") == "1" {
		// One call with the edges and history of every hit, for the mirror, so
		// a session start does not make one request per pain.
		details := make([]pain.Detail, 0, len(list))
		for _, p := range list {
			if d, err := a.access(r).PainDetailView(p.ID); err == nil {
				details = append(details, d)
			}
		}
		writeJSON(w, http.StatusOK, details)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) getPain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad pain id")
		return
	}
	detail, err := a.access(r).PainDetailView(id)
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) setPainState(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad pain id")
		return
	}
	var body struct {
		State        string `json:"state"`
		Remaining    string `json:"remaining"`
		EvidenceKind string `json:"evidence_kind"`
		EvidenceRef  string `json:"evidence_ref"`
		Note         string `json:"note"`
		SessionID    int64  `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := a.access(r).SetPainState(id, body.State, body.Remaining,
		pain.Evidence{Kind: body.EvidenceKind, Ref: body.EvidenceRef}, body.Note, a.painActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) correctPain(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad pain id")
		return
	}
	var body struct {
		Title     *string `json:"title"`
		Body      *string `json:"body"`
		Reason    string  `json:"reason"`
		SessionID int64   `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := a.access(r).CorrectPain(id, body.Title, body.Body, body.Reason, a.painActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) addPainLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad pain id")
		return
	}
	var body struct {
		pain.Target
		SessionID int64 `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := a.access(r).AddPainLink(id, body.Target, a.painActor(r, body.SessionID))
	if err != nil {
		writePainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (a *api) removePainLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad link id")
		return
	}
	if err := a.access(r).RemovePainLink(id, r.URL.Query().Get("reason"), a.painActor(r, 0)); err != nil {
		writePainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
