package web

import (
	"errors"
	"net/http"
	"slices"
	"sort"
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
	// Actor is who the row comes from (the creator of a task, the sender of a
	// question), RequestID the issue it hangs on (0 for none).
	Actor     string
	RequestID int64
	// Primary fills the action: the one next thing for an agent that waits.
	Primary bool
	// Open: the first row is unfolded and carries "Kurz erklärt".
	Open    bool
	Explain *explainView

	rank int
	at   time.Time
	// taskID and num name the task a row stands for (0 and "" for other rows):
	// the inbox ticks it off by that number.
	taskID int64
	num    string
	// attentionID, roomKey and messageID name the open question behind a row, so
	// the inbox can answer it in place (roomKey is empty for a message in a task
	// thread, which is answered there).
	attentionID, messageID int64
	roomKey                string
	explainKind            string
	explainID              int64
	// decisionID names the open decision a row stands for (0 for other rows);
	// Brief is its short text.
	decisionID int64
	Brief      string
	// steps are the walk-through of a task, for the inbox row.
	steps []humantask.Step
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
	// More is the number of rows beyond the ones shown, MoreHref where the first
	// of them leads (the task list when only tasks are left out).
	More     int
	MoreHref string
	// Tabs is empty when there is nobody else to switch to.
	Tabs []forYouTab
}

// runRow is one agent of "Läuft": two lines of text, a state, one action.
type runRow struct {
	agentRow
	Title, Line  string
	Action, Href string
	// SessionHref opens what the agent did: the title links there.
	SessionHref string
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

// sortForYou puts what holds an agent up first, then what has waited longest;
// a row without a time goes last.
func sortForYou(rows []forYouRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		if rows[i].at.IsZero() != rows[j].at.IsZero() {
			return !rows[i].at.IsZero()
		}
		return rows[i].at.Before(rows[j].at)
	})
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
				Mark: t.Kind, Title: t.Title, Rest: rest, Action: l.msg("home.act.task"), Href: taskHref(t),
				rank: rankTask, at: created, explainKind: explain.KindTask, explainID: t.ID,
				taskID: t.ID, num: t.HumanID(), Actor: t.CreatedBy, RequestID: t.RequestID, steps: t.Steps,
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
			Title: questionTitle(v.Preview, 110), Href: v.URL, at: created, Tone: "ask", Actor: coordSenderLabel(l, item, labels),
			Rest:        coordSenderLabel(l, item, labels) + agentLabelSep + room,
			explainKind: explain.KindQuestion, explainID: item.MessageID, explainStored: item.Reason != store.AttentionQuestion,
			attentionID: item.ID, messageID: item.MessageID,
		}
		if item.DestinationKind == store.DestinationRoom {
			row.roomKey = item.DestinationID
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
		row := forYouRow{Mark: "garden", Title: l.msg("home.garden", len(garden)), Lead: l.msg("usage.job.garden"), Rest: projectNames(projects, 4), Actor: "gardener",
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

// questionTitle is the line a question stands under. A long message is cut
// where the question ends, not mid-sentence: the first sentence that asks
// (ends in "?") if it fits, else the plain cut.
func questionTitle(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	start := 0
	for i, c := range rs {
		end := i == len(rs)-1 || ((c == '.' || c == '!' || c == '?') && rs[i+1] == ' ')
		if !end {
			continue
		}
		if c == '?' && i+1-start <= limit {
			return strings.TrimSpace(string(rs[start : i+1]))
		}
		start = i + 1
		for start < len(rs) && rs[start] == ' ' {
			start++
		}
	}
	return shortWords(s, limit)
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
