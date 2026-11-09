package web

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// searchPerGroup is how many hits a group shows on the page of all kinds;
	// searchKindPage is how many it shows once the page is narrowed to one kind,
	// and the step of "More hits" (n= in the address, never above searchCountCap).
	searchPerGroup = 5
	searchKindPage = 20
	minSearchRunes = 2
	// searchCountCap is how many hits of a kind are counted. The count is of the
	// hits that survive the very same rights and qualifier rules as the shown
	// ones (a count straight in SQL would count what the viewer may not see, and
	// most rights are rules in Go), so it reads at most this many rows of a kind;
	// beyond it the page says "100+". Requests are the exception: their rights
	// are part of the SQL, so a search of plain words counts them exactly with
	// one COUNT (CountRequestMatches). A viewer is told no number that includes
	// a hit he may not read.
	searchCountCap = 100
	// searchSessionCount is the same for sessions, whose search is the most
	// expensive one: more rows are read only when the page asks to show more.
	searchSessionCount = 30
)

// searchHit is one hit. Date and Area are where and when it came to be: the
// time of its origin context (or of its creation) and the main area of the
// session that wrote it; Stamp is that time as RFC 3339 for the tooltip.
type searchHit struct {
	Title, Sub, Href  string
	Date, Stamp, Area string
	// Snip is the line of a document the words were found in.
	Snip string
	// RefKind and RefN make the number pill before the title ("#12"); Project is
	// the short name of the repository, shown only when the hits of a group come
	// from more than one. Marked is the title with the searched words marked.
	RefKind, Project, Lead string
	RefN                   int64
	Marked, SnipHTML       template.HTML
}

// searchGroup is the hits of one kind. All is the link to the full list, empty
// when nothing lies behind the shown hits.
type searchGroup struct {
	Key, Label, Icon string
	// Hits are the hits of the group; the kind searches return every hit they
	// count, searchKind cuts them to the window the page shows.
	Hits []searchHit
	// Total is how many hits the kind has for this viewer, Capped says that it
	// has more than were counted (see searchCountCap). Count is Total as the
	// facet shows it ("24", "100+"), TotalLabel the line of the list ("24 hits").
	// Exact: Total came from a COUNT and is not bound by the cap.
	Total             int
	Capped, Exact     bool
	Count, TotalLabel string
	// More is the link that shows more hits of the kind, empty when the group
	// shows all of them.
	More string
	// Mark is the icon of the kind, Href the link that narrows the page to it.
	Mark, Href string
	// MultiProject: the hits come from more than one repository.
	MultiProject bool
	// On: the page is narrowed to this group.
	On bool
}

type searchView struct {
	Q      string
	Groups []searchGroup
	// Searched: the query was long enough to run.
	Searched bool
	// Live is the frame of the live page; its groups come from the stream.
	Live *searchLive
	// Notice is what is wrong with the search line (an unknown qualifier, one
	// that guests do not get, a kind this page does not show); empty when
	// the search ran as asked. NoticeKind is "err" or "note".
	Notice, NoticeKind string
	// Kind is the group the page is narrowed to ("" = all), AllHref the link
	// back to all of them.
	Kind, AllHref string
	// AllCount is the number of hits of all kinds together, as the facet shows it.
	AllCount string
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
var searchKindMark = map[string]string{"docs": "file", "requests": "issue", "ideas": "idea", "knowledge": "book", "sessions": "term", "tasks": "pin", "pains": "pain"}

// searchPage searches requests, ideas, knowledge, documents, sessions, tasks
// and pains in a fixed order, one group each. The line is the search language of REQ-600
// (internal/query): free text and qualifiers such as is:open or area:internal/store;
// a quick filter (preset) runs its lines one after the other and joins the hits.
// Every group asks the same store calls as its own list page and so sees exactly
// what that page shows the viewer. A guest searches requests, ideas, knowledge
// and documents only, as the start page shows them nothing else. The groups are never mixed
// into one ranking, and the sessions group runs the sessions search as it is,
// including its ranking for restricted viewers.
//
// With live=1 (set by the page script, never by a link) the page is only the
// frame: the groups come from /ui/search/stream as each kind is done (searchStream).
// Without it every kind is searched before the page is sent.
func (a *app) searchPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := a.planSearch(r)
	if p.runnable() {
		if r.URL.Query().Get("live") == "1" {
			p.v.Live = p.liveView(r)
		} else {
			p.v.Groups = a.runSearch(r.Context(), r, p)
			p.v.AllCount = allCount(p.v.Groups)
		}
	}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		for _, g := range p.v.Groups {
			if g.Key == kind {
				p.v.Kind = kind
			}
		}
		if p.v.Live != nil && slices.Contains(p.kinds, kind) {
			p.v.Kind = kind
		}
	}
	for i := range p.v.Groups {
		p.v.Groups[i].On = p.v.Groups[i].Key == p.v.Kind
	}
	a.searchSidebar(p.l, r, p.pa, p.v, p.project, p.guest)
	a.renderBrowser(w, r, "search", pageData{Title: p.l.msg("search.title"), Project: p.project, SearchV: p.v})
}

