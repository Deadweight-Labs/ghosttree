package server

import (
	"net/http"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Abfragen für die Oberfläche (REQ-601): Zähler offener Requests mit der
// Semantik der Liste, die Pakete eines Requests und die Paketliste eines
// Projekts. Alles läuft durch ProjectAccess, ein unlesbares Objekt antwortet wie
// ein unbekanntes.
var requestCountRoutes = map[string]routeClass{
	"GET /api/requests/count":         classProject,
	"GET /api/requests/{id}/packages": classProject,
	"GET /api/packages/list":          classProject,
}

func init() {
	for pattern, class := range requestCountRoutes {
		accessRoutes[pattern] = class
	}
}

func (a *api) registerRequestCountRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/requests/count", a.countRequests)
	a.routeFunc(mux, "GET /api/requests/{id}/packages", a.packagesOfRequest)
	a.routeFunc(mux, "GET /api/packages/list", a.listPackages)
}

// countRequests zählt wie GET /api/requests listet: dieselben Achsen (project,
// branch, machine), derselbe Sichtfilter, state aus der Anfrage (Vorgabe open).
func (a *api) countRequests(w http.ResponseWriter, r *http.Request) {
	ax := axesFromQuery(r)
	if !a.listGate(w, r, ax.Project, store.ResRequest) {
		return
	}
	state := "open"
	if r.URL.Query().Has("state") {
		state = r.URL.Query().Get("state")
	}
	switch state {
	case "", "open", "done", "dropped":
	default:
		writeCoded(w, http.StatusBadRequest, "invalid_state", "state is open, done, dropped or empty for all")
		return
	}
	n, err := a.access(r).CountRequests(ax, state)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "state": state})
}

func (a *api) packagesOfRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActRead) {
		return
	}
	list, err := a.access(r).PackagesOfRequest(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (a *api) listPackages(w http.ResponseWriter, r *http.Request) {
	project := axesFromQuery(r).Project
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	if !a.listGate(w, r, project, store.ResRequest) {
		return
	}
	list, err := a.access(r).PackagesOfProject(project, r.URL.Query().Get("state"))
	if err != nil {
		if !denyAccess(w, err) {
			writeRequestError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, nonNil[requestdomain.PackageSummary](list))
}
