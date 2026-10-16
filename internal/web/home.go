package web

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Home: "Für dich", "Läuft", "Diese Woche". Every list is built from the set
// the viewer may see anyway (#2447): tasks through ListHumanTasks, questions
// through CoordAccess.Attention, proposals through the knowledge window and
// CheckKnowledge, agents through the project's agent right. A person other than
// the viewer is offered as a tab only when one of the tasks the viewer may read
// is assigned to that person; the questions of another person are never read.

const (
	homeMaxRows    = 10
	homeAgentLimit = 200
	// homeTabPeople: at most this many other people get a tab of their own.
	homeTabPeople = 3
	// homeLessonsCap is how far new lessons are counted.
	homeLessonsCap = 100

	// gardenerHarness marks the notes the gardener files (garden.Harness; a
	// test keeps the two equal, so the web package need not import the gardener).
	gardenerHarness = "gardener"
)

// Sort ranks of a row of "Für dich": what holds an agent up comes first.
const (
	rankBlocks = iota
	rankHandoff
	rankTask
	rankGarden
	rankProposals
)

// forYouRow is one line of "Für dich": mark, title, source and one action.
type forYouRow struct {
	// Mark is the key of the icon, Tone "ask" or "warn" or "".
	Mark, Tone string
	Title      string
	// Lead is the bold start of the source line, Rest the rest, Age the age
	// words, last, in the warning colour when AgeOld.
	Lead, Rest, Age string
	AgeOld          bool
	Action, Href    string
	// Primary fills the action: the one next thing for an agent that waits.
	Primary bool
	// Open: the first row is unfolded and carries "Kurz erklärt".
	Open    bool
	Explain *explainView

	rank        int
	at          time.Time
	explainKind string
	explainID   int64
	// explainStored: the text is shown only when an agent wrote it.
	explainStored bool
}

type forYouTab struct {
	Label, Href string
	Current     bool
}

type forYouView struct {
	Rows []forYouRow
	// Total is the number of rows before the cut.
	Total int
	// More is the number of rows beyond the ones shown.
	More int
	// Tabs is empty when there is nobody else to switch to.
	Tabs []forYouTab
}

// runRow is one agent of "Läuft": two lines of text, a state, one action.
type runRow struct {
	agentRow
	Title, Line  string
	Action, Href string
}

type runGroup struct {
	Label, Room string
	Rows        []runRow
}

type runView struct {
	Groups []runGroup
	// Hidden counts the quiet agents that are not listed.
	Hidden int
}

// weekView is the sentence of "Diese Woche".
type weekView struct {
	Text string
}

func (a *app) homeForYou(r *http.Request, pa *store.ProjectAccess, who viewer, project string, now time.Time) (*forYouView, error) {
	l := a.langFor(r)
	me, _ := store.AccountNumericID(browserPrincipal(r).ID)
	tasks, people, err := a.homeTasks(l, r, pa, project, now, me)
	if err != nil {
		return nil, err
	}
	// Which list: the viewer's own (default), one person's, or everyone's.
	scope := r.URL.Query().Get("wer")
	var other int64
	if n, err := strconv.ParseInt(scope, 10, 64); err == nil && people[n] != "" {
		other, scope = n, "person"
	} else if scope != "alle" || len(people) == 0 {
		scope = ""
	}
	var rows []forYouRow
	for _, t := range tasks {
		switch {
		case scope == "alle":
		case scope == "person" && t.person == other:
		case scope == "" && (t.person == 0 || t.person == me) && t.canDone:
		default:
			continue
		}
		rows = append(rows, t.row)
	}
	if scope != "person" {
		attention, err := a.homeAttention(r, now)
		if err != nil {
			return nil, err
		}
		rows = append(rows, attention...)
		if who.reviewer {
			bundles, err := a.homeBundles(l, pa, project, now)
			if err != nil {
				return nil, err
			}
			rows = append(rows, bundles...)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		// The one that waited longest first; a row without a time goes last.
		if rows[i].at.IsZero() != rows[j].at.IsZero() {
			return !rows[i].at.IsZero()
		}
		return rows[i].at.Before(rows[j].at)
	})
	v := &forYouView{Rows: rows, Total: len(rows)}
	if len(v.Rows) > homeMaxRows {
		v.More, v.Rows = len(v.Rows)-homeMaxRows, v.Rows[:homeMaxRows]
	}
	if len(v.Rows) > 0 {
		top := &v.Rows[0]
		top.Open = true
		switch {
		case top.explainStored:
			top.Explain = a.explainStoredFor(l, top.explainKind, top.explainID)
		case top.explainKind != "":
			top.Explain = a.explainFor(l, top.explainKind, top.explainID)
		}
	}
	if len(people) > 0 {
		v.Tabs = append(v.Tabs, forYouTab{Label: l.msg("home.tab.mine"), Href: "/ui/overview", Current: scope == ""})
		ids := make([]int64, 0, len(people))
		for id := range people {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(x, y int64) int { return strings.Compare(people[x], people[y]) })
		if len(ids) > homeTabPeople {
			ids = ids[:homeTabPeople]
		}
		for _, id := range ids {
			v.Tabs = append(v.Tabs, forYouTab{Label: people[id], Href: "/ui/overview?wer=" + strconv.FormatInt(id, 10), Current: scope == "person" && other == id})
		}
		v.Tabs = append(v.Tabs, forYouTab{Label: l.msg("home.tab.all"), Href: "/ui/overview?wer=alle", Current: scope == "alle"})
	}
	return v, nil
}