// searchPlan is a search before any kind is searched: the view with what the line
// says, the lines that run and the kinds the viewer gets.
type searchPlan struct {
	l       lang
	pa      *store.ProjectAccess
	v       *searchView
	guest   bool
	project string
	now     time.Time
	// lines are the lines that run, raw their text as typed.
	lines []searchLine
	raw   []string
	// kinds are the groups this viewer gets, in the order of the page.
	kinds []string
	href  func(kind string) string
	// shown is how many hits each group shows: searchPerGroup on the page of all
	// kinds, more once it is narrowed to one (n= of the address).
	shown int
	// kind is the kind the page is narrowed to, "" for the page of all kinds.
	kind string
}

func (p *searchPlan) runnable() bool { return len(p.lines) > 0 }

// planSearch reads the line (or the quick filter) of r for its viewer.
func (a *app) planSearch(r *http.Request) *searchPlan {
	l := a.langFor(r)
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
		if q != "" && !v.Searched {
			v.Notice, v.NoticeKind = l.msg("search.min_chars", minSearchRunes), "note"
		}
	}
	p := &searchPlan{l: l, pa: pa, v: v, guest: guest, project: project, now: overviewNow().UTC(), shown: searchPerGroup}
	p.href = func(k string) string {
		qs := url.Values{"q": {q}}
		if v.Preset != "" {
			qs = r.URL.Query()
			qs.Del("kind")
			qs.Del("live")
			qs.Del("n")
		}
		if k != "" {
			qs.Set("kind", k)
		}
		return "/ui/search?" + qs.Encode()
	}
	v.AllHref = p.href("")
	p.kinds = []string{"requests", "ideas", "knowledge", "docs"}
	if !guest {
		p.kinds = append(p.kinds, "sessions", "tasks", "pains")
	}
	if k := r.URL.Query().Get("kind"); slices.Contains(p.kinds, k) {
		p.kind, p.shown = k, searchKindPage
		if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil {
			p.shown = min(max(n, searchKindPage), searchCountCap)
		}
	}
	if !v.Searched {
		return p
	}
	person := personOf(r)
	for _, line := range lines {
		sl, problem := a.parseSearchLine(line, pa, guest, project, person, p.now)
		if problem != nil {
			// One line that does not run stops the whole search, the lines before it too.
			v.Notice, v.NoticeKind = l.msg(problem.Key, problem.Args...), "err"
			v.Searched = false
			p.lines, p.raw = nil, nil
			break
		}
		if !sl.qualified && utf8.RuneCountInString(sl.text) < minSearchRunes {
			continue
		}
		sl.shown = p.shown
		if un := sl.unsupportedKinds(); len(un) > 0 && v.Notice == "" {
			v.Notice, v.NoticeKind = l.msg("search.err.kinds", searchKindWords(l, un)), "note"
		}
		p.lines = append(p.lines, sl)
		p.raw = append(p.raw, line)
	}
	return p
}

