package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.gateMachine(w, r, scope.CanonicalAxes(scope.Axes{Machine: body.Machine}).Machine, false) ||
		!a.gateProject(w, r, scope.CanonicalAxes(scope.Axes{Project: body.Project}).Project) {
		return
	}
	if denyAccess(w, a.access(r).Check(scope.CanonicalAxes(scope.Axes{Project: body.Project}).Project, store.ResRequest, store.ActCreate, store.Object{Own: true})) {
		return
	}
	detail, err := a.st.CreateRequest(requestdomain.CreateInput{
		Request: requestdomain.Request{
			Type: body.Type, Title: body.Title, Description: body.Description, Priority: body.Priority,
			Scope:  scope.CanonicalAxes(scope.Axes{Project: body.Project, Branch: body.Branch, Machine: body.Machine}),
			Origin: body.Origin, Person: personOf(r), SessionRef: body.SessionRef,
		},
		Criteria: body.Criteria, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.access(r).RequestDetailView(detail))
}

func (a *api) searchRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !a.listGate(w, r, axesFromQuery(r).Project, store.ResRequest) {
		return
	}
	page, err := a.st.SearchRequests(a.requestFilter(r, requestdomain.SearchFilter{
		Scope: axesFromQuery(r), Query: q.Get("q"), State: q.Get("state"),
		Type: q.Get("type"), Cursor: q.Get("cursor"), Limit: intParam(r, "limit", 10),
		// Nur für Aufrufer, die den ganzen Text zeigen — der Dateispiegel. Eine
		// Trefferliste bleibt eine Trefferliste.
		FullDescription: q.Get("full") == "1",
	}))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	a.noteRequestHits(r, page.Results)
	pa := a.access(r)
	for i := range page.Results {
		page.Results[i] = pa.RequestHitView(page.Results[i])
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *api) getRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActRead) {
		return
	}
	detail, err := a.st.RequestByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).RequestDetailView(detail))
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActEdit) {
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
	writeJSON(w, http.StatusOK, a.access(r).RequestDetailView(detail))
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, requestID, store.ActWork) {
		return
	}
	pa := a.access(r)
	ref, refErr := a.st.RequestRef(store.RefRequest, requestID)
	sees := refErr == nil && pa.SeesSessionNumbers(ref.Project)
	// Mitglieder mit Nummernsicht und Instanz-Admins dürfen jede lesbare Session
	// anhängen; wer die Nummern nicht kennen darf, nur die eigene. Eine fremde,
	// verborgene oder fehlende Nummer antwortet gleich (#2447).
	if sess, err := a.st.SessionByID(body.SessionID); err != nil || !pa.CanSeeTranscript(sess) || (!sees && !pa.IsAdmin() && !pa.OwnsSession(sess)) {
		pa.Filtered()
		writeRequestError(w, sql.ErrNoRows)
		return
	}
	work, warnings, err := a.st.StartRequestWork(requestID, body.SessionID, body.Role, personOf(r))
	if err != nil {
		writeRequestError(w, a.hideRuleRequest(r, err))
		return
	}
	if !sees && len(warnings) > 0 {
		warnings = a.readableStartWarnings(r, requestID, work.ID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"work": a.workView(r, work), "warnings": warnings})
}

// hideRuleRequest ersetzt in primary_exists die REQ-Nummer, wenn der Betrachter
// diesen Auftrag nicht lesen darf (er könnte in einem unsichtbaren Projekt
// liegen).
func (a *api) hideRuleRequest(r *http.Request, err error) error {
	var rule *requestdomain.RuleError
	if !errors.As(err, &rule) || rule.ErrorCode != "primary_exists" {
		return err
	}
	if len(rule.IDs) == 1 {
		if n, perr := strconv.ParseInt(strings.TrimPrefix(rule.IDs[0], "REQ-"), 10, 64); perr == nil {
			pa := a.access(r)
			if ref, rerr := a.st.RequestRef(store.RefRequest, n); rerr == nil &&
				pa.Check(ref.Project, store.ResRequest, store.ActRead, store.Object{Own: pa.IsAuthor(ref.Person)}) == nil {
				return err
			}
		}
	}
	return requestdomain.NewRuleError("primary_exists", "session is already working on another request", "finish or abandon that work before starting another", nil)
}