type homeTask struct {
	row     forYouRow
	person  int64
	canDone bool
}

// homeTasks lists the open tasks the viewer may see, and the other people they
// are assigned to by account number and name.
func (a *app) homeTasks(l lang, r *http.Request, pa *store.ProjectAccess, project string, now time.Time, me int64) ([]homeTask, map[int64]string, error) {
	var out []homeTask
	people := map[int64]string{}
	titles := map[int64]string{}
	for _, p := range visibleProjects(pa, project) {
		list, err := a.store.ListHumanTasks(pa, store.TaskFilter{Project: p, State: humantask.Open, Limit: tasksShown})
		if err != nil {
			return nil, nil, err
		}
		for _, t := range list {
			foreign := t.AssigneeAccount != 0 && t.AssigneeAccount != me && t.Assignee != ""
			if foreign {
				people[t.AssigneeAccount] = store.NormalizeAccountName(t.Assignee)
			}
			title, seen := titles[t.RequestID]
			if !seen {
				if a.requestReadable(pa, t.RequestID) {
					if d, err := a.store.RequestByID(t.RequestID); err == nil {
						title = d.Request.Title
					}
				}
				titles[t.RequestID] = title
			}
			rest := l.msg("home.task", lastSegment(t.Project))
			if title != "" {
				rest = l.msg("home.task_for", title)
			}
			if foreign {
				rest += agentLabelSep + store.NormalizeAccountName(t.Assignee)
			}
			created := parseTime(t.CreatedAt)
			row := forYouRow{
				Mark: t.Kind, Title: t.Title, Rest: rest, Action: l.msg("home.act.task"), Href: "/ui/tasks#task-" + strconv.FormatInt(t.ID, 10),
				rank: rankTask, at: created, explainKind: explain.KindTask, explainID: t.ID,
			}
			if !created.IsZero() {
				row.Age = l.msg("home.open_since", shortAge(l, now, created))
				row.AgeOld = olderThan(t.CreatedAt, now, taskOldAfter)
			}
			out = append(out, homeTask{row: row, person: t.AssigneeAccount, canDone: pa.CheckTask(t, store.ActWork) == nil})
		}
	}
	return out, people, nil
}

