package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	// searchPerGroup is how many hits a group shows; one more is asked for, so
	// "show all" appears only when there is something behind it. No total is
	// ever counted: a number would say how much exists beyond what the viewer sees.
	searchPerGroup = 5
	minSearchRunes = 2
)

type searchHit struct {
	Title, Sub, Href string
}

// searchGroup is the hits of one kind. All is the link to the full list, empty
// when nothing lies behind the shown hits.
type searchGroup struct {
	Key, Label, Icon string
	Hits             []searchHit
	All              string
}

type searchView struct {
	Q      string
	Groups []searchGroup
	// Searched: the query was long enough to run.
	Searched bool
}

// searchPage searches requests, knowledge, sessions, tasks and pains in a fixed
// order, one group each. Every group asks the same store calls as its own list
// page and so sees exactly what that page shows the viewer. A guest searches
// requests and knowledge only, as the start page shows them nothing else. The
// groups are never mixed into one ranking, and the sessions group runs the
// sessions search as it is, including its ranking for restricted viewers.
func (a *app) searchPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > maxQueryRunes {
		q = string([]rune(q)[:maxQueryRunes])
	}
	pa := a.access(r)
	guest := a.viewerOf(r, pa).kind == viewerGuest
	project := selectedProject(a.shellFor(r, "search"))
	v := &searchView{Q: q, Searched: utf8.RuneCountInString(q) >= minSearchRunes}
	if v.Searched {
		add := func(g searchGroup) {
			if len(g.Hits) > 0 {
				v.Groups = append(v.Groups, g)
			}
		}
		add(a.searchRequests(l, pa, project, q))
		add(a.searchKnowledge(l, pa, project, q))
		if !guest {
			add(a.searchSessions(l, r, pa, project, q))
			add(a.searchTasks(l, pa, project, q))
			add(a.searchPains(l, pa, project, q))
		}
	}
	a.renderBrowser(w, r, "search", pageData{Title: l.msg("search.title"), Project: project, SearchV: v})
}

func (a *app) searchRequests(l lang, pa *store.ProjectAccess, project, q string) searchGroup {
	g := searchGroup{Key: "requests", Label: l.msg("nav.requests"), Icon: "requests"}
	if project != "" && pa.GateList(project, store.ResRequest, true) != nil {
		project = "\x00hidden"
	}
	filter := requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Query: q, Limit: searchPerGroup + 1}
	page, err := a.store.SearchRequests(pa.RequestFilter(filter))
	if err != nil {
		return g
	}
	pa.NoteRequestHits(page.Results)
	for i, raw := range page.Results {
		if i == searchPerGroup {
			g.All = "/ui/requests?state=all&q=" + url.QueryEscape(q)
			break
		}
		h := pa.RequestHitView(raw)
		sub := requestStateLabel(l, h.Request.State)
		if p := lastSegment(h.Request.Scope.Project); p != "" {
			sub = p + " · " + sub
		}
		g.Hits = append(g.Hits, searchHit{Title: h.Request.Title, Sub: sub, Href: requestURL(h.Request.ID)})
	}
	return g
}

func (a *app) searchKnowledge(l lang, pa *store.ProjectAccess, project, q string) searchGroup {
	g := searchGroup{Key: "knowledge", Label: l.msg("nav.knowledge"), Icon: "knowledge"}
	if project != "" && pa.GateList(project, store.ResKnowledge, true) != nil {
		return g
	}
	readable := func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale")
	}
	entries, err := a.store.SearchKnowledgeWith(q, store.KnowledgeSearch{
		Axes: scope.Axes{Project: project}, Operator: true,
		Limit: a.overfetch(searchPerGroup + 1), Viewer: pa.Viewer(), Keep: readable, Prefilter: pa.KnowledgePrefilter(),
	})
	if err != nil {
		return g
	}
	entries = keep(entries, searchPerGroup+1, readable)
	for i, k := range entries {
		if i == searchPerGroup {
			g.All = "/ui/knowledge?q=" + url.QueryEscape(q)
			break
		}
		row := a.knowledgeRowFor(l, pa, k, true)
		sub := row.TypeLabel
		if row.Project != "" {
			sub = row.Project + " · " + sub
		}
		g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: sub, Href: row.Href})
	}
	return g
}

func (a *app) searchSessions(l lang, r *http.Request, pa *store.ProjectAccess, project, q string) searchGroup {
	g := searchGroup{Key: "sessions", Label: l.msg("nav.sessions"), Icon: "sessions"}
	filter := store.SessionFilter{Project: project}
	page, err := a.store.SearchTranscripts(pa, store.SearchQuery{Q: q, Filter: filter, Sort: "best", Limit: searchPerGroup + 1})
	if err != nil {
		return g
	}
	now := overviewNow().UTC()
	for i, grp := range page.Groups {
		if i == searchPerGroup {
			g.All = "/ui/sessions?q=" + url.QueryEscape(q)
			break
		}
		s := grp.Session
		title := s.Title
		if title == "" {
			title = l.msg("sessions.untitled")
		}
		href := "/ui/sessions/" + s.PublicID
		if len(grp.Hits) > 0 {
			href = sessionURL(s.PublicID, url.Values{"at": {strconv.Itoa(grp.Hits[0].Seq)}, "q": {q}, "back": {"1"}}, "c"+strconv.Itoa(grp.Hits[0].Seq))
		}
		g.Hits = append(g.Hits, searchHit{Title: title, Sub: shortAge(l, now, parseStamp(s.LastSeenAt)) + " · " + sessionMeta(s), Href: href})
	}
	return g
}

func (a *app) searchTasks(l lang, pa *store.ProjectAccess, project, q string) searchGroup {
	g := searchGroup{Key: "tasks", Label: l.msg("nav.tab_tasks"), Icon: "action"}
	needle := strings.ToLower(q)
	for _, p := range visibleProjects(pa, project) {
		// ListHumanTasks is the visibility check; the text match runs on what it lets through.
		tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: "all", Limit: tasksShown})
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if !strings.Contains(strings.ToLower(t.Title+" "+t.Description+" "+t.Location), needle) {
				continue
			}
			if len(g.Hits) == searchPerGroup {
				g.All = "/ui/tasks?who=all"
				return g
			}
			sub := lastSegment(t.Project)
			if t.State != humantask.Open {
				sub += " · " + label(l, taskStateKeys, t.State)
			}
			g.Hits = append(g.Hits, searchHit{Title: t.Title, Sub: sub, Href: "/ui/tasks?who=all#task-" + strconv.FormatInt(t.ID, 10)})
		}
	}
	return g
}

func (a *app) searchPains(l lang, pa *store.ProjectAccess, project, q string) searchGroup {
	g := searchGroup{Key: "pains", Label: l.msg("nav.tab_pains"), Icon: "warn"}
	for _, p := range visibleProjects(pa, project) {
		list, err := pa.SearchPains(pain.Filter{Project: p, State: "all", Query: q, Limit: searchPerGroup + 1})
		if err != nil {
			continue
		}
		for _, row := range list {
			if len(g.Hits) == searchPerGroup {
				g.All = "/ui/pains"
				return g
			}
			g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: lastSegment(row.Project) + " · " + label(l, painStateKeys, row.State), Href: painURL(row.ID)})
		}
	}
	return g
}