// readableStartWarnings zählt nur andere aktive Hauptarbeit, deren Session der
// Betrachter lesen darf; verborgene Arbeit ginge sonst als Zahl mit ein.
func (a *api) readableStartWarnings(r *http.Request, requestID, ownWork int64) []string {
	detail, err := a.st.RequestByID(requestID)
	if err != nil {
		return nil
	}
	pa := a.access(r)
	n := 0
	for _, w := range detail.Work {
		if w.ID == ownWork || w.Role != "primary" || w.State != "active" {
			continue
		}
		if sess, err := a.st.SessionByID(w.SessionID); err == nil && pa.CanSeeTranscript(sess) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return []string{fmt.Sprintf("REQ-%d already has %d active primary session(s)", requestID, n)}
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkWorkAccess(w, r, workID) {
		return
	}
	work, err := a.st.FinishRequestWork(workID, body.State, body.Summary, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.workView(r, work))
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, requestID, store.ActEdit) {
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefCriterion, criterionID, store.ActWork) {
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
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, requestID, store.ActEdit) {
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
	writeJSON(w, http.StatusOK, a.access(r).RequestDetailView(detail))
}

func (a *api) addRequestRelation(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var relation requestdomain.Relation
	if err := readJSON(r, &relation); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, requestID, store.ActWork) {
		return
	}
	saved, err := a.st.AddRequestRelation(requestID, relation, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (a *api) correctRequest(w http.ResponseWriter, r *http.Request) {
	requestID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	var body struct {
		Patch  map[string]string `json:"patch"`
		Reason string            `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, requestID, store.ActEdit) {
		return
	}
	if err := a.st.UpdateRequest(requestID, body.Patch, personOf(r), body.Reason); err != nil {
		writeRequestError(w, err)
		return
	}
	detail, err := a.st.RequestByID(requestID)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).RequestDetailView(detail))
}

func (a *api) removeRequestRelation(w http.ResponseWriter, r *http.Request) {
	relationID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad relation id")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefRelation, relationID, store.ActWork) {
		return
	}
	if err := a.st.RemoveRequestRelation(relationID, personOf(r), body.Reason); err != nil {
		writeRequestError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRequestError(w http.ResponseWriter, err error) {
	if writeWriterError(w, err, false) {
		return
	}
	var rule *requestdomain.RuleError
	if errors.As(err, &rule) {
		status := http.StatusBadRequest
		if rule.ErrorCode == "open_criteria" || rule.ErrorCode == "primary_exists" || rule.ErrorCode == "work_not_active" {
			status = http.StatusConflict
		}
		recordResponseError(w, classifyRequestError(status, "", err.Error()), err.Error())
		writeJSON(w, status, map[string]any{
			"code": rule.ErrorCode, "message": rule.Message, "resolution": rule.Resolution,
			"details": map[string]any{"ids": rule.IDs},
		})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		recordResponseError(w, "not_found", err.Error())
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": "request resource not found", "resolution": "check the identifier"})
		return
	}
	recordResponseError(w, classifyRequestError(http.StatusInternalServerError, "", err.Error()), err.Error())
	writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "internal", "message": "request operation failed", "resolution": "retry or inspect server logs"})
}

// checkWorkAccess prüft das Beenden von Arbeit. Wer die Session-Nummern des
// Projekts nicht kennen darf, darf nur Arbeit beenden, an der er arbeiten darf
// und deren Session ihm gehört. Instanz-Admins sind ausgenommen und beenden
// auch hängengebliebene Arbeit anderer Konten. Jede andere Ablehnung, die
// Rollenprüfung eingeschlossen, antwortet wie für eine unbekannte Id, sonst verriete Status oder Text
// (403, 404 mit anderem Body, work_not_active), dass es die verborgene Arbeit gibt (#2485).
func (a *api) checkWorkAccess(w http.ResponseWriter, r *http.Request, workID int64) bool {
	pa := a.access(r)
	ref, err := a.st.RequestRef(store.RefWork, workID)
	if err != nil || pa.IsAdmin() || pa.SeesSessionNumbers(ref.Project) {
		return a.checkRequest(w, r, store.RefWork, workID, store.ActWork)
	}
	if pa.Decide(ref.Project, store.ResRequest, store.ActWork, store.Object{Own: pa.IsAuthor(ref.Person)}).Allowed {
		if sid, err := a.st.RequestWorkSession(workID); err == nil {
			// Nur Arbeit der eigenen Session: eine lesbare, geteilte reicht nicht.
			if sess, serr := a.st.SessionByID(sid); serr == nil && pa.OwnsSession(sess) {
				return true
			}
		}
	}
	pa.Filtered()
	writeRequestError(w, sql.ErrNoRows)
	return false
}

// workView: wer die Session-Nummern des Projekts nicht kennen darf, bekommt sie
// auch aus einem einzelnen Arbeitseintrag nicht zurück.
func (a *api) workView(r *http.Request, w requestdomain.Work) requestdomain.Work {
	detail, err := a.st.RequestByID(w.RequestID)
	if err != nil || !a.access(r).SeesSessionNumbers(detail.Request.Scope.Project) {
		w.SessionID = 0
	}
	return w
}
