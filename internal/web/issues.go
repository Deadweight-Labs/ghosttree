package web

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The issues tab of a repository (REQ-599, decision #4315): one list for the
// issues (the requests), the ideas and the pains, told apart by a segment at the
// top. The search text is the filter. This file maps it onto the filters the
// backend has today, in one place (parseIssueQuery); the text itself is never
// rewritten, so it can go to the query language of REQ-600 (internal/query)
// unchanged once that is in. What the backend cannot do yet is shown as
// unsupported, not dropped silently.

const (
	segIssue = "issue"
	segIdea  = "idea"
	segPain  = "pain"

	issuesClosedShown = 50
)

var (
	qualifierKey = regexp.MustCompile(`^[a-z][a-z-]*$`)
	hexCommit    = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// issueQuery is the search text as today's filters.
type issueQuery struct {
	Segment  string // issue, idea or pain
	State    string // open, closed (done and dropped), done, dropped or all
	Type     string
	Branch   string
	Priority string
	Commit   string
	Label    string
	Author   string
	// Status is the state of a pain or an idea as a filter of its own.
	Status string
	PR     int64
	// Text is what is left over for the free-text search.
	Text string
	// Unsupported are the qualifiers the backend has no filter for yet.
	Unsupported []string
	// tokens keeps the words of the qualifiers that were understood, by key.
	tokens map[string]string
}

// parseIssueQuery reads the qualifiers is:, type:, priority:, branch:, pr: and
// commit:. Every other word:value is reported in Unsupported and is not part of
// the free text.
func parseIssueQuery(q string) issueQuery {
	iq := issueQuery{Segment: segIssue, State: "open", tokens: map[string]string{}}
	var text []string
	for _, tok := range strings.Fields(q) {
		key, val, ok := strings.Cut(tok, ":")
		key = strings.ToLower(key)
		if !ok || val == "" || !qualifierKey.MatchString(key) || strings.HasPrefix(val, "//") {
			text = append(text, tok)
			continue
		}
		valid := true
		switch key {
		case "is":
			switch v := strings.ToLower(val); v {
			case "open", "closed", "done", "dropped", "all", "accepted", "dismissed":
				iq.State = v
			case "any":
				iq.State = "all"
			case segIssue, segIdea, segPain:
				iq.Segment = v
			default:
				valid = false
			}
		case "type":
			valid = slices.Contains(requestTypes, strings.ToLower(val))
			if valid {
				iq.Type = strings.ToLower(val)
			}
		case "priority":
			iq.Priority = val
		case "label":
			iq.Label = val
		case "author":
			iq.Author = val
		case "status":
			iq.Status = strings.ToLower(val)
		case "branch":
			iq.Branch = val
		case "commit":
			valid = hexCommit.MatchString(val)
			iq.Commit = val
		case "pr":
			n, err := strconv.ParseInt(strings.TrimPrefix(val, "#"), 10, 64)
			valid = err == nil && n > 0
			iq.PR = n
		default:
			valid = false
		}
		if !valid {
			iq.Unsupported = append(iq.Unsupported, tok)
		} else {
			iq.tokens[key] = tok
		}
	}
	iq.Text = strings.Join(text, " ")
	return iq
}

// withQualifier returns the search text with every word that drop accepts
// removed and add (if any) appended. The dropdowns and segments are links made
// with it, so they write into the same text the field shows.
func withQualifier(q string, drop func(key, val string) bool, add string) string {
	var out []string
	for _, tok := range strings.Fields(q) {
		key, val, _ := strings.Cut(tok, ":")
		if drop(strings.ToLower(key), strings.ToLower(val)) {
			continue
		}
		out = append(out, tok)
	}
	if add != "" {
		out = append(out, add)
	}
	return strings.Join(out, " ")
}

func dropKey(k string) func(key, val string) bool {
	return func(key, _ string) bool { return key == k }
}

func dropIs(group ...string) func(key, val string) bool {
	return func(key, val string) bool { return key == "is" && slices.Contains(group, val) }
}

var (
	isSegments = []string{segIssue, segIdea, segPain}
	isStates   = []string{"open", "closed", "done", "dropped", "all", "any", "accepted", "dismissed"}
)

type issueSegment struct {
	Label, Href, Count string
	Current            bool
	Kind               string
}

type issueMenuItem struct {
	Label, Href string
	Current     bool
}

type issueMenu struct {
	Label  string
	Active bool
	Items  []issueMenuItem
}

type issueRow struct {
	// Actor is who wrote it, when an agent did (REQ-620); a person stays Person.
	Actor                   actorView
	Kind, State, StateLabel string
	N                       int64
	Href, Title             string
	TypeLabel, Person, Age  string
	Done, Total, Percent    int
	Progress                string
	// Waiting is who an open human task waits for ("" = no such task).
	Waiting, WaitingTitle string
	// Icon and Tone draw the state (open, closed, pain, drop), Branch is the
	// branch of the issue, Opened the line "opened 3 d ago by Robin".
	Icon, Tone, Branch, Opened string
	// Live is the agent that works on the issue right now (live.go).
	Live liveAgent
	// Labels are the pills after the title; Excerpt, ExLead, Pill and PillTone
	// are the extra line and the state word of an idea or a pain.
	Labels                          []rowLabel
	Excerpt, ExLead, Pill, PillTone string
}

// issueStateTab is one tab of the list head: "9 open", "5 closed".
type issueStateTab struct {
	Icon, Label, Count, Href string
	On                       bool
}

// presetLink is a saved search in the column left of the list.
type presetLink struct{ Name, Href, Query string }

// newItem is one entry of the "New" menu.
type newItem struct{ Label, Href string }

type issuesView struct {
	Q, Base, Segment string
	Segments         []issueSegment
	Tabs             []issueStateTab
	Menus            []issueMenu
	Unsupported      []string
	Rows             []issueRow
	Older            string
	Empty, NoMatch   bool
	Guest            bool
	EmptyKey         string
	ClearHref        string
	// LiveIDs are the issues of the page, for the live stream.
	LiveIDs string
	// Presets are the saved searches (issues only); CanSave: a form to keep the
	// text of the search field.
	Presets []presetLink
	CanSave bool
	SaveQ   string
	Scopes  []saveScope
	Token   string
	// New is the button above the list, the others its menu.
	New      newItem
	NewMenu  []newItem
	HasAside bool
}

// issuesPage is /ui/{org}/{repo}/issues.
func (a *app) issuesPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	view := repoFrom(r)
	project := view.Project
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	if len(q) > 500 {
		q = q[:500]
	}
	iq := parseIssueQuery(q)
	pa := a.access(r)
	if _, stop := a.gateList(w, r, pa, project, store.ResRequest); stop {
		return
	}
	if iq.Branch != "" && !a.seesAllBranches(pa, project) {
		// A guest has no branch filter: it would tell which branches have a work package.
		iq.Unsupported, iq.Branch = append(iq.Unsupported, "branch:"+iq.Branch), ""
	}
	v := &issuesView{Q: q, Base: view.Href + "/issues", Segment: iq.Segment, Guest: a.shellBaseFor(r).who.kind == viewerGuest, Unsupported: iq.Unsupported, Token: csrfOf(r)}
	href := func(text string) string {
		if text = strings.TrimSpace(text); text == "" {
			return v.Base
		}
		return v.Base + "?q=" + url.QueryEscape(text)
	}
	v.ClearHref = href("is:" + iq.Segment + " is:open")

	var err error
	switch iq.Segment {
	case segPain:
		err = a.issuePainRows(l, pa, project, iq, v)
	case segIdea:
		err = a.issueIdeaRows(l, pa, project, iq, v)
	default:
		err = a.issueRequestRows(r, pa, project, iq, query.Get("cursor"), q, v)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	counts := a.issueSegmentCounts(pa, project)
	for _, s := range []struct{ kind, label string }{{segIssue, "issues.seg.issues"}, {segIdea, "issues.seg.ideas"}, {segPain, "issues.seg.pains"}} {
		next := withQualifier(q, dropIs(isSegments...), "is:"+s.kind)
		if next == "" {
			next = "is:" + s.kind
		}
		v.Segments = append(v.Segments, issueSegment{Kind: s.kind, Label: s.label, Count: strconv.Itoa(counts[s.kind]), Href: href(withQualifier(next, dropIs(isStates...), "is:open")), Current: s.kind == iq.Segment})
	}
	tab := func(icon, label, count, state string, on bool) {
		v.Tabs = append(v.Tabs, issueStateTab{Icon: icon, Label: label, Count: count, Href: href(withQualifier(q, dropIs(isStates...), "is:"+state)), On: on})
	}
	closed := iq.State == "closed" || iq.State == "done" || iq.State == "dropped"
	rest := a.otherStateCounts(pa, project)
	switch iq.Segment {
	case segIdea:
		tab("idea", "issues.tab.open", strconv.Itoa(counts[segIdea]), "open", iq.State == "open")
		tab("issue", "issues.tab.accepted", rest["accepted"], "accepted", iq.State == "accepted")
		tab("drop", "issues.tab.dismissed", rest["dismissed"], "dismissed", iq.State == "dismissed")
	case segPain:
		tab("pain", "issues.tab.open", strconv.Itoa(counts[segPain]), "open", iq.State == "open")
		tab("done", "issues.tab.done", rest["pains"], "closed", closed)
	default:
		tab("issue", "issues.tab.open", strconv.Itoa(counts[segIssue]), "open", iq.State == "open")
		tab("done", "issues.tab.closed", rest["issues"], "closed", closed)
	}
	v.Menus = a.issueMenus(l, pa, project, q, iq, href, v.Rows)
	v.HasAside = iq.Segment == segIssue
	if v.HasAside {
		if presets, err := pa.ListPresets(project); err == nil {
			for _, p := range presets {
				if len(p.Queries) > 0 {
					v.Presets = append(v.Presets, presetLink{Name: p.Name, Query: p.Queries[0], Href: href(p.Queries[0])})
				}
			}
		}
		v.CanSave = !v.Guest && interactive(r)
		if v.CanSave {
			v.Scopes = a.saveScopes(l, r, pa, project)
		}
		v.SaveQ = q
		if v.SaveQ == "" {
			v.SaveQ = "is:issue is:open"
		}
		// A box of saved searches with neither a search nor a way to save one has nothing to show.
		v.HasAside = len(v.Presets) > 0 || v.CanSave
	}
	v.New, v.NewMenu = issueNewMenu(v.Base, iq.Segment)
	a.rowActors(l, v.Rows)
	a.renderBrowser(w, r, "issues", pageData{Title: l.msg("repo.tab.issues"), Project: project, IssuesV: v})
}

// issueNewMenu is the button for the segment on show and the other two in the
// menu behind it: "New issue", "New idea", "New pain".
func issueNewMenu(base, segment string) (newItem, []newItem) {
	all := []struct{ seg, label, href string }{
		{segIssue, "issues.new_issue", base + "/new"},
		{segIdea, "issues.new_idea", base + "/new?kind=idea"},
		{segPain, "issues.new_pain", base + "/new?kind=pain"},
	}
	var main newItem
	var menu []newItem
	for _, n := range all {
		it := newItem{Label: n.label, Href: n.href}
		if n.seg == segment {
			main = it
		}
		menu = append(menu, it)
	}
	return main, menu
}

// issueMenus are the filter dropdowns: each entry is a link that sets one
// qualifier in the search text. A pain has a status menu, every segment label
// and author.
func (a *app) issueMenus(l lang, pa *store.ProjectAccess, project, q string, iq issueQuery, href func(string) string, rows []issueRow) []issueMenu {
	var menus []issueMenu
	switch iq.Segment {
	case segIssue:
		typeMenu := issueMenu{Label: l.msg("issues.filter.type"), Active: iq.Type != ""}
		typeMenu.Items = append(typeMenu.Items, issueMenuItem{Label: l.msg("requests.all_types"), Href: href(withQualifier(q, dropKey("type"), "")), Current: iq.Type == ""})
		for _, t := range requestTypes {
			typeMenu.Items = append(typeMenu.Items, issueMenuItem{Label: requestTypeLabel(l, t), Href: href(withQualifier(q, dropKey("type"), "type:"+t)), Current: iq.Type == t})
		}
		menus = append(menus, typeMenu)
		known, err := a.store.RequestPriorities(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}}))
		if err == nil {
			known = slices.DeleteFunc(known, func(p string) bool { return p == "" || strings.ContainsAny(p, " \t:") })
			if len(known) > 0 {
				slices.Sort(known)
				m := issueMenu{Label: l.msg("issues.filter.priority"), Active: iq.Priority != ""}
				m.Items = append(m.Items, issueMenuItem{Label: l.msg("requests.all_priorities"), Href: href(withQualifier(q, dropKey("priority"), "")), Current: iq.Priority == ""})
				for _, p := range known {
					m.Items = append(m.Items, issueMenuItem{Label: p, Href: href(withQualifier(q, dropKey("priority"), "priority:"+p)), Current: iq.Priority == p})
				}
				menus = append(menus, m)
			}
		}
	case segPain:
		m := issueMenu{Label: l.msg("issues.filter.status"), Active: iq.Status != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg("issues.filter.any_status"), Href: href(withQualifier(q, dropKey("status"), "")), Current: iq.Status == ""})
		for _, st := range pain.States {
			m.Items = append(m.Items, issueMenuItem{Label: label(l, painStateKeys, st), Href: href(withQualifier(q, dropKey("status"), "status:"+st)), Current: iq.Status == st})
		}
		menus = append(menus, m)
	}
	if labels, err := pa.ListLabels(project); err == nil && len(labels) > 0 {
		m := issueMenu{Label: l.msg("issues.filter.label"), Active: iq.Label != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg("issues.filter.any_label"), Href: href(withQualifier(q, dropKey("label"), "")), Current: iq.Label == ""})
		for _, lb := range labels {
			m.Items = append(m.Items, issueMenuItem{Label: lb.Name, Href: href(withQualifier(q, dropKey("label"), "label:"+lb.Name)), Current: strings.EqualFold(iq.Label, lb.Name)})
		}
		menus = append(menus, m)
	}
	var people []string
	for _, r := range rows {
		if r.Person != "" && !slices.Contains(people, r.Person) && !strings.ContainsAny(r.Person, " \t:") {
			people = append(people, r.Person)
		}
	}
	if len(people) > 0 || iq.Author != "" {
		slices.Sort(people)
		m := issueMenu{Label: l.msg("issues.filter.author"), Active: iq.Author != ""}
		m.Items = append(m.Items, issueMenuItem{Label: l.msg("issues.filter.any_author"), Href: href(withQualifier(q, dropKey("author"), "")), Current: iq.Author == ""})
		for _, p := range people {
			m.Items = append(m.Items, issueMenuItem{Label: p, Href: href(withQualifier(q, dropKey("author"), "author:"+p)), Current: strings.EqualFold(iq.Author, p)})
		}
		menus = append(menus, m)
	}
	return menus
}

