package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The gardener's schedule (REQ-624). The server keeps the settings; the
// sandboxed gardener unit asks what is due, runs, and reports each run back.
// Only the gardener's account may call these routes; the store checks it.

func writeGardenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotGardener):
		writeCoded(w, http.StatusForbidden, "not_gardener", err.Error())
	case errors.Is(err, store.ErrProjectNotFound):
		writeCoded(w, http.StatusNotFound, "project_not_found", err.Error())
	case errors.Is(err, store.ErrGardenSettings):
		writeCoded(w, http.StatusConflict, "gardener_off", err.Error())
	case errors.Is(err, store.ErrGardenAutoGone):
		writeCoded(w, http.StatusNotFound, "run_not_found", err.Error())
	default:
		writeStoreError(w, http.StatusInternalServerError, err)
	}
}

// gardenDue answers GET /api/garden/due: the namespaces whose gardener is due
// now, with repositories and budgets.
func (a *api) gardenDue(w http.ResponseWriter, r *http.Request) {
	id, ok := accountOf(principalOf(r))
	if !ok || id != a.st.GardenerAccountID() || id == 0 {
		writeGardenError(w, store.ErrNotGardener)
		return
	}
	jobs, err := a.st.GardenDue(time.Now())
	if err != nil {
		writeGardenError(w, err)
		return
	}
	if jobs == nil {
		jobs = []store.GardenJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// gardenRunStart answers POST /api/garden/runs {org_id, project}.
func (a *api) gardenRunStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID   int64  `json:"org_id"`
		Project string `json:"project"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	id, err := a.st.StartGardenRun(principalOf(r).ID, body.OrgID, body.Project)
	if err != nil {
		writeGardenError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// gardenRunFinish answers POST /api/garden/runs/{id}/finish {state, cost_usd, note}.
func (a *api) gardenRunFinish(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeCoded(w, http.StatusBadRequest, "invalid_input", "run id")
		return
	}
	var body struct {
		State   string  `json:"state"`
		CostUSD float64 `json:"cost_usd"`
		Note    string  `json:"note"`
	}
	if !readOrgJSON(w, r, &body) {
		return
	}
	if err := a.st.FinishGardenRun(principalOf(r).ID, id, body.State, body.CostUSD, body.Note); err != nil {
		writeGardenError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
