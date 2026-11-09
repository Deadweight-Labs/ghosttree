package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
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

// searchHit is one hit. Date and Area are where and when it came to be: the
// time of its origin context (or of its creation) and the main area of the
// session that wrote it; Stamp is that time as RFC 3339 for the tooltip.
type searchHit struct {
	Title, Sub, Href  string
	Date, Stamp, Area string
}

// searchGroup is the hits of one kind. All is the link to the full list, empty
// when nothing lies behind the shown hits.
type searchGroup struct {
	Key, Label, Icon string
	Hits             []searchHit
	All              string
	// Mark is the icon of the kind, Href the link that narrows the page to it.
	Mark, Href string
	// Shown is how many hits the group shows ("5+" when more lie behind them).
	Shown string
}

type searchView struct {
	Q      string
	Groups []searchGroup
	// Searched: the query was long enough to run.
	Searched bool
	// Notice is what is wrong with the search line (an unknown qualifier, one
	// that guests do not get, a kind this page does not show); empty when
	// the search ran as asked. NoticeKind is "err" or "note".
	Notice, NoticeKind string
	// Kind is the group the page is narrowed to ("" = all), AllHref the link
	// back to all of them.
	Kind, AllHref string
	// Presets are the saved searches of the project, CanSave the form that keeps
	// this search (a project has to be chosen); Project and Token belong to it.
	Presets []presetLink
	CanSave bool
	Project string
	Token   string
	// Quick are the quick filters (saved and supplied searches) with their form
	// parts; Preset names the quick filter that ran, Lines its search lines.
	Quick      []quickFilter
	Preset     string
	PresetName string
	Lines      []string
	// Scopes are the places a search can be kept at: for me, the project, the org.
	Scopes []saveScope
}

// searchKindMark is the icon of each kind of hit.
var searchKindMark = map[string]string{"requests": "issue", "ideas": "idea", "knowledge": "book", "sessions": "term", "tasks": "pin", "pains": "pain"}

// searchPage searches requests, ideas, knowledge, sessions, tasks and pains in a
// fixed order, one group each. The line is the search language of REQ-600
// (internal/query): free text and qualifiers such as is:open or area:internal/store;
// a quick filter (preset) runs its lines one after the other and joins the hits.
// Every group asks the same store calls as its own list page and so sees exactly
// what that page shows the viewer. A guest searches requests, ideas and knowledge
// only, as the start page shows them nothing else. The groups are never mixed
// into one ranking, and the sessions group runs the sessions search as it is,
// including its ranking for restricted viewers.
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
	v := &searchView{Q: q, Project: project, Token: csrfOf(r)}
	lines := []string{q}
	if name := r.URL.Query().Get("preset"); name != "" {
		lines = a.presetLines(l, r, pa, v, name)
	} else {
		v.Searched = utf8.RuneCountInString(q) >= minSearchRunes
	}
	kind := r.URL.Query().Get("kind")
	href := func(k string) string {
		qs := url.Values{"q": {q}}
		if v.Preset != "" {
			qs = r.URL.Query()
			qs.Del("kind")
		}
		if k != "" {
			qs.Set("kind", k)
		}
		return "/ui/search?" + qs.Encode()
	}
	v.AllHref = href("")
	now := overviewNow().UTC()
	if v.Searched {
		person := personOf(r)
		merged := map[string]*searchGroup{}
		var order []string
		for _, line := range lines {
			sl, problem := a.parseSearchLine(line, pa, guest, project, person, now)
			if problem != nil {
				v.Notice, v.NoticeKind = l.msg(problem.Key, problem.Args...), "err"
				v.Groups, v.Searched = nil, false
				break
			}
			if !sl.qualified && utf8.RuneCountInString(sl.text) < minSearchRunes {
				continue
			}
			if un := sl.unsupportedKinds(); len(un) > 0 && v.Notice == "" {
				v.Notice, v.NoticeKind = l.msg("search.err.kinds", strings.Join(un, ", ")), "note"
			}
			var groups []searchGroup
			groups = append(groups, a.searchRequests(l, pa, sl, line, now))
			groups = append(groups, a.searchIdeas(l, pa, sl, line, now))
			groups = append(groups, a.searchKnowledge(l, pa, sl, line, now))
			if !guest {
				groups = append(groups, a.searchSessions(l, r, pa, sl, line, now))
				groups = append(groups, a.searchTasks(l, pa, sl, now))
				groups = append(groups, a.searchPains(l, pa, sl, now))
			}
			for _, g := range groups {
				if len(g.Hits) == 0 {
					continue
				}
				m, ok := merged[g.Key]
				if !ok {
					g.Mark, g.Href = searchKindMark[g.Key], href(g.Key)
					merged[g.Key] = &g
					order = append(order, g.Key)
					continue
				}
				for _, h := range g.Hits {
					if !hasHitHref(m.Hits, h.Href) {
						m.Hits = append(m.Hits, h)
					}
				}
				if m.All == "" {
					m.All = g.All
				}
			}
		}
		for _, key := range order {
			g := *merged[key]
			g.Shown = strconv.Itoa(len(g.Hits))
			if g.All != "" {
				g.Shown += "+"
			}
			v.Groups = append(v.Groups, g)
		}
	}
	if kind != "" {
		for _, g := range v.Groups {
			if g.Key == kind {
				v.Kind = kind
			}
		}
	}
	a.searchSidebar(l, r, pa, v, project, guest)
	a.renderBrowser(w, r, "search", pageData{Title: l.msg("search.title"), Project: project, SearchV: v})
}

