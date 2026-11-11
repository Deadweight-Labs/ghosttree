package server

import (
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Commits (REQ-599): aus den Git-Ankern des Ledgers und den Commits der PRs, nach
// SHA zusammengeführt. Das Recht ist das Lesen des Projekts; Einträge an einem
// Commit zählen und erscheinen nur, soweit der Betrachter sie lesen darf. Was es
// nicht gibt und was er nicht lesen darf, antwortet gleich (404).
func init() {
	for _, route := range []string{"GET /api/commits", "GET /api/commits/{sha}", "GET /api/paths/commits", "GET /api/pulls/{n}/commits"} {
		accessRoutes[route] = classProject
	}
}

func (a *api) registerCommitRoutes(mux *http.ServeMux) {
	a.routeFunc(mux, "GET /api/commits", a.listCommits)
	a.routeFunc(mux, "GET /api/commits/{sha}", a.getCommit)
	a.routeFunc(mux, "GET /api/paths/commits", a.pathCommits)
	a.routeFunc(mux, "GET /api/pulls/{n}/commits", a.pullCommits)
}

type commitEntry struct {
	store.CommitInfo
	Link string `json:"link,omitempty"`
}

type commitListReply struct {
	store.CommitList
	Items []commitEntry `json:"items"`
}

func (a *api) listCommits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := scope.NormalizeRemote(q.Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	list, err := a.access(r).Commits(project, q.Get("branch"), intParam(r, "limit", 0), intParam(r, "offset", 0))
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	reply := commitListReply{CommitList: list, Items: make([]commitEntry, 0, len(list.Items))}
	for _, c := range list.Items {
		reply.Items = append(reply.Items, commitEntry{CommitInfo: c, Link: b.Commit(c.SHA)})
	}
	writeJSON(w, http.StatusOK, reply)
}

type commitEntryLink struct {
	store.CommitEntry
	Link string `json:"link,omitempty"`
}

type commitDetailReply struct {
	store.CommitInfo
	Project string            `json:"project"`
	Said    []commitEntryLink `json:"said"`
	Link    string            `json:"link,omitempty"`
	Card    links.Card        `json:"card"`
}

// entryLink ist die Adresse eines Eintrags am Commit.
func entryLink(b links.Builder, e store.CommitEntry) string {
	switch e.Kind {
	case store.CommitKindKnowledge:
		return b.Knowledge(e.ID)
	case store.CommitKindRequest:
		return b.Issue(e.ID)
	case store.CommitKindPain:
		return b.Pain(e.ID)
	case store.CommitKindIdea:
		return b.Idea(e.ID)
	case store.CommitKindFeature:
		return b.Feature(e.ID)
	case store.CommitKindGhost:
		if e.Branch != "" {
			return b.Blob(e.Branch, e.Path)
		}
	case store.CommitKindComment:
		return b.Comment(e.ObjectKind, e.ObjectID, e.ID)
	}
	return ""
}

func commitLinks(b links.Builder, in []store.CommitEntry) []commitEntryLink {
	out := make([]commitEntryLink, 0, len(in))
	for _, e := range in {
		out = append(out, commitEntryLink{CommitEntry: e, Link: entryLink(b, e)})
	}
	return out
}

// getCommit liefert einen Commit (voller Hash oder Präfix ab 7 Zeichen) mit dem,
// was zu ihm gesagt wurde.
func (a *api) getCommit(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	d, err := a.access(r).CommitView(project, r.PathValue("sha"))
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	writeJSON(w, http.StatusOK, commitDetailReply{CommitInfo: d.CommitInfo, Project: d.Project, Said: commitLinks(b, d.Said),
		Link: b.Commit(d.SHA), Card: b.CommitCard(d.CommitInfo)})
}

type pathCommitReply struct {
	store.PathCommit
	Link    string            `json:"link,omitempty"`
	Matched []commitEntryLink `json:"matched"`
}

type pathCommitListReply struct {
	store.PathCommitList
	Items []pathCommitReply `json:"items"`
}

// pathCommits liefert zu einem Pfad (Datei oder Ordner) die Commits, an denen
// Einträge hängen, die beim Schreiben mit diesem Pfad zu tun hatten.
func (a *api) pathCommits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project := scope.NormalizeRemote(q.Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	list, err := a.access(r).CommitsForPath(project, q.Get("path"), intParam(r, "limit", 0), intParam(r, "offset", 0))
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	reply := pathCommitListReply{PathCommitList: list, Items: make([]pathCommitReply, 0, len(list.Items))}
	for _, c := range list.Items {
		reply.Items = append(reply.Items, pathCommitReply{PathCommit: c, Link: b.Commit(c.SHA), Matched: commitLinks(b, c.Matched)})
	}
	writeJSON(w, http.StatusOK, reply)
}

// pullCommits liefert die Commits eines PRs in der Reihenfolge des PRs.
func (a *api) pullCommits(w http.ResponseWriter, r *http.Request) {
	project := scope.NormalizeRemote(r.URL.Query().Get("project"))
	if project == "" {
		writeCoded(w, http.StatusBadRequest, "project_required", "name the project")
		return
	}
	n, ok := pullNumber(w, r)
	if !ok {
		return
	}
	list, err := a.access(r).PullCommits(project, n)
	if err != nil {
		writePullError(w, err)
		return
	}
	b := links.New(a.requestBaseURL(r), project)
	items := make([]commitEntry, 0, len(list.Items))
	for _, c := range list.Items {
		items = append(items, commitEntry{CommitInfo: c, Link: b.Commit(c.SHA)})
	}
	writeJSON(w, http.StatusOK, struct {
		Project string        `json:"project"`
		PR      int64         `json:"pr"`
		Link    string        `json:"link,omitempty"`
		Items   []commitEntry `json:"items"`
	}{list.Project, list.PR, b.Pull(n), items})
}
