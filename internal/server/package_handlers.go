package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Arbeitspakete (REQ-601). Die Routen hängen am Projekt wie die Requests: Lesen
// darf jedes Mitglied, Anlegen und Ändern braucht Schreibrecht an Requests. Die
// Mitglieder eines Pakets laufen vor der Auslieferung durch dieselbe Sichtprüfung
// wie request_get (ProjectAccess.PackageView).
var packageRoutes = map[string]routeClass{
	"POST /api/packages":                         classProject,
	"GET /api/packages":                          classProject,
	"POST /api/packages/import-branch-relations": classProject,
	"GET /api/packages/{id}":                     classProject,
	"PATCH /api/packages/{id}":                   classProject,
	"POST /api/packages/{id}/requests":           classProject,
	"POST /api/packages/{id}/requests/remove":    classProject,
	"GET /api/packages/{id}/review":              classProject,
	"POST /api/criteria/{id}/reopen":             classProject,
}

func init() {
	for pattern, class := range packageRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerPackageRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/packages", a.createPackage)
	a.routeFunc(mux, "GET /api/packages", a.findPackage)
	a.routeFunc(mux, "POST /api/packages/import-branch-relations", a.importBranchRelations)
	a.routeFunc(mux, "GET /api/packages/{id}", a.getPackage)
	a.routeFunc(mux, "PATCH /api/packages/{id}", a.updatePackage)
	a.routeFunc(mux, "POST /api/packages/{id}/requests", a.addPackageRequests)
	a.routeFunc(mux, "POST /api/packages/{id}/requests/remove", a.removePackageRequest)
	a.routeFunc(mux, "GET /api/packages/{id}/review", a.reviewPackage)
	a.routeFunc(mux, "POST /api/criteria/{id}/reopen", a.reopenCriterion)
}

// packageGate prüft den Zugriff auf ein Paket über sein Projekt. Ein Paket, das
// es nicht gibt, und eines in einem verborgenen Projekt antworten gleich.
func (a *api) packageGate(w http.ResponseWriter, r *http.Request, id int64, act store.Action) (string, bool) {
	pa := a.access(r)
	project, err := a.st.PackageRef(id)
	if err != nil {
		pa.Filtered()
		writePackageNotFound(w, id)
		return "", false
	}
	err = pa.Check(project, store.ResRequest, act, store.Object{})
	if errors.Is(err, store.ErrAccessNotFound) {
		writePackageNotFound(w, id)
		return "", false
	}
	return project, !denyAccess(w, err)
}

// packageRequestGate prüft einen Request, den ein Paket aufnehmen oder abgeben
// soll, wie request_start_work den seinen: lesbar und bearbeitbar für den
// Aufrufer, und im Projekt des Pakets. Alles andere antwortet wie eine
// unbekannte Nummer, damit weder ein verborgener Request noch einer in einem
// fremden Projekt sich durch die Antwort verrät. Das Paket-Recht allein reicht
// nicht: es sagt nichts darüber, welche Requests der Aufrufer anfassen darf.
func (a *api) packageRequestGate(w http.ResponseWriter, r *http.Request, requestID int64, project string) bool {
	ref, err := a.st.RequestRef(store.RefRequest, requestID)
	if err != nil || ref.Project != project {
		a.access(r).Filtered()
		writeRequestError(w, sql.ErrNoRows)
		return false
	}
	return a.checkRequest(w, r, store.RefRequest, requestID, store.ActWork)
}

func writePackageNotFound(w http.ResponseWriter, id int64) {
	writeRequestError(w, requestdomain.NewRuleError("package_not_found", "no work package PKG-"+strconv.FormatInt(id, 10), "list the packages of the branch to find its id", nil))
}

func packageMembersFromBody(in []struct {
	RequestID int64  `json:"request_id"`
	Role      string `json:"role"`
}) []requestdomain.PackageMember {
	out := make([]requestdomain.PackageMember, 0, len(in))
	for _, m := range in {
		out = append(out, requestdomain.PackageMember{RequestID: m.RequestID, Role: m.Role})
	}
	return out
}