// homeAttention turns the open questions, approvals, blockers and handoffs for
// the viewer into rows. CoordAccess.Attention already left out everything the
// viewer may not read.
func (a *app) homeAttention(r *http.Request, now time.Time) ([]forYouRow, error) {
	l := a.langFor(r)
	access := a.browserCoord(r)
	items, err := access.Attention()
	if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
		// Without access to rooms (an account without agents) there is nothing.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current := browserPrincipal(r)
	labels := coordIdentityLabels(l, current, nil)
	seenRooms := map[string]bool{}
	for _, item := range items {
		if !item.IsRecipient || item.State != store.AttentionOpen || item.HomeRoomKey == "" || seenRooms[item.HomeRoomKey] {
			continue
		}
		seenRooms[item.HomeRoomKey] = true
		if peers, err := access.Peers(item.HomeRoomKey, ""); err == nil {
			for id, label := range coordIdentityLabels(l, current, nil, peers...) {
				labels[id] = label
			}
		}
	}
	views, _ := buildCoordAttentionViews(l, items, "", labels)
	byID := make(map[int64]store.AttentionItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	var rows []forYouRow
	for _, v := range views {
		item := byID[v.ID]
		room := coordShortRoomName(coordRoomKindOf(item.HomeRoomKey), coordRoomLabel(l, store.CoordRoom{Key: item.HomeRoomKey}, "", labels))
		created := parseTime(item.CreatedAt)
		row := forYouRow{
			Title: oneLine(v.Preview, 110), Href: v.URL, at: created, Tone: "ask",
			Rest:        coordSenderLabel(l, item, labels) + agentLabelSep + room,
			explainKind: explain.KindQuestion, explainID: item.MessageID, explainStored: item.Reason != store.AttentionQuestion,
		}
		switch item.Reason {
		case store.AttentionQuestion:
			row.Mark, row.Action, row.Primary, row.rank = "ask", l.msg("home.act.answer"), true, rankBlocks
		case store.AttentionApproval:
			row.Mark, row.Action, row.Primary, row.rank = "approve", l.msg("home.act.review"), true, rankBlocks
		case store.AttentionBlocker:
			row.Mark, row.Action, row.Primary, row.rank = "warn", l.msg("home.act.open"), true, rankBlocks
		default:
			row.Mark, row.Action, row.rank, row.Tone = "handoff", l.msg("home.act.accept"), rankHandoff, ""
		}
		if !created.IsZero() {
			if row.rank == rankBlocks {
				row.Age = l.msg("home.waiting_since", shortAge(l, now, created))
			} else {
				row.Age = shortAgo(l, now, created)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// homeBundles gives ONE row for the knowledge proposals of agents and ONE for
// the notes of the gardener, never a row per proposal. Only what the viewer may
// verify is counted.
func (a *app) homeBundles(l lang, pa *store.ProjectAccess, project string, now time.Time) ([]forYouRow, error) {
	w := a.knowledgeWindowFor(pa, project)
	w.Pending = true
	pa.Filtered()
	pending, err := a.knowledgeKeep(w, overviewMaxProposals, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && pa.CheckKnowledge(k, store.ActVerify) == nil
	})
	if err != nil {
		return nil, err
	}
	var agents, garden []store.Knowledge
	for _, k := range pending {
		if k.Harness == gardenerHarness {
			garden = append(garden, k)
		} else {
			agents = append(agents, k)
		}
	}
	var rows []forYouRow
	if len(garden) > 0 {
		oldest, projects := bundleFacts(garden)
		row := forYouRow{Mark: "garden", Title: l.msg("home.garden", len(garden)), Lead: l.msg("usage.job.garden"), Rest: projectNames(projects, 4),
			Action: l.msg("home.act.overview"), Href: "/ui/review?source=gardener", rank: rankGarden, at: oldest}
		if len(garden) == 1 {
			row.Title = l.msg("home.garden_one")
		}
		if now.Sub(oldest) >= 48*time.Hour {
			row.Age = l.msg("home.oldest_since", shortAge(l, now, oldest))
		}
		rows = append(rows, row)
	}
	if len(agents) > 0 {
		oldest, projects := bundleFacts(agents)
		title := l.msg("home.proposals", len(agents))
		switch {
		case len(agents) == 1:
			title = l.msg("home.proposals_one")
		case len(agents) >= overviewMaxProposals:
			title = l.msg("home.proposals_more", overviewMaxProposals)
		}
		row := forYouRow{Mark: "knowledge", Title: title, Action: l.msg("home.act.check"), Href: "/ui/review", rank: rankProposals, at: oldest}
		if len(projects) > 1 {
			row.Rest = l.msg("home.projects", len(projects))
		}
		if now.Sub(oldest) >= 48*time.Hour {
			row.Age = l.msg("home.oldest_since", shortAge(l, now, oldest))
			row.AgeOld = now.Sub(oldest) >= reviewOldAfter
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// bundleFacts is the oldest creation time and the projects of a set of entries.
func bundleFacts(entries []store.Knowledge) (time.Time, []string) {
	var oldest time.Time
	seen := map[string]bool{}
	var projects []string
	for _, k := range entries {
		if t := parseTime(firstNonEmpty(k.ObservedAt, k.CreatedAt)); !t.IsZero() && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
		if p := lastSegment(k.Scope.Project); p != "" && !seen[p] {
			seen[p] = true
			projects = append(projects, p)
		}
	}
	slices.Sort(projects)
	return oldest, projects
}

// projectNames lists at most limit project names, "a, b, c".
func projectNames(names []string, limit int) string {
	if len(names) > limit {
		names = names[:limit]
	}
	return strings.Join(names, ", ")
}

// homeRunning is "Läuft": the agents of the projects the viewer may list,
// grouped by project, each group in the order of what needs a person first.
// Quiet agents of more than a day are not listed, only counted.
func (a *app) homeRunning(r *http.Request, pa *store.ProjectAccess, project string, now time.Time) (*runView, error) {
	l := a.langFor(r)
	rows, err := a.listAgents(r, pa, project, now, homeAgentLimit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ExternalID
	}
	work, err := a.store.AgentWorkFor(pa, ids)
	if err != nil {
		return nil, err
	}
	order := map[string]int{"wait_you": 0, "stuck": 1, "work": 2, "wait_other": 3, "idle": 4, "off": 5}
	groups := map[string]*runGroup{}
	var keys []string
	v := &runView{}
	for _, row := range rows {
		if row.Presence == "off" {
			v.Hidden++
			continue
		}
		w := work[row.ExternalID]
		row.Work = agentWorkTitle(w, row)
		if w.RequestID > 0 && w.RequestTitle != "" {
			row.RequestRef = "REQ-" + strconv.FormatInt(w.RequestID, 10)
		}
		rr := runRow{agentRow: row, Title: row.Name}
		var line []string
		if row.Work != "" {
			rr.Title = row.Work
			line = append(line, row.Owner)
		}
		if row.Branch != "" && row.Branch != row.Work {
			line = append(line, row.Branch)
		}
		line = append(line, row.RequestRef)
		rr.Line = joinNonEmpty(line, agentLabelSep)
		switch row.Presence {
		case "wait_you":
			rr.Action, rr.Href = l.msg("home.act.answer"), coordRoomURL(row.RoomKey, "", 0)
		case "stuck":
			rr.Action, rr.Href = l.msg("ov.open_room"), coordRoomURL(row.RoomKey, "", 0)
		}
		g := groups[row.ProjectTitle]
		if g == nil {
			g = &runGroup{Label: row.Project, Room: coordRoomURL(row.RoomKey, "", 0)}
			groups[row.ProjectTitle] = g
			keys = append(keys, row.ProjectTitle)
		}
		g.Rows = append(g.Rows, rr)
	}
	best := func(g *runGroup) int {
		n := len(order)
		for _, rr := range g.Rows {
			n = min(n, order[rr.Presence])
		}
		return n
	}
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g.Rows, func(i, j int) bool { return order[g.Rows[i].Presence] < order[g.Rows[j].Presence] })
		v.Groups = append(v.Groups, *g)
	}
	sort.SliceStable(v.Groups, func(i, j int) bool {
		if bi, bj := best(&v.Groups[i]), best(&v.Groups[j]); bi != bj {
			return bi < bj
		}
		return v.Groups[i].Label < v.Groups[j].Label
	})
	return v, nil
}

func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// homeWeek is one sentence: how far the open requests are, and how many lessons
// came in. Nothing to say, no sentence.
func (a *app) homeWeek(l lang, pa *store.ProjectAccess, project string, now time.Time, requests []requestRow) (*weekView, error) {
	w := a.knowledgeWindowFor(pa, project)
	w.Since = now.Add(-learnedWindow).Format(time.RFC3339)
	pa.Filtered()
	learned, err := a.knowledgeKeep(w, homeLessonsCap, func(k store.Knowledge) bool {
		return pa.CanSeeKnowledge(k) && pa.CanDeliverKnowledge(k)
	})
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, rq := range requests {
		if rq.Total == 0 || len(parts) >= 2 {
			continue
		}
		key := "home.week.progress"
		if len(parts) > 0 {
			key = "home.week.progress_next"
		}
		parts = append(parts, l.msg(key, rq.Title, rq.Done, rq.Total))
	}
	text := ""
	if len(parts) > 0 {
		text = strings.Join(parts, ", ") + "."
	}
	switch n := len(learned); {
	case n == 1:
		text = strings.TrimSpace(text + " " + l.msg("home.week.lesson_one"))
	case n >= homeLessonsCap:
		text = strings.TrimSpace(text + " " + l.msg("home.week.lessons_more", homeLessonsCap))
	case n > 1:
		text = strings.TrimSpace(text + " " + l.msg("home.week.lessons", n))
	}
	if text == "" {
		return nil, nil
	}
	return &weekView{Text: text}, nil
}
