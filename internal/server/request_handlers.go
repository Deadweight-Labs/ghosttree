package server

import (
	"database/sql"
	"errors"
	"net/http"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func (a *api) createRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type           string   `json:"type"`
		Title          string   `json:"title"`
		Description    string   `json:"description"`
		Priority       string   `json:"priority"`
		Project        string   `json:"project"`
		Branch         string   `json:"branch"`
		Machine        string   `json:"machine"`
		Origin         string   `json:"origin"`
		SessionRef     string   `json:"session_ref"`
		IdempotencyKey string   `json:"idempotency_key"`
		Criteria       []string `json:"criteria"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := a.st.CreateRequest(requestdomain.CreateInput{
		Request: requestdomain.Request{
			Type: body.Type, Title: body.Title, Description: body.Description, Priority: body.Priority,
			Scope:  scope.Axes{Project: body.Project, Branch: body.Branch, Machine: body.Machine},
			Origin: body.Origin, Person: personOf(r), SessionRef: body.SessionRef,
		},
		Criteria: body.Criteria, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (a *api) searchRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := a.st.SearchRequests(requestdomain.SearchFilter{
		Scope: axesFromQuery(r), Query: q.Get("q"), State: q.Get("state"),
		Type: q.Get("type"), Cursor: q.Get("cursor"), Limit: intParam(r, "limit", 10),
	})
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *api) getRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	detail, err := a.st.RequestByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) completeRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var body struct {
		EvidenceKind string `json:"evidence_kind"`
		EvidenceRef  string `json:"evidence_ref"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err := a.st.CompleteRequest(id, requestdomain.Evidence{Kind: body.EvidenceKind, Ref: body.EvidenceRef, Person: personOf(r)})
	if err != nil {
		writeRequestError(w, err)
		return
	}
	detail, err := a.st.RequestByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) startRequestWork(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var body struct {
		SessionID int64  `json:"session_id"`
		Role      string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	work, warnings, err := a.st.StartRequestWork(requestID, body.SessionID, body.Role, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"work": work, "warnings": warnings})
}

func (a *api) finishRequestWork(w http.ResponseWriter, r *http.Request) {
	workID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad work id")
		return
	}
	var body struct {
		State   string `json:"state"`
		Summary string `json:"summary"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	work, err := a.st.FinishRequestWork(workID, body.State, body.Summary, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, work)
}

func (a *api) addRequestCriterion(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var body struct {
		Description string `json:"description"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	criterion, err := a.st.AddCriterion(requestID, body.Description, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, criterion)
}

func (a *api) setRequestCriterion(w http.ResponseWriter, r *http.Request) {
	criterionID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad criterion id")
		return
	}
	var body struct {
		State        string `json:"state"`
		EvidenceKind string `json:"evidence_kind"`
		EvidenceRef  string `json:"evidence_ref"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.SetCriterionState(criterionID, body.State, requestdomain.Evidence{Kind: body.EvidenceKind, Ref: body.EvidenceRef, Person: personOf(r)}); err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"criterion_id": criterionID, "state": body.State})
}

func (a *api) dropRequest(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.st.DropRequest(requestID, body.Reason, personOf(r)); err != nil {
		writeRequestError(w, err)
		return
	}
	detail, err := a.st.RequestByID(requestID)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *api) addRequestRelation(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var relation requestdomain.Relation
	if err := readJSON(r, &relation); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := a.st.AddRequestRelation(requestID, relation, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func writeRequestError(w http.ResponseWriter, err error) {
	var rule *requestdomain.RuleError
	if errors.As(err, &rule) {
		status := http.StatusBadRequest
		if rule.ErrorCode == "open_criteria" || rule.ErrorCode == "primary_exists" || rule.ErrorCode == "work_not_active" {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{
			"code": rule.ErrorCode, "message": rule.Message, "resolution": rule.Resolution,
			"details": map[string]any{"ids": rule.IDs},
		})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": "request resource not found", "resolution": "check the identifier"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "internal", "message": "request operation failed", "resolution": "retry or inspect server logs"})
}
