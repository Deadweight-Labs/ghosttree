package server

import (
	"errors"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Sessions an Commits und PRs (IDEA-31, P1 und P3).
//
//	POST /api/session-git-events        der Collector meldet Git-Vorgänge seiner Sessions
//	GET  /api/commits/{sha}/sessions    Sessions zu einem Commit, soweit der Betrachter sie sehen darf
//	GET  /api/pulls/{n}/sessions        Sessions zu einem PR, ebenso
//
// Die Route zum Melden steht NEBEN dem Präfix /api/sessions und nicht darunter:
// ein älterer Server beantwortet ein unbekanntes Wort unter einem Präfix mit 400
// statt 404, und der Collector erkennt nur ein 404 als "Route fehlt".
func init() {
	accessRoutes["POST /api/session-git-events"] = classAccount
	accessRoutes["GET /api/commits/{sha}/sessions"] = classProject
	accessRoutes["GET /api/pulls/{n}/sessions"] = classProject
}

func (a *api) registerSessionGitRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/session-git-events", a.recordSessionGitEvents)
	a.routeFunc(mux, "GET /api/commits/{sha}/sessions", a.commitSessions)
	a.routeFunc(mux, "GET /api/pulls/{n}/sessions", a.pullSessions)
}

// maxSessionGitBody begrenzt den Körper: 500 Vorgänge mit je 20 SHAs sind weit
// darunter.
const maxSessionGitBody = 4 << 20

type sessionGitEventsRequest struct {
	Events []store.SessionGitEvent `json:"events"`
}

// recordSessionGitEvents nimmt die Vorgänge an. Jede Session muss dem Konto des
// Tokens gehören; sonst wird nichts geschrieben und die Antwort sieht für eine
// fremde wie für eine unbekannte Session gleich aus.
func (a *api) recordSessionGitEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSessionGitBody)
	var in sessionGitEventsRequest
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	account := store.AccountIDOf(principalOf(r).ID)
	if account == 0 {
		writeErr(w, http.StatusForbidden, "git events are recorded for the account that owns the session")
		return
	}
	res, err := a.st.RecordSessionGitEvents(account, in.Events)
	switch {
	case errors.Is(err, store.ErrGitEventSession):
		writeErr(w, http.StatusForbidden, "cannot record git events for another person's session")
	case errors.Is(err, store.ErrInvalidInput):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeStoreError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// commitSessions: Antwort wie die übrigen Commit-Routen, ein nicht lesbares
// Projekt ist ein 404.
func (a *api) commitSessions(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	out, err := a.access(r).CommitSessions(project, r.PathValue("sha"))
	if err != nil {
		writePullError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) pullSessions(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	n, ok := pullNumber(w, r)
	if !ok {
		return
	}
	out, err := a.access(r).PullSessions(project, n)
	if err != nil {
		writePullError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