// runSearch searches every kind of the plan at the same time and returns the
// groups with hits in the order of the page.
func (a *app) runSearch(ctx context.Context, r *http.Request, p *searchPlan) []searchGroup {
	out := make([]searchGroup, len(p.kinds))
	var wg sync.WaitGroup
	for i, key := range p.kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = a.searchKind(ctx, r, p, key)
		}()
	}
	wg.Wait()
	var groups []searchGroup
	for _, g := range out {
		if len(g.Hits) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}

// searchKind searches one kind over every line of the plan and joins the hits.
// It asks with its own access check: a ProjectAccess is not shared between the
// kinds that run at the same time.
//
// The kind waits for a free place among the searches of the server (searchSlots),
// reads through a view of the store that ends with ctx, and gives back an empty
// group, logged, when it panics: one kind does not take the server down.
func (a *app) searchKind(ctx context.Context, r *http.Request, p *searchPlan, key string) (out searchGroup) {
	defer func() {
		if v := recover(); v != nil {
			a.log().Error("search_panic", "kind", key, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			out = searchGroup{Key: key}
		}
	}()
	release, ok := a.searchSlots.acquire(ctx, a.store)
	if !ok {
		return searchGroup{Key: key}
	}
	defer release()
	st := a.store.WithContext(ctx)
	pa := st.Access(browserPrincipal(r))
	var merged *searchGroup
	for i, sl := range p.lines {
		if ctx.Err() != nil {
			return searchGroup{Key: key}
		}
		g := a.searchOne(ctx, p.l, st, pa, sl, p.raw[i], p.now, key)
		if len(g.Hits) == 0 {
			continue
		}
		if merged == nil {
			g.Mark, g.Href = searchKindMark[g.Key], p.href(g.Key)
			merged = &g
			continue
		}
		for _, h := range g.Hits {
			if !hasHitHref(merged.Hits, h.Href) {
				merged.Hits = append(merged.Hits, h)
			}
		}
	}
	if merged == nil || ctx.Err() != nil {
		return searchGroup{Key: key}
	}
	if len(p.lines) > 1 {
		// Lines of a quick filter overlap: the union is counted from its hits.
		merged.Total = 0
	}
	p.sizeGroup(merged)
	return *merged
}

// sizeGroup turns the hits a kind found into what the page shows of it: the
// total and its labels, the window of hits, and the link to more of them.
// The kinds return every hit they counted (at most searchCountCap, one more
// to tell that there are more); Total is set by a kind only when it counted
// exactly with a COUNT, and then it stands.
func (p *searchPlan) sizeGroup(g *searchGroup) {
	limit := searchCountCap
	if g.Key == "sessions" {
		limit = max(searchSessionCount, p.shown)
	}
	n := len(g.Hits)
	switch {
	case g.Total > n:
		g.Exact = true
	default:
		g.Total, g.Capped = min(n, limit), n > limit
	}
	g.Count = strconv.Itoa(g.Total)
	g.TotalLabel = p.l.msg("search.total", g.Total)
	if g.Capped {
		g.Count += "+"
		g.TotalLabel = p.l.msg("search.total_capped", g.Total)
	}
	if len(g.Hits) > p.shown {
		g.Hits = g.Hits[:p.shown]
	}
	if g.Total > len(g.Hits) {
		g.More = p.href(g.Key)
		if p.kind == g.Key {
			g.More += "&n=" + strconv.Itoa(min(p.shown+searchKindPage, searchCountCap))
		}
	}
}

// allCount is the number of hits of all groups as the facet "All" shows it.
func allCount(groups []searchGroup) string {
	total, capped := 0, false
	for _, g := range groups {
		total += g.Total
		capped = capped || g.Capped
	}
	if capped {
		return strconv.Itoa(total) + "+"
	}
	return strconv.Itoa(total)
}

func (a *app) searchOne(ctx context.Context, l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time, key string) searchGroup {
	g := a.searchOneKind(ctx, l, st, pa, sl, line, now, key)
	markHits(&g, sl.text)
	return g
}

func (a *app) searchOneKind(ctx context.Context, l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time, key string) searchGroup {
	switch key {
	case "requests":
		return a.searchRequests(l, st, pa, sl, line, now)
	case "ideas":
		return a.searchIdeas(l, st, pa, sl, line, now)
	case "knowledge":
		return a.searchKnowledge(l, st, pa, sl, line, now)
	case "docs":
		return a.searchDocs(l, st, pa, sl, line, now)
	case "sessions":
		return a.searchSessions(ctx, l, st, pa, sl, line, now)
	case "tasks":
		return a.searchTasks(l, st, pa, sl, now)
	case "pains":
		return a.searchPains(l, st, pa, sl, now)
	}
	return searchGroup{Key: key}
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
func factsFor(st *store.Store, kind string, ids []int64) map[int64]store.ObjectFacts {
	facts, err := st.ObjectFacts(kind, ids)
	if err != nil {
		return map[int64]store.ObjectFacts{}
	}
	return facts
}

func (a *app) searchRequests(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "requests", Label: l.msg("nav.requests"), Icon: "requests"}
	if !sl.wants(query.KindRequest) {
		return g
	}
	project := sl.project
	if project != "" && pa.GateList(project, store.ResRequest, true) != nil {
		project = "\x00hidden"
	}
	filter := requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Query: sl.text,
		Commit: sl.anchor.Commit, PR: sl.anchor.PR, Origin: sl.origin}
	// Without qualifiers only the window is read, and the total is one COUNT on
	// the same filter (rights are part of it); with qualifiers the hits that
	// survive them are the count, so every candidate is read, page after page.
	want := sl.shown + 1
	if sl.qualified {
		want = sl.fetch
	} else if n, err := st.CountRequestMatches(pa.RequestFilter(filter)); err == nil {
		g.Total = n
	}
	hits, err := requestPages(st, pa, filter, want)
	if err != nil {
		return g
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.Request.ID
	}
	facts := factsFor(st, "request", ids)
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
	for _, raw := range kept {
		h := pa.RequestHitView(raw)
		o := originOf(l, now, h.Request.CreatedAt, facts[raw.Request.ID])
		g.Hits = append(g.Hits, searchHit{Title: h.Request.Title, Sub: requestStateLabel(l, h.Request.State), Project: lastSegment(h.Request.Scope.Project), RefKind: "request", RefN: h.Request.ID,
			Href: requestURL(h.Request.ID), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchIdeas(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
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
		facts := factsFor(st, "idea", ids)
		for _, it := range list {
			if sl.qualified && !sl.q.Match(factsOf(pa, query.KindIdea, it.ID, it.State, "", it.Project, it.Person, "", it.CreatedAt, it.UpdatedAt, facts[it.ID])) {
				continue
			}
			all = append(all, found{it, facts[it.ID]})
		}
	}
	for _, f := range all {
		o := originOf(l, now, f.idea.CreatedAt, f.of)
		g.Hits = append(g.Hits, searchHit{Title: f.idea.Title, Sub: label(l, ideaStateKeys, f.idea.State), Project: lastSegment(f.idea.Project), RefKind: "idea", RefN: f.idea.ID,
			Href: repoHref(f.idea.Project) + "/ideas/" + strconv.FormatInt(f.idea.ID, 10), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchKnowledge(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "knowledge", Label: l.msg("nav.knowledge"), Icon: "knowledge"}
	project := sl.project
	if !sl.wants(query.KindKnowledge) || (project != "" && pa.GateList(project, store.ResKnowledge, true) != nil) {
		return g
	}
	readable := func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && (k.Status == "active" || k.Status == "stale")
	}
	entries, err := st.SearchKnowledgeWith(sl.text, store.KnowledgeSearch{
		Axes: scope.Axes{Project: project}, Operator: true, Anchor: sl.anchor, Origin: sl.origin,
		Limit: a.overfetch(sl.fetch), Viewer: pa.Viewer(), Keep: readable, Prefilter: pa.KnowledgePrefilter(),
		// Die Vorschläge des Gärtners gehören in die Prüfung; in der Suche
		// erscheinen sie nur, wenn die Zeile nach ihnen fragt.
		NoGardener: !(sl.qualified && sl.q.WantsUnsettled()),
	})
	if err != nil {
		return g
	}
	entries = keep(entries, 0, readable)
	ids := make([]int64, len(entries))
	for i, k := range entries {
		ids[i] = k.ID
	}
	facts := factsFor(st, "knowledge", ids)
	if sl.qualified {
		entries = keep(entries, 0, func(k store.Knowledge) bool {
			return sl.q.Match(factsOf(pa, query.KindKnowledge, k.ID, k.Status, k.Type, k.Scope.Project, k.Person, "", k.CreatedAt, k.UpdatedAt, facts[k.ID]).WithKnowledge(k.Confidence, k.Harness))
		})
	}
	for _, k := range entries {
		row := a.knowledgeRowFor(l, pa, k, true)
		o := originOf(l, now, k.CreatedAt, facts[k.ID])
		g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: row.TypeLabel, Project: row.Project, RefKind: "knowledge", RefN: k.ID, Href: row.Href, Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

func (a *app) searchSessions(ctx context.Context, l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, line string, now time.Time) searchGroup {
	g := searchGroup{Key: "sessions", Label: l.msg("nav.sessions"), Icon: "sessions"}
	// A transcript is found by its words, not listed: without a word there is no hit.
	if !sl.wants(query.KindSession) || strings.TrimSpace(sl.text) == "" {
		return g
	}
	filter := store.SessionFilter{Project: sl.project}
	limit := max(searchSessionCount, sl.shown) + 1
	if sl.qualified {
		limit = min(sl.fetch, 60)
	}
	page, err := st.SearchTranscripts(pa, store.SearchQuery{Q: sl.text, Filter: filter, Sort: "best", Limit: limit, Ctx: ctx})
	if err != nil {
		return g
	}
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

func (a *app) searchTasks(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, now time.Time) searchGroup {
	g := searchGroup{Key: "tasks", Label: l.msg("nav.tab_tasks"), Icon: "action"}
	if !sl.wants(query.KindTask) {
		return g
	}
	words := strings.Fields(strings.ToLower(sl.text))
	for _, p := range visibleProjects(pa, sl.project) {
		// ListHumanTasks is the visibility check; the text match runs on what it lets through.
		tasks, err := st.ListHumanTasks(pa, store.TaskFilter{Project: p, State: "all", Limit: tasksShown})
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
		facts := factsFor(st, "task", ids)
		readable := a.readableParents(pa, sl)
		for _, t := range matched {
			of := facts[t.ID]
			of.Parents, of.Blocks = readable(of.Parents), readable(of.Blocks)
			facts[t.ID] = of
			if sl.qualified && !sl.q.Match(factsOf(pa, query.KindTask, t.ID, t.State, t.Kind, p, t.CreatedBy, t.Assignee, t.CreatedAt, t.UpdatedAt, of)) {
				continue
			}
			sub := ""
			if t.State != humantask.Open {
				sub = label(l, taskStateKeys, t.State)
			}
			o := originOf(l, now, t.CreatedAt, of)
			g.Hits = append(g.Hits, searchHit{Title: t.Title, Sub: sub, Project: lastSegment(t.Project), RefKind: "task", RefN: t.ID, Href: taskHref(t), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
		}
	}
	return g
}

func (a *app) searchPains(l lang, st *store.Store, pa *store.ProjectAccess, sl searchLine, now time.Time) searchGroup {
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
	facts := factsFor(st, "pain", ids)
	for _, row := range list {
		if sl.qualified && !sl.q.Match(factsOf(pa, query.KindPain, row.ID, row.State, "", row.Project, row.Person, "", row.CreatedAt, row.UpdatedAt, facts[row.ID])) {
			continue
		}
		o := originOf(l, now, row.CreatedAt, facts[row.ID])
		g.Hits = append(g.Hits, searchHit{Title: row.Title, Sub: label(l, painStateKeys, row.State), Project: lastSegment(row.Project), RefKind: "pain", RefN: row.ID, Href: painURL(row.ID), Date: o.Date, Stamp: o.Stamp, Area: o.Area})
	}
	return g
}

// searchKindWords names the kinds the page does not show, in the reader's language.
var searchKindWordKeys = map[string]string{"feature": "search.kind_word.feature", "criterion": "search.kind_word.criterion", "file": "search.kind_word.file", "message": "search.kind_word.message"}

func searchKindWords(l lang, kinds []string) string {
	words := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if key, ok := searchKindWordKeys[k]; ok {
			k = l.msg(key)
		}
		words = append(words, k)
	}
	return strings.Join(words, ", ")
}

// markHits writes the title and the snippet of each hit with the searched words
// marked, and says whether the hits come from more than one repository.
func markHits(g *searchGroup, text string) {
	var words []string
	for _, w := range strings.Fields(text) {
		if utf8.RuneCountInString(w) >= 2 {
			words = append(words, w)
		}
	}
	for i := range g.Hits {
		h := &g.Hits[i]
		h.Marked = markWords(h.Title, words)
		if h.Snip != "" {
			h.SnipHTML = markWords(h.Snip, words)
		}
		if h.Project != g.Hits[0].Project {
			g.MultiProject = true
		}
	}
	for i := range g.Hits {
		h := &g.Hits[i]
		h.Lead = h.Sub
		if g.MultiProject && h.Project != "" {
			h.Lead = h.Project
			if h.Sub != "" {
				h.Lead += " · " + h.Sub
			}
		}
	}
}

// markWords is a text with every occurrence of the words (any case) in <mark>;
// the text itself is escaped.
func markWords(text string, words []string) template.HTML {
	if len(words) == 0 {
		return template.HTML(html.EscapeString(text))
	}
	low := strings.ToLower(text)
	if len(low) != len(text) { // a case fold that changes the length: skip the marks, keep the text
		return template.HTML(html.EscapeString(text))
	}
	mark := make([]bool, len(text))
	for _, w := range words {
		w = strings.ToLower(w)
		for from := 0; ; {
			i := strings.Index(low[from:], w)
			if i < 0 {
				break
			}
			for j := from + i; j < from+i+len(w); j++ {
				mark[j] = true
			}
			from += i + len(w)
		}
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && mark[j] == mark[i] {
			j++
		}
		// do not cut a multi-byte rune: extend j to a rune boundary
		for j < len(text) && !utf8.RuneStart(text[j]) {
			j++
		}
		seg := html.EscapeString(text[i:j])
		if mark[i] {
			seg = "<mark>" + seg + "</mark>"
		}
		b.WriteString(seg)
		i = j
	}
	return template.HTML(b.String())
}

// requestPages reads the requests of a search page by page (a page holds at
// most 25) until want are read or the search ends, and says to the log mode
// what the rights would have hidden.
func requestPages(st *store.Store, pa *store.ProjectAccess, filter requestdomain.SearchFilter, want int) ([]requestdomain.SearchHit, error) {
	var hits []requestdomain.SearchHit
	cursor := ""
	for len(hits) < want {
		f := filter
		// The size of a page is the same for every page: a ranked search fuses
		// differently for another size, and its pages would no longer add up.
		f.Limit, f.Cursor = requestPageSize, cursor
		page, err := st.SearchRequestsWith(pa.RequestFilter(f), store.SearchAuto)
		if err != nil {
			return nil, err
		}
		pa.NoteRequestHits(page.Results)
		hits = append(hits, page.Results...)
		if page.NextCursor == "" || len(page.Results) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	return hits[:min(len(hits), want)], nil
}

// requestPageSize is the largest page a request search gives.
const requestPageSize = 25
