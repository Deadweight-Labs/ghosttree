package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// Workflow definitions over the API (REQ-695): the MCP tool and ctx read,
// check and save them here. Reading is for members and up, saving and
// switching on or off for leads and owners (ResWorkflow); the save goes
// through workflow.SaveDef, the same check and the same version as the web
// interface. A run's own token never changes a workflow.
var workflowRoutes = map[string]routeClass{
	"GET /api/workflows":                 classProject,
	"POST /api/workflows/check":          classProject,
	"GET /api/workflows/{name}":          classProject,
	"PUT /api/workflows/{name}":          classProject,
	"POST /api/workflows/{name}/enabled": classProject,
}

func init() {
	for pattern, class := range workflowRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerWorkflowRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/workflows", a.listWorkflows)
	a.routeFunc(mux, "POST /api/workflows/check", a.checkWorkflow)
	a.routeFunc(mux, "GET /api/workflows/{name}", a.getWorkflow)
	a.routeFunc(mux, "PUT /api/workflows/{name}", a.saveWorkflow)
	a.routeFunc(mux, "POST /api/workflows/{name}/enabled", a.enableWorkflow)
}

// WorkflowDetail is a workflow with its versions, as GET /api/workflows/{name}
// answers it.
type WorkflowDetail struct {
	Workflow store.WorkflowDef          `json:"workflow"`
	Versions []store.WorkflowDefVersion `json:"versions,omitempty"`
	CanEdit  bool                       `json:"can_edit"`
}

// WorkflowSaveRequest is the body of PUT /api/workflows/{name}.
type WorkflowSaveRequest struct {
	Project string `json:"project"`
	Text    string `json:"text"`
	Note    string `json:"note,omitempty"`
	// Base is the version the text was written on: 0 creates the workflow,
	// -1 saves over whatever is there.
	Base int    `json:"base"`
	Via  string `json:"via,omitempty"`
}

// WorkflowSaveResponse says what a save did; Check is the report of the check,
// also when the save was refused for it (422).
type WorkflowSaveResponse struct {
	Saved *store.WorkflowDefSaved `json:"saved,omitempty"`
	Check workflow.CheckReport    `json:"check"`
}

func writeWorkflowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrWorkflowDefMissing):
		writeCoded(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrWorkflowDefConflict):
		writeCoded(w, http.StatusConflict, "workflow_conflict", err.Error())
	case errors.Is(err, store.ErrWorkflowDefExists):
		writeCoded(w, http.StatusConflict, "workflow_exists", err.Error())
	case denyAccess(w, err):
	default:
		writeStoreError(w, http.StatusBadRequest, err)
	}
}

// workflowWriter refuses a run's own token: what a run writes is a result of
// its workflow, never a change of a workflow.
func workflowWriter(w http.ResponseWriter, r *http.Request) bool {
	if principalOf(r).RunID > 0 {
		writeCoded(w, http.StatusForbidden, "forbidden", "a run does not change workflows")
		return false
	}
	return true
}

func (a *api) listWorkflows(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	defs, err := a.access(r).WorkflowDefs(project)
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	if defs == nil {
		defs = []store.WorkflowDef{}
	}
	writeJSON(w, http.StatusOK, defs)
}

func (a *api) getWorkflow(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	pa := a.access(r)
	def, err := pa.WorkflowDef(project, r.PathValue("name"))
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	out := WorkflowDetail{Workflow: def, CanEdit: pa.CanEditWorkflows(project)}
	if r.URL.Query().Get("versions") == "1" {
		if out.Versions, err = pa.WorkflowDefVersions(project, def.Name); err != nil {
			writeWorkflowError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// WorkflowCheckRequest is the body of POST /api/workflows/check.
type WorkflowCheckRequest struct {
	Project string `json:"project"`
	Text    string `json:"text"`
}

// checkWorkflow checks a text against the catalogue, the aliases and the cap
// of the project, without saving it and without a model call.
func (a *api) checkWorkflow(w http.ResponseWriter, r *http.Request) {
	var in WorkflowCheckRequest
	if err := readJSON(r, &in); err != nil {
		writeCoded(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if denyAccess(w, a.access(r).Check(in.Project, store.ResWorkflow, store.ActRead, store.Object{})) {
		return
	}
	_, rep := workflow.Check(in.Text, workflow.EnvFor(a.st, in.Project))
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) saveWorkflow(w http.ResponseWriter, r *http.Request) {
	if !workflowWriter(w, r) {
		return
	}
	var in WorkflowSaveRequest
	if err := readJSON(r, &in); err != nil {
		writeCoded(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if denyAccess(w, a.access(r).Check(in.Project, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	via := store.WorkflowViaAPI
	if in.Via == store.WorkflowViaMCP {
		via = store.WorkflowViaMCP
	}
	saved, rep, err := workflow.SaveDef(a.st, principalOf(r).ID, in.Project, r.PathValue("name"), in.Text, in.Note, via, in.Base)
	switch {
	case errors.Is(err, workflow.ErrCheck):
		writeJSON(w, http.StatusUnprocessableEntity, WorkflowSaveResponse{Check: rep})
	case err != nil:
		writeWorkflowError(w, err)
	default:
		writeJSON(w, http.StatusOK, WorkflowSaveResponse{Saved: &saved, Check: rep})
	}
}

// WorkflowEnableRequest is the body of POST /api/workflows/{name}/enabled.
type WorkflowEnableRequest struct {
	Project string `json:"project"`
	Enabled bool   `json:"enabled"`
}

func (a *api) enableWorkflow(w http.ResponseWriter, r *http.Request) {
	if !workflowWriter(w, r) {
		return
	}
	var in WorkflowEnableRequest
	if err := readJSON(r, &in); err != nil {
		writeCoded(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if denyAccess(w, a.access(r).Check(in.Project, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	if err := a.st.SetWorkflowDefEnabled(principalOf(r).ID, in.Project, r.PathValue("name"), in.Enabled); err != nil {
		writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": in.Enabled})
}
