package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Sessions an Commits und PRs (IDEA-31, P1 und P3).
//
//	POST /api/session-git-events        der Collector meldet Git-Vorgänge seiner Sessions
//	GET  /api/commits/{sha}/sessions    Sessions zu einem Commit, soweit der Betrachter sie sehen darf
//	GET  /api/pulls/{n}/sessions        Sessions zu einem PR, ebenso
//	GET  /api/requests/{id}/sessions    Sessions eines Requests über PRs im Paket und Commit-Nachweise, ebenso
//
// Die Route zum Melden steht NEBEN dem Präfix /api/sessions und nicht darunter:
// ein älterer Server beantwortet ein unbekanntes Wort unter einem Präfix mit 400
// statt 404, und der Collector erkennt nur ein 404 als "Route fehlt".
func init() {
	accessRoutes["POST /api/session-git-events"] = classProject
	accessRoutes["GET /api/commits/{sha}/sessions"] = classProject
	accessRoutes["GET /api/pulls/{n}/sessions"] = classProject
	accessRoutes["GET /api/requests/{id}/sessions"] = classProject
}

func (a *api) registerSessionGitRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "POST /api/session-git-events", a.recordSessionGitEvents)
	a.routeFunc(mux, "GET /api/commits/{sha}/sessions", a.commitSessions)
	a.routeFunc(mux, "GET /api/pulls/{n}/sessions", a.pullSessions)
	a.routeFunc(mux, "GET /api/requests/{id}/sessions", a.requestSessions)
}

// maxSessionGitBody begrenzt den Körper: 500 Vorgänge mit je 20 SHAs sind weit
// darunter.
const maxSessionGitBody = 4 << 20

type sessionGitEventsRequest struct {
	Events []store.SessionGitEvent `json:"events"`
}

// recordSessionGitEvents nimmt die Vorgänge an. Jede Session muss dem Konto des
// Tokens gehören (und, ist der Token an eine Maschine gebunden, auf ihr gelaufen
// sein); sonst wird nichts geschrieben und die Antwort sieht für eine fremde wie
// für eine unbekannte Session gleich aus.
//
// Die Prüfungen sind die von appendChunks: das Recht, eigene Sessions anzulegen
// (ActCreate auf ResSessionMeta), steht vor dem Lesen des Körpers, damit ein
// Konto ohne Recht den Server nicht zum Lesen und Decodieren bringt, und große
// Körper belegen einen von wenigen Plätzen in bigBodies.
func (a *api) recordSessionGitEvents(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	account := store.AccountIDOf(p.ID)
	if account == 0 {
		writeErr(w, http.StatusForbidden, "git events are recorded for the account that owns the session")
		return
	}
	if denyAccess(w, a.access(r).Check("", store.ResSessionMeta, store.ActCreate, store.Object{Own: true})) {
		return
	}
	if r.ContentLength < 0 || r.ContentLength > smallBody {
		select {
		case bigBodies <- struct{}{}:
			defer func() { <-bigBodies }()
		case <-r.Context().Done():
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadWindow))
	}
	var in sessionGitEventsRequest
	if !readLimitedJSON(w, r, &in, maxSessionGitBody) {
		return
	}
	res, err := a.st.RecordSessionGitEvents(account, p.Machine, in.Events)
	switch {
	case errors.Is(err, store.ErrGitEventSession):
		writeErr(w, http.StatusForbidden, "cannot record git events for another person's session")
	case errors.Is(err, store.ErrGitEventMachine):
		writeCoded(w, http.StatusForbidden, "machine_bound", "this token is bound to machine "+p.Machine)
	case errors.Is(err, store.ErrGitEventQuota):
		w.Header().Set("Retry-After", "3600")
		writeCoded(w, http.StatusTooManyRequests, "git_events_quota", "this account reached its daily limit of git events; try again later")
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

// requestSessions nennt die Sessions eines Requests über die PRs seines Pakets und
// seine Commit-Nachweise. Ein Request, den es nicht gibt oder den der Aufrufer
// nicht lesen darf, antwortet wie jede Request-Route (404).
func (a *api) requestSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad request id")
		return
	}
	if !a.checkRequest(w, r, store.RefRequest, id, store.ActRead) {
		return
	}
	out, err := a.access(r).RequestSessions(id)
	if err != nil {
		writeRequestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