func hasHitHref(hits []searchHit, href string) bool {
	for _, h := range hits {
		if h.Href == href {
			return true
		}
	}
	return false
}

// readableParents returns the function that keeps, of a list of issue numbers,
// those the viewer may read: parent:7 would otherwise tell that a hidden issue 7
// exists. Without parent: or blocks: in the line nothing is looked at.
func (a *app) readableParents(pa *store.ProjectAccess, sl searchLine) func([]int64) []int64 {
	if !sl.q.Has(query.KeyParent) && !sl.q.Has(query.KeyBlocks) {
		return func([]int64) []int64 { return nil }
	}
	return func(in []int64) []int64 {
		var out []int64
		for _, id := range in {
			if a.requestReadable(pa, id) {
				out = append(out, id)
			}
		}
		return out
	}
}

// factsFor reads the properties of a kind of object for its hits: the numbers
// are the object ids. A failure leaves the facts empty, so a qualified search
// finds nothing it cannot prove instead of everything.
func (a *app) factsFor(kind string, ids []int64) map[int64]store.ObjectFacts {
	facts, err := a.store.ObjectFacts(kind, ids)
	if err != nil {
		return map[int64]store.ObjectFacts{}
	}
	return facts
}

func (a *app) searchRequests(l lang, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "requests", Label: l.msg("nav.requests"), Icon: "requests"}
	if !sl.wants(query.KindRequest) {
		return g
	}
	project := sl.project
	if project != "" && pa.GateList(project, store.ResRequest, true) != nil {
		project = "\x00hidden"
	}
	filter := requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Query: sl.text, Limit: sl.fetch,
		Commit: sl.anchor.Commit, PR: sl.anchor.PR, Origin: sl.origin}
	page, err := a.store.SearchRequestsWith(pa.RequestFilter(filter), store.SearchAuto)
	if err != nil {
		return g
	}
	hits := page.Results
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.Request.ID
	}
	facts := a.factsFor("request", ids)
	readable := a.readableParents(pa, sl)
	var kept []requestdomain.SearchHit
	for _, h := range hits {
		of := facts[h.Request.ID]
		of.Parents, of.Blocks = readable(of.Parents), readable(of.Blocks)
		facts[h.Request.ID] = of
		if sl.qualified && !sl.q.Match(factsOf(pa, query.KindRequest, h.Request.ID, h.Request.State, h.Request.Type, h.Request.Scope.Project, h.Request.Person, "", h.Request.CreatedAt, h.Request.UpdatedAt, of)) {
			continue
		}
		kept = append(kept, h)
	}
	pa.NoteRequestHits(page.Results)
	for i, raw := range kept {
		if i == searchPerGroup {
			if projects := visibleProjects(pa, project); len(projects) > 0 {
				g.All = repoHref(projects[0]) + "/issues?q=" + url.QueryEscape(line)
			}
			break
		}
		h := pa.RequestHitView(raw)
		sub := requestStateLabel(l, h.Request.State)
		if p := lastSegment(h.Request.Scope.Project); p != "" {
			sub = p + " · " + sub
		}
		o := originOf(l, now, h.Request.CreatedAt, facts[raw.Request.ID])
		g.Hits = append(g.Hits, searchHit{Title: h.Request.Title, Sub: sub, Href: requestURL(h.Request.ID), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchIdeas(l lang, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "ideas", Label: l.msg("issues.seg.ideas"), Icon: "ideas"}
	if !sl.wants(query.KindIdea) {
		return g
	}
	type found struct {
		idea idea.Idea
		of   store.ObjectFacts
	}
	var all []found
	for _, p := range visibleProjects(pa, sl.project) {
		list, err := pa.SearchIdeas(idea.Filter{Project: p, State: "all", Query: sl.text, Limit: sl.fetch})
		if err != nil {
			continue
		}
		ids := make([]int64, len(list))
		for i, it := range list {
			ids[i] = it.ID
		}
		facts := a.factsFor("idea", ids)
		for _, it := range list {
			if sl.qualified && !sl.q.Match(factsOf(pa, query.KindIdea, it.ID, it.State, "", it.Project, it.Person, "", it.CreatedAt, it.UpdatedAt, facts[it.ID])) {
				continue
			}
			all = append(all, found{it, facts[it.ID]})
		}
	}
	for i, f := range all {
		if i == searchPerGroup {
			if projects := visibleProjects(pa, sl.project); len(projects) > 0 {
				g.All = repoHref(projects[0]) + "/issues?q=" + url.QueryEscape("is:idea "+line)
			}
			break
		}
		o := originOf(l, now, f.idea.CreatedAt, f.of)
		g.Hits = append(g.Hits, searchHit{Title: f.idea.Title, Sub: lastSegment(f.idea.Project) + " · " + label(l, ideaStateKeys, f.idea.State),
			Href: repoHref(f.idea.Project) + "/ideas/" + strconv.FormatInt(f.idea.ID, 10), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchKnowledge(l lang, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "knowledge", Label: l.msg("nav.knowledge"), Icon: "knowledge"}
	project := sl.project
	if !sl.wants(query.KindKnowledge) || (project != "" && pa.GateList(project, store.ResKnowledge, true) != nil) {
		return g
	}
	readable := func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale")
	}
	entries, err := a.store.SearchKnowledgeWith(sl.text, store.KnowledgeSearch{
		Axes: scope.Axes{Project: project}, Operator: true, Anchor: sl.anchor, Origin: sl.origin,
		Limit: a.overfetch(sl.fetch), Viewer: pa.Viewer(), Keep: readable, Prefilter: pa.KnowledgePrefilter(),
	})
	if err != nil {
		return g
	}
	entries = keep(entries, 0, readable)
	ids := make([]int64, len(entries))
	for i, k := range entries {
		ids[i] = k.ID
	}
	facts := a.factsFor("knowledge", ids)
	if sl.qualified {
		entries = keep(entries, 0, func(k store.Knowledge) bool {
			return sl.q.Match(factsOf(pa, query.KindKnowledge, k.ID, k.Status, k.Type, k.Scope.Project, k.Person, "", k.CreatedAt, k.UpdatedAt, facts[k.ID]))
		})
	}
	for i, k := range entries {
		if i == searchPerGroup {
			g.All = "/ui/knowledge?q=" + url.QueryEscape(line)
			break
		}
		row := a.knowledgeRowFor(l, pa, k, true)
		sub := row.TypeLabel
		if row.Project != "" {
			sub = row.Project + " · " + sub
		}
		o := originOf(l, now, k.CreatedAt, facts[k.ID])
		g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: sub, Href: row.Href, Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchSessions(l lang, r *http.Request, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "sessions", Label: l.msg("nav.sessions"), Icon: "sessions"}
	// A transcript is found by its words, not listed: without a word there is no hit.
	if !sl.wants(query.KindSession) || strings.TrimSpace(sl.text) == "" {
		return g
	}
	filter := store.SessionFilter{Project: sl.project}
	limit := searchPerGroup + 1
	if sl.qualified {
		limit = min(sl.fetch, 60)
	}
	page, err := a.store.SearchTranscripts(pa, store.SearchQuery{Q: sl.text, Filter: filter, Sort: "best", Limit: limit})
	if err != nil {
		return g
	}
	n := 0
	for _, grp := range page.Groups {
		s := grp.Session
		if sl.qualified {
			f := query.Facts{Kind: query.KindSession, Repo: s.Scope.Project, Author: s.Owner, Created: s.StartedAt, Updated: s.LastSeenAt}
			if s.Scope.Branch != "" {
				f.Branches = []string{s.Scope.Branch}
			}
			if !sl.q.Match(f) {
				continue
			}
		}
		if n == searchPerGroup {
			g.All = "/ui/sessions?q=" + url.QueryEscape(sl.text)
			break
		}
		n++
		title := s.Title
		if title == "" {
			title = l.msg("sessions.untitled")
		}
		href := "/ui/sessions/" + s.PublicID
		if len(grp.Hits) > 0 {
			href = sessionURL(s.PublicID, url.Values{"at": {strconv.Itoa(grp.Hits[0].Seq)}, "q": {sl.text}, "back": {"1"}}, "c"+strconv.Itoa(grp.Hits[0].Seq))
		}
		o := originOf(l, now, s.StartedAt, store.ObjectFacts{})
		g.Hits = append(g.Hits, searchHit{Title: title, Sub: shortAge(l, now, parseStamp(s.LastSeenAt)) + " · " + sessionMeta(s), Href: href, Date: o.Date, Stamp: o.Stamp})
	}
	return g
}

func (a *app) searchTasks(l lang, pa *store.ProjectAccess, sl searchLine, now time.Time) searchGroup {
	g := searchGroup{Key: "tasks", Label: l.msg("nav.tab_tasks"), Icon: "action"}
	if !sl.wants(query.KindTask) {
		return g
	}
	words := strings.Fields(strings.ToLower(sl.text))
	for _, p := range visibleProjects(pa, sl.project) {
		// ListHumanTasks is the visibility check; the text match runs on what it lets through.
		tasks, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: "all", Limit: tasksShown})
		if err != nil {
			continue
		}
		var matched []humantask.Task
		for _, t := range tasks {
			hay := strings.ToLower(t.Title + " " + t.Description + " " + t.Location)
			ok := true
			for _, w := range words {
				ok = ok && strings.Contains(hay, strings.Trim(w, `"`))
			}
			if ok {
				matched = append(matched, t)
			}
		}
		ids := make([]int64, len(matched))
		for i, t := range matched {
			ids[i] = t.ID
		}
		facts := a.factsFor("task", ids)
		readable := a.readableParents(pa, sl)
		for _, t := range matched {
			of := facts[t.ID]
			of.Parents, of.Blocks = readable(of.Parents), readable(of.Blocks)
			facts[t.ID] = of
			if sl.qualified && !sl.q.Match(factsOf(pa, query.KindTask, t.ID, t.State, t.Kind, p, t.CreatedBy, t.Assignee, t.CreatedAt, t.UpdatedAt, of)) {
				continue
			}
			if len(g.Hits) == searchPerGroup {
				g.All = "/ui/inbox?f=aufgaben&done=1"
				return g
			}
			sub := lastSegment(t.Project)
			if t.State != humantask.Open {
				sub += " · " + label(l, taskStateKeys, t.State)
			}
			o := originOf(l, now, t.CreatedAt, of)
			g.Hits = append(g.Hits, searchHit{Title: t.Title, Sub: sub, Href: taskHref(t), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
		}
	}
	return g
}

func (a *app) searchPains(l lang, pa *store.ProjectAccess, sl searchLine, now time.Time) searchGroup {
	g := searchGroup{Key: "pains", Label: l.msg("nav.tab_pains"), Icon: "warn"}
	projects := visibleProjects(pa, sl.project)
	if len(projects) == 0 || !sl.wants(query.KindPain) {
		return g
	}
	// One search over every project the viewer sees, not one per project: one
	// embedding of the query (and one timeout), and one share of hits for meaning
	// alone for the whole group, so that the neighbours of a project early in the
	// alphabet do not crowd out the hit of a later one.
	list, err := pa.SearchPains(pain.Filter{Projects: projects, State: "all", Query: sl.text, Limit: sl.fetch})
	if err != nil {
		return g
	}
	ids := make([]int64, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	facts := a.factsFor("pain", ids)
	for _, row := range list {
		if sl.qualified && !sl.q.Match(factsOf(pa, query.KindPain, row.ID, row.State, "", row.Project, row.Person, "", row.CreatedAt, row.UpdatedAt, facts[row.ID])) {
			continue
		}
		if len(g.Hits) == searchPerGroup {
			g.All = "/ui/pains"
			break
		}
		o := originOf(l, now, row.CreatedAt, facts[row.ID])
		g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: lastSegment(row.Project) + " · " + label(l, painStateKeys, row.State), Href: painURL(row.ID), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}