// otherStateCounts are the numbers beside the tabs that are not "open": accepted and
// dismissed ideas, finished pains, closed issues. Each is what the list behind the
// tab would show, up to repoCountAt.
func (a *app) otherStateCounts(pa *store.ProjectAccess, project string) map[string]string {
	out := map[string]string{}
	for _, st := range []string{idea.Accepted, idea.Dismissed} {
		if list, err := pa.SearchIdeas(idea.Filter{Project: project, State: st, Limit: repoCountAt}); err == nil {
			out[st] = countText(len(list))
		}
	}
	if list, err := pa.SearchPains(pain.Filter{Project: project, State: "all", Limit: 500}); err == nil {
		n := 0
		for _, p := range list {
			if !painOpen(p.State) {
				n++
			}
		}
		out["pains"] = countText(n)
	}
	closed := 0
	for _, st := range []string{"done", "dropped"} {
		if page, err := a.store.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, State: st, Limit: repoCountAt}), store.SearchAuto); err == nil {
			closed += len(page.Results)
		}
	}
	out["issues"] = countText(closed)
	return out
}

// issueSegmentCounts are the open entries of each segment.
func (a *app) issueSegmentCounts(pa *store.ProjectAccess, project string) map[string]int {
	out := map[string]int{}
	out[segIssue], out[segPain] = a.issueCounts(pa, project)
	if list, err := pa.SearchIdeas(idea.Filter{Project: project, Limit: 500}); err == nil {
		out[segIdea] = len(list)
	}
	return out
}

