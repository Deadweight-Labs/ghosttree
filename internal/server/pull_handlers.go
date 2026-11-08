package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Pull Requests (REQ-597, REQ-599). Die Routen hängen am Projekt wie die Pakete:
// wer das Projekt lesen darf, sieht seine PRs, und jeder Eintrag, der an einem PR
// hängt, läuft durch dieselbe Sichtprüfung wie in der Suche. Ein PR, den es nicht
// gibt, ein unbekanntes Projekt und ein Projekt, das der Aufrufer nicht lesen
// darf, antworten gleich (404).
func init() {
	for _, route := range []string{"GET /api/pulls", "GET /api/pulls/{n}", "GET /api/pulls/{n}/knowledge"} {
		accessRoutes[route] = classProject
	}
}

func (a *api) registerPullRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/pulls", a.listPulls)
	a.routeFunc(mux, "GET /api/pulls/{n}", a.getPull)
	a.routeFunc(mux, "GET /api/pulls/{n}/knowledge", a.pullKnowledge)
}

func pullNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil || n <= 0 {
		writeCoded(w, http.StatusBadRequest, "invalid_pr", "pr must be a positive pull request number")
		return 0, false
	}
	return n, true
}

func writePullNotFound(w http.ResponseWriter) {
	writeCoded(w, http.StatusNotFound, "not_found", "not found")
}

// writePullError antwortet auf einen Fehler der Lesefunktionen: Regelfehler
// (ungültiger Filter) als 400, alles Unlesbare als dasselbe 404.
func writePullError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrAccessNotFound) {
		writePullNotFound(w)
		return
	}
	if denyAccess(w, err) {
		return
	}
	writeRequestError(w, err)
}

// pullListReply ist die Seite der Liste mit den Links ihrer Zeilen.
type pullListReply struct {
	store.PullList
	Link  string      `json:"link,omitempty"`
	Items []pullEntry `json:"items"`
}

type pullEntry struct {
	store.PullSummary
	Link string `json:"link,omitempty"`
}

// listPulls liefert die PRs eines Projekts: state (open, merged, closed, all),
// branch (Head), author, limit (höchstens 100) und offset.
func (a *api) listPulls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := scope.NormalizeRemote(q.Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	list, err := a.access(r).PullRequests(project, store.PullListFilter{State: q.Get("state"), Branch: q.Get("branch"), Author: q.Get("author"),
		Limit: intParam(r, "limit", 0), Offset: intParam(r, "offset", 0)})
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	reply := pullListReply{PullList: list, Items: make([]pullEntry, 0, len(list.Items))}
	for _, it := range list.Items {
		reply.Items = append(reply.Items, pullEntry{PullSummary: it, Link: b.Pull(it.Number)})
	}
	writeJSON(w, http.StatusOK, reply)
}

// pullDetailReply ist ein PR mit allem, was zu ihm gehört, samt Link und Karte.
type pullDetailReply struct {
	store.PullDetail
	Link string     `json:"link,omitempty"`
	Card links.Card `json:"card"`
}

// getPull liefert einen PR mit seiner Checkliste (Paket, Prüfstand, Features) und
// dem Brief (was zu ihm gesagt wurde).
func (a *api) getPull(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	n, ok := pullNumber(w, r)
	if !ok {
		return
	}
	d, err := a.access(r).PullRequestDetail(project, n)
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	card := b.PullCard(d.PullRequestInfo, d.Commits)
	writeJSON(w, http.StatusOK, pullDetailReply{PullDetail: d, Link: b.Pull(n), Card: card})
}

// pullKnowledge liefert Wissen und Ghost-Pfade mit dem PR-Anker eines PRs. Ein
// PR, den es nicht gibt, und einer, zu dem nichts gesagt wurde, antworten gleich.
func (a *api) pullKnowledge(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	n, ok := pullNumber(w, r)
	if !ok {
		return
	}
	brief, err := a.access(r).PullRequestBriefView(project, n)
	if err != nil {
		if denyAccess(w, err) {
			return
		}
		writeStoreError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, brief)
}