func (a *api) createPackage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project  string `json:"project"`
		Branch   string `json:"branch"`
		Name     string `json:"name"`
		PR       int64  `json:"pr"`
		Requests []struct {
			RequestID int64  `json:"request_id"`
			Role      string `json:"role"`
		} `json:"requests"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project := scope.CanonicalAxes(scope.Axes{Project: body.Project}).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "a work package belongs to a project: name the project")
		return
	}
	if !a.gateProject(w, r, project) {
		return
	}
	if denyAccess(w, a.access(r).Check(project, store.ResRequest, store.ActCreate, store.Object{Own: true})) {
		return
	}
	for _, m := range body.Requests {
		if !a.packageRequestGate(w, r, m.RequestID, project) {
			return
		}
	}
	detail, err := a.st.CreatePackage(requestdomain.PackageInput{
		Project: project, Branch: body.Branch, Name: body.Name, PR: body.PR,
		Members: packageMembersFromBody(body.Requests), Person: personOf(r),
	})
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a.access(r).PackageView(detail, false))
}

// findPackage beantwortet "welches Paket gilt für diesen Branch" (mit Erbe aus
// der Kette lineage) und "welches Paket hängt an diesem PR".
func (a *api) findPackage(w http.ResponseWriter, r *http.Request) {
	ax := axesFromQuery(r)
	if !a.listGate(w, r, ax.Project, store.ResRequest) {
		return
	}
	if ax.Project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	full := r.URL.Query().Get("full") == "1"
	var detail requestdomain.PackageDetail
	var err error
	switch pr := r.URL.Query().Get("pr"); {
	case pr != "":
		n, perr := strconv.ParseInt(pr, 10, 64)
		if perr != nil || n <= 0 {
			writeCoded(w, http.StatusBadRequest, "invalid_pr", "pr must be a positive pull request number")
			return
		}
		detail, err = a.st.PackageForPR(ax.Project, n)
	case ax.Branch != "":
		detail, err = a.st.PackageForBranch(ax.Project, ax.Branch, ax.Lineage)
	default:
		writeCoded(w, http.StatusBadRequest, "branch_or_pr_required", "name a branch or a pr")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeRequestError(w, requestdomain.NewRuleError("package_not_found", "no open work package for this branch or pr", "create one with package_update or ctx package create", nil))
		return
	}
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, full))
}

func (a *api) getPackage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad package id")
		return
	}
	if _, ok := a.packageGate(w, r, id, store.ActRead); !ok {
		return
	}
	detail, err := a.st.PackageByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, r.URL.Query().Get("full") == "1"))
}

// reviewPackage liefert das vollständige Lastenheft: alle Mitglieder mit
// jedem Kriterium, seinem Stand und den Nachweisen samt Git-Anker.
func (a *api) reviewPackage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad package id")
		return
	}
	if _, ok := a.packageGate(w, r, id, store.ActRead); !ok {
		return
	}
	detail, err := a.st.PackageByID(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, true))
}

func (a *api) updatePackage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad package id")
		return
	}
	var patch requestdomain.PackagePatch
	if err := readJSON(r, &patch); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := a.packageGate(w, r, id, store.ActWork); !ok {
		return
	}
	detail, err := a.st.UpdatePackage(id, patch)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, false))
}

func (a *api) addPackageRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad package id")
		return
	}
	var body struct {
		Requests []struct {
			RequestID int64  `json:"request_id"`
			Role      string `json:"role"`
		} `json:"requests"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := a.packageGate(w, r, id, store.ActWork)
	if !ok {
		return
	}
	for _, m := range body.Requests {
		if !a.packageRequestGate(w, r, m.RequestID, project) {
			return
		}
	}
	detail, err := a.st.AddPackageRequests(id, packageMembersFromBody(body.Requests), body.Reason, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, false))
}

func (a *api) removePackageRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad package id")
		return
	}
	var body struct {
		RequestID int64  `json:"request_id"`
		Reason    string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project, ok := a.packageGate(w, r, id, store.ActWork)
	if !ok {
		return
	}
	if !a.packageRequestGate(w, r, body.RequestID, project) {
		return
	}
	detail, err := a.st.RemovePackageRequest(id, body.RequestID, body.Reason, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.access(r).PackageView(detail, false))
}

// importBranchRelations übernimmt die Übergangs-Relationen "branch:<projekt>@<branch>"
// in Pakete. Es verlangt Schreibrecht im genannten Projekt (auch für den
// Probelauf: er zeigt, was hinter dem Schreibrecht liegt) und fasst nur
// Requests an, die der Aufrufer lesen und bearbeiten darf. Das Projekt ist
// Pflicht. Die Relationen bleiben stehen und werden als übernommen markiert.
func (a *api) importBranchRelations(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		DryRun  bool   `json:"dry_run"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	project := scope.CanonicalAxes(scope.Axes{Project: body.Project}).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project to import for")
		return
	}
	if !a.gateProject(w, r, project) {
		return
	}
	if denyAccess(w, a.access(r).Check(project, store.ResRequest, store.ActWork, store.Object{})) {
		return
	}
	report, err := a.access(r).ImportBranchRelations(project, body.DryRun, personOf(r))
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (a *api) reopenCriterion(w http.ResponseWriter, r *http.Request) {
	criterionID, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad criterion id")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeStoreError(w, http.StatusBadRequest, err)
		return
	}
	if !a.checkRequest(w, r, store.RefCriterion, criterionID, store.ActWork) {
		return
	}
	if err := a.st.ReopenCriterion(criterionID, body.Reason, personOf(r), anchorFromRequest(r)); err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"criterion_id": criterionID, "state": "open"})
}