func (a *app) issueCounts(pa *store.ProjectAccess, project string) (issues, pains int) {
	issues = a.openIssueCount(pa, project)
	if list, err := pa.SearchPains(pain.Filter{Project: project, Limit: 500}); err == nil {
		for _, p := range list {
			if painOpen(p.State) {
				pains++
			}
		}
	}
	return issues, pains
}

func painOpen(state string) bool { return state == "active" || state == "mitigated" }

// firstLineCut is the first line of a text, cut to n runes.
func firstLineCut(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// hasLabel says whether the named label is among the labels of an object.
func hasLabel(labels []store.Label, name string) bool {
	return name == "" || slices.ContainsFunc(labels, func(l store.Label) bool { return strings.EqualFold(l.Name, name) })
}

func (a *app) issuePainRows(l lang, pa *store.ProjectAccess, project string, iq issueQuery, v *issuesView) error {
	// No filter but state, label, author and text applies to pains yet.
	for _, k := range []string{"type", "priority", "branch", "commit", "pr"} {
		if tok, ok := iq.tokens[k]; ok {
			v.Unsupported = append(v.Unsupported, tok)
		}
	}
	list, err := pa.SearchPains(pain.Filter{Project: project, State: "all", Query: iq.Text, Limit: 200})
	if err != nil {
		return err
	}
	ids := make([]int64, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	labels, _ := a.store.LabelsFor("pain", ids)
	now := overviewNow().UTC()
	base := strings.TrimSuffix(v.Base, "/issues")
	for _, p := range list {
		open := painOpen(p.State)
		switch iq.State {
		case "open":
			if !open {
				continue
			}
		case "closed", "done", "dropped":
			if open {
				continue
			}
		}
		if iq.Status != "" && iq.Status != p.State || !hasLabel(labels[p.ID], iq.Label) || iq.Author != "" && !strings.EqualFold(iq.Author, p.Person) {
			continue
		}
		row := issueRow{Kind: "pain", N: p.ID, Href: base + "/pains/" + strconv.FormatInt(p.ID, 10), Title: p.Title, State: p.State, StateLabel: label(l, painStateKeys, p.State),
			Person: p.Person, Age: shortAge(l, now, parseTime(p.UpdatedAt)), Icon: "pain", Tone: "pain", Labels: rowLabels(labels[p.ID], v.Base)}
		switch p.State {
		case pain.Mitigated:
			row.Tone, row.Pill, row.PillTone = "warn", row.StateLabel, "p-warn"
			if p.Remaining != "" {
				row.ExLead, row.Excerpt = l.msg("pains.stays_open"), firstLineCut(p.Remaining, 160)
			}
		case pain.Accepted:
			row.Tone, row.Pill, row.PillTone = "drop", row.StateLabel, "p-mute"
			if p.AcceptedReason != "" {
				row.ExLead, row.Excerpt = l.msg("pains.why"), firstLineCut(p.AcceptedReason, 160)
			}
		case pain.Resolved:
			row.Tone, row.Pill, row.PillTone = "closed", row.StateLabel, "p-done"
		case pain.Obsolete:
			row.Tone, row.Pill, row.PillTone = "drop", row.StateLabel, "p-mute"
		}
		v.Rows = append(v.Rows, row)
	}
	v.Empty = len(v.Rows) == 0 && iq.Text == "" && iq.State == "open" && len(v.Unsupported) == 0 && iq.Label == "" && iq.Author == "" && iq.Status == ""
	v.NoMatch = len(v.Rows) == 0 && !v.Empty
	v.EmptyKey = "pains.empty"
	return nil
}

// ideaStateKeys name the state of an idea as the list shows it.
var ideaStateKeys = map[string]string{idea.Open: "ideas.state.open", idea.Accepted: "ideas.state.accepted", idea.Dismissed: "ideas.state.dismissed"}

func (a *app) issueIdeaRows(l lang, pa *store.ProjectAccess, project string, iq issueQuery, v *issuesView) error {
	for _, k := range []string{"type", "priority", "branch", "commit", "pr", "status"} {
		if tok, ok := iq.tokens[k]; ok {
			v.Unsupported = append(v.Unsupported, tok)
		}
	}
	state := ""
	switch iq.State {
	case "accepted", "dismissed":
		state = iq.State
	case "all":
		state = "all"
	case "closed", "done", "dropped":
		state = "all"
	}
	list, err := pa.SearchIdeas(idea.Filter{Project: project, State: state, Query: iq.Text, Limit: 200})
	if err != nil {
		return err
	}
	now := overviewNow().UTC()
	base := strings.TrimSuffix(v.Base, "/issues")
	for _, i := range list {
		if iq.State == "closed" && i.State == idea.Open {
			continue
		}
		var labels []store.Label
		for _, lb := range i.Labels {
			labels = append(labels, store.Label{Name: lb.Name, Color: lb.Color})
		}
		if !hasLabel(labels, iq.Label) || iq.Author != "" && !strings.EqualFold(iq.Author, i.Person) {
			continue
		}
		row := issueRow{Kind: "idea", N: i.ID, Href: base + "/ideas/" + strconv.FormatInt(i.ID, 10), Title: i.Title, State: i.State, StateLabel: label(l, ideaStateKeys, i.State),
			Person: i.Person, Age: shortAge(l, now, parseTime(i.UpdatedAt)), Icon: "idea", Tone: "idea", Labels: rowLabels(labels, v.Base), Excerpt: firstLineCut(i.Body, 160)}
		switch i.State {
		case idea.Open:
			row.Pill, row.PillTone = row.StateLabel, "p-ok"
		case idea.Accepted:
			row.Pill, row.PillTone, row.Tone = row.StateLabel, "p-done", "closed"
		case idea.Dismissed:
			row.Pill, row.PillTone, row.Tone = row.StateLabel, "p-mute", "drop"
		}
		v.Rows = append(v.Rows, row)
	}
	v.Empty = len(v.Rows) == 0 && iq.Text == "" && iq.State == "open" && len(v.Unsupported) == 0 && iq.Label == "" && iq.Author == ""
	v.NoMatch = len(v.Rows) == 0 && !v.Empty
	v.EmptyKey = "issues.ideas.empty"
	return nil
}

func (a *app) issueRequestRows(r *http.Request, pa *store.ProjectAccess, project string, iq issueQuery, cursor, q string, v *issuesView) error {
	l := a.langFor(r)
	filter := requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Query: iq.Text, Type: iq.Type, Priority: iq.Priority, PR: iq.PR, Limit: requestsShown, Cursor: cursor}
	if iq.Commit != "" {
		if !hexCommit.MatchString(iq.Commit) {
			iq.Commit = ""
		}
		filter.Commit = iq.Commit
	}
	if !store.ValidRequestCursor(filter.Cursor) {
		filter.Cursor = ""
	}
	// The members of the open work package of the branch: the branch filter of
	// today. A branch without a package has no issues.
	var members map[int64]bool
	if iq.Branch != "" {
		members = map[int64]bool{}
		if d, err := a.store.PackageForBranch(project, iq.Branch, nil); err == nil {
			for _, m := range d.Members {
				members[m.RequestID] = true
			}
		}
		filter.Limit, filter.Cursor = 200, ""
	}
	var hits []requestdomain.SearchHit
	var next string
	states := []string{iq.State}
	switch iq.State {
	case "all":
		states = []string{""}
	case "closed":
		states = []string{"done", "dropped"}
		filter.Limit, filter.Cursor = issuesClosedShown, ""
	}
	for _, st := range states {
		f := filter
		f.State = st
		page, err := a.store.SearchRequestsWith(pa.RequestFilter(f), store.SearchAuto)
		if err != nil {
			return err
		}
		pa.NoteRequestHits(page.Results)
		hits = append(hits, page.Results...)
		if len(states) == 1 {
			next = page.NextCursor
		}
	}
	if len(states) > 1 {
		slices.SortStableFunc(hits, func(x, y requestdomain.SearchHit) int { return int(y.Request.ID - x.Request.ID) })
		hits = hits[:min(len(hits), issuesClosedShown)]
	}
	if members != nil {
		hits = slices.DeleteFunc(hits, func(h requestdomain.SearchHit) bool { return !members[h.Request.ID] })
		hits = hits[:min(len(hits), requestsShown)]
		next = ""
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.Request.ID
	}
	labels, _ := a.store.LabelsFor("request", ids)
	if iq.Label != "" || iq.Author != "" {
		hits = slices.DeleteFunc(hits, func(h requestdomain.SearchHit) bool {
			return !hasLabel(labels[h.Request.ID], iq.Label) || iq.Author != "" && !strings.EqualFold(iq.Author, pa.RequestHitView(h).Request.Person)
		})
		ids = ids[:0]
		for _, h := range hits {
			ids = append(ids, h.Request.ID)
		}
	}
	progress, err := a.store.CriteriaProgress(ids)
	if err != nil {
		return err
	}
	waiting := a.openTasksByRequest(pa, project)
	now := overviewNow().UTC()
	for _, raw := range hits {
		h := pa.RequestHitView(raw)
		req := h.Request
		row := issueRow{Kind: "request", N: req.ID, Href: v.Base + "/" + strconv.FormatInt(req.ID, 10), Title: req.Title, State: req.State, StateLabel: requestStateLabel(l, req.State),
			TypeLabel: requestTypeLabel(l, req.Type), Person: req.Person, Age: shortAge(l, now, parseTime(req.CreatedAt)), Icon: "issue", Tone: "open", Branch: req.Scope.Branch}
		switch req.State {
		case "done":
			row.Icon, row.Tone = "done", "closed"
		case "dropped":
			row.Icon, row.Tone = "drop", "drop"
		}
		row.Opened = issueOpened(l, req.CreatedAt, row.Age, req.Person, now)
		row.Labels = rowLabels(labels[req.ID], v.Base)
		if p := progress[req.ID]; p.Total > 0 {
			row.Done, row.Total, row.Percent = p.Done, p.Total, p.Done*100/p.Total
			row.Progress = strconv.Itoa(p.Done) + "/" + strconv.Itoa(p.Total)
		}
		if req.State == "open" {
			if t, ok := waiting[req.ID]; ok {
				row.Waiting, row.WaitingTitle = firstNonEmpty(t.Assignee, req.Person), t.Title
			}
		}
		v.Rows = append(v.Rows, row)
	}
	live := a.liveByRequest(pa, ids, now, l)
	var liveIDs []string
	for i := range v.Rows {
		if v.Rows[i].Kind == "request" {
			liveIDs = append(liveIDs, strconv.FormatInt(v.Rows[i].N, 10))
			v.Rows[i].Live = live[v.Rows[i].N]
		}
	}
	v.LiveIDs = strings.Join(liveIDs, ",")
	if next != "" {
		v.Older = v.Base + "?" + url.Values{"q": {q}, "cursor": {next}}.Encode()
	}
	filtered := q != "" && q != "is:issue is:open" && q != "is:open" && q != "is:issue"
	v.Empty = len(v.Rows) == 0 && !filtered && filter.Cursor == "" && iq.State == "open"
	v.NoMatch = len(v.Rows) == 0 && !v.Empty
	v.EmptyKey = "issues.empty"
	return nil
}

// issueOpened is "opened 3 d ago by Robin" for a row of the list.
func issueOpened(l lang, created, age, person string, now time.Time) string {
	if at := parseTime(created); !at.IsZero() && now.Sub(at) < time.Minute {
		return l.msg("issues.opened_now_by", person)
	}
	return l.msg("issues.opened_by", age, person)
}

// openTasksByRequest is the first open human task of each request of the
// project, as far as the viewer may list tasks.
func (a *app) openTasksByRequest(pa *store.ProjectAccess, project string) map[int64]humantask.Task {
	out := map[int64]humantask.Task{}
	list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: project, State: humantask.Open, Limit: 500})
	if err != nil {
		return out
	}
	for _, t := range list {
		if t.State != humantask.Open || t.RequestID == 0 {
			continue
		}
		if _, ok := out[t.RequestID]; !ok {
			out[t.RequestID] = t
		}
	}
	return out
}
