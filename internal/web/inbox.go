package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// inboxAction is one button of an inbox row. A task is ticked off with a POST
// (TaskID), a mention is marked read with a POST (ReadRoom, ReadSeq); every
// other action is a link.
type inboxAction struct {
	Label, Href string
	Primary     bool
	TaskID      int64
	// AttentionID closes an open question, approval or blocker as seen.
	AttentionID int64
	ReadRoom    string
	ReadSeq     int64
	// Token is the CSRF token of the form behind a POST action.
	Token string
}

// inboxRow is one line of the inbox: icon, source line, title and one to three
// actions. Token is the CSRF token of the forms; Forms is false for a session
// from a pasted token, which may read but not change anything.
type inboxRow struct {
	Mark, Tone      string
	Title           string
	Num, NumHref    string
	Lead, Rest, Age string
	AgeOld, Mention bool
	Actions         []inboxAction
	Token           string
	Forms           bool
	// Actor is who the row comes from, TaskN the number of its task, RequestID
	// the issue it hangs on; Live is the agent working on that issue right now.
	Actor     string
	TaskN     int64
	RequestID int64
	Live      liveAgent
	// Cats are the filters the row belongs to ("direkt", "fragen", "aufgaben",
	// "issues"). Href is where a click on the row leads.
	Cats []string
	Href string
	// In names the room a message came in ("Jonas in Release-Team"); Body is the
	// message itself, Quote the viewer's own message it answers.
	In, InHref, Body string
	Quote            *inboxQuote
	// Reply is the composer behind "Reply"; nil when the row cannot be answered
	// in place.
	Reply *composerView
	// Done marks a row of the finished entries: the row is quiet and has no actions.
	Done bool
	// Options are the answers an agent proposes (two to six), ProposedBy its short
	// name; a click posts to /ui/inbox/answer as QuickKind and QuickID name the
	// entry. Quick is, on a finished row, the proposal it was answered with.
	Options    []inboxOption
	ProposedBy string
	QuickKind  string
	QuickID    int64
	Quick      *inboxQuick
	// Note is what a task says about itself: "Kurz erklärt" and its steps.
	Note *taskNote
	// DecisionN is the number of the open decision of a row, Brief its short text;
	// OwnKey is the catalog key of the button that opens the composer ("" reads "Own answer").
	DecisionN   int64
	Brief       string
	OwnKey      string
	AttentionID int64
	taskID      int64
	at          time.Time
}

// inboxOption is one proposed answer: Key is what the answer route takes, Text
// what the button says and what the answer says.
type inboxOption struct{ Key, Text string }

// Lead is the letter in front of the line of a proposal ("A"): the key when it is a short mark
// and the text does not carry it already as "A: ".
func (o inboxOption) Lead() string {
	if len(o.Key) == 0 || len(o.Key) > 2 || o.Key != strings.ToUpper(o.Key) {
		return ""
	}
	return o.Key
}

// Body is the text of the proposal without its "A: " lead.
func (o inboxOption) Body() string {
	for _, sep := range []string{": ", ". ", ") "} {
		if rest, ok := strings.CutPrefix(o.Text, o.Key+sep); ok && o.Lead() != "" {
			return rest
		}
	}
	return o.Text
}

// inboxQuick is the proposal a finished entry was answered with ("per Schnellantwort").
type inboxQuick struct{ Text, Person string }

// inboxQuote is the viewer's own message that a group message answers.
type inboxQuote struct{ Who, Text string }

// inboxFilters are the filter chips in order; "alle" is every row.
var inboxFilters = []string{"alle", "direkt", "fragen", "aufgaben", "issues"}

// inboxFilterKeys are the catalog keys of the chips.
var inboxFilterKeys = map[string]string{"alle": "inbox.filter.alle", "direkt": "inbox.filter.direkt", "fragen": "inbox.filter.fragen",
	"aufgaben": "inbox.filter.aufgaben", "issues": "inbox.filter.issues"}

// inboxFilterChip is one chip of the filter row.
type inboxFilterChip struct {
	Key, Label, Href string
	Count            int
	Current          bool
}

// inboxSection is one box of the inbox: Title is a catalog key.
type inboxSection struct {
	ID, Title string
	// None is the catalog key of the line for a box without rows ("" reads "nothing here").
	None string
	Rows []inboxRow
	Warn bool
}

type inboxView struct {
	Waiting, Mentioned []inboxRow
	WaitingBox         inboxSection
	DirectBox          inboxSection
	MentionedBox       inboxSection
	Direct             []inboxRow
	// Filters is the chip row, Filter the key of the one that is on.
	Filters []inboxFilterChip
	Filter  string
	// FilteredEmpty: a filter is on and no row matches it.
	FilteredEmpty bool
	// Projects are the repos the list can be narrowed to; LiveIDs the issues
	// the rows hang on, for the live stream.
	Projects            []projectOption
	AllSelected, CanAll bool
	LiveIDs             string
	// Empty: neither section has a row. The page says so in one line and offers
	// one way on.
	Empty bool
	// ShowDone is the switch "Show done": the finished entries of the last 30
	// days come as a box under the others. ToggleHref turns it on or off, the
	// filter and the repo stay.
	// Result is the line after a task could not be ticked off (?fail=).
	Result     *requestResult
	ShowDone   bool
	ToggleHref string
	DoneBox    inboxSection
	DoneDays   int
}

// waitingFor is the number of rows in "Waiting for you" for the shell, counted
// once per request (shellMemo) with the same query as the page itself.
func (a *app) waitingFor(r *http.Request, project string) []forYouRow {
	memo, _ := r.Context().Value(shellMemoKey{}).(*shellMemo)
	if memo != nil && memo.waitingSet {
		return memo.waiting
	}
	rows, err := a.inboxWaiting(r, a.access(r), a.shellBaseFor(r).who, project, overviewNow().UTC())
	if err != nil {
		rows = nil
	}
	if memo != nil {
		memo.waiting, memo.waitingSet = rows, true
	}
	return rows
}

// inboxWaiting collects what waits for the viewer: their own open tasks (and
// those for everyone), the questions, approvals, blockers and handoffs asked of
// them, and, for reviewers, the knowledge proposals and the gardener's notes as
// one row each. Every source is the one the overview already reads through
// ProjectAccess and CoordAccess, so the inbox shows nothing the viewer could not
// open anyway.
func (a *app) inboxWaiting(r *http.Request, pa *store.ProjectAccess, who viewer, project string, now time.Time) ([]forYouRow, error) {
	l := a.langFor(r)
	me, _ := store.AccountNumericID(browserPrincipal(r).ID)
	tasks, _, err := a.homeTasks(l, r, pa, project, now, me)
	if err != nil {
		return nil, err
	}
	var rows []forYouRow
	for _, t := range tasks {
		if (t.person == 0 || t.person == me) && t.canDone {
			rows = append(rows, t.row)
		}
	}
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
	rows = append(rows, a.inboxDecisions(l, pa, project, now)...)
	sortForYou(rows)
	return rows, nil
}

// inboxLabels are the names of the people and agents behind the messages of the
// rooms in play, as the room page spells them.
func (a *app) inboxLabels(r *http.Request, roomKeys ...string) map[string]string {
	l := a.langFor(r)
	current := browserPrincipal(r)
	labels := coordIdentityLabels(l, current, nil)
	access := a.browserCoord(r)
	for _, key := range roomKeys {
		if peers, err := access.Peers(key, ""); err == nil {
			for id, label := range coordIdentityLabels(l, current, nil, peers...) {
				labels[id] = label
			}
		}
	}
	return labels
}

// inboxMessage is the newest unread message of a room that somebody else wrote.
// With mentionOnly it must also mention the viewer. The earlier messages of the
// window come back as well, to find the viewer's own message a reply answers.
type inboxMessage struct {
	Msg     store.CoordMessage
	Quote   *store.CoordMessage
	Author  string
	Mine    bool
	Present bool
}

func (a *app) inboxLatestUnread(r *http.Request, s store.CoordRoomSummary, labels map[string]string, mentionOnly bool) inboxMessage {
	l := a.langFor(r)
	current := browserPrincipal(r)
	access := a.browserCoord(r)
	page, presented, err := access.MessagePresentationWindow(store.DestinationRoom, s.Room.Key, store.LatestWindow(30))
	if err != nil || len(presented) != len(page.Messages) {
		return inboxMessage{}
	}
	mine := func(m store.CoordMessage) bool {
		return m.AuthorKind == store.AuthorHuman && m.AuthorPrincipalID == current.ID
	}
	unread := func(m store.CoordMessage) bool {
		return m.Sequence > s.ReadThrough || (s.ManualUnreadFrom > 0 && m.Sequence >= s.ManualUnreadFrom)
	}
	checks := 0
	for i := len(page.Messages) - 1; i >= 0; i-- {
		m := page.Messages[i]
		if !unread(m) || mine(m) || m.AuthorKind == store.AuthorSystem {
			continue
		}
		if mentionOnly {
			if checks++; checks > 10 {
				break
			}
			ids, err := access.MessageMentions(m.ID)
			if err != nil || !slices.Contains(ids, current.ID) {
				continue
			}
		}
		out := inboxMessage{Msg: m, Present: true}
		id := strings.TrimSpace(m.SenderExternalID)
		if m.AuthorKind == store.AuthorHuman && strings.TrimSpace(m.AuthorPrincipalID) != "" {
			id = strings.TrimSpace(m.AuthorPrincipalID)
		}
		out.Author = readableAuthor(l, strings.TrimSpace(presented[i].AuthorLabel))
		if out.Author == "" {
			out.Author = coordIdentityLabel(l, id, labels)
		}
		if m.ReplyTo > 0 {
			for j := range page.Messages {
				if q := page.Messages[j]; q.ID == m.ReplyTo && mine(q) {
					out.Quote = &q
				}
			}
		}
		return out
	}
	return inboxMessage{}
}

// inboxReplyComposer is the composer that opens under a row: it posts to
// /ui/inbox/reply, which answers in the room the message lives in.
func (a *app) inboxReplyComposer(l lang, token, who string, room string, replyTo, attention int64) *composerView {
	hidden := []hiddenField{{"room", room}}
	if replyTo > 0 {
		hidden = append(hidden, hiddenField{"reply_to", strconv.FormatInt(replyTo, 10)})
	}
	if attention > 0 {
		hidden = append(hidden, hiddenField{"attention_id", strconv.FormatInt(attention, 10)})
	}
	ph := l.msg("inbox.reply_placeholder")
	if who != "" {
		ph = l.msg("inbox.reply_to", who)
	}
	return &composerView{Action: "/ui/inbox/reply", Token: token, Placeholder: ph, Submit: l.msg("inbox.reply_send"), Cancel: l.msg("inbox.reply_cancel"), Hidden: hidden}
}

// inboxMentions lists the rooms in which people mentioned the viewer and the
// viewer has not read yet: one row per room with the message that mentions
// them, not one per message. Rooms come from CoordAccess.RoomSummaries, so only
// rooms the viewer may read appear.
func (a *app) inboxMentions(r *http.Request, project string) ([]inboxRow, error) {
	l := a.langFor(r)
	summaries, err := a.browserCoord(r).RoomSummaries()
	if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current := browserPrincipal(r)
	token := csrfOf(r)
	var keys []string
	for _, s := range summaries {
		if s.MentionUnread > 0 {
			keys = append(keys, s.Room.Key)
		}
	}
	labels := a.inboxLabels(r, keys...)
	for k, v := range coordIdentityLabels(l, current, nil) {
		labels[k] = v
	}
	var rows []inboxRow
	for _, s := range summaries {
		if s.MentionUnread == 0 || (project != "" && s.Room.Key != store.RoomKeyForProject(project)) {
			continue
		}
		name := coordShortRoomName(coordRoomKindOf(s.Room.Key), coordRoomLabel(l, s.Room, current.ID, labels))
		last := parseTime(s.LastMessageAt)
		row := inboxRow{Mark: "rooms", Mention: true, Title: name, In: name, InHref: "/ui/coord?room=" + url.QueryEscape(s.Room.Key),
			Href: "/ui/coord?room=" + url.QueryEscape(s.Room.Key), at: last,
			Age: shortAgo(l, overviewNow().UTC(), last)}
		row.Actions = []inboxAction{{Label: l.msg("inbox.act.read"), ReadRoom: s.Room.Key, ReadSeq: s.LastSequence}}
		if m := a.inboxLatestUnread(r, s, labels, true); m.Present {
			row.Actor, row.Lead, row.Title, row.Body = m.Author, m.Author, "", questionTitle(m.Msg.Body, 220)
			row.Age = shortAgo(l, overviewNow().UTC(), parseTime(m.Msg.CreatedAt))
			if s.MentionUnread > 1 {
				row.Rest = l.msg("inbox.mentions", s.MentionUnread)
			}
			row.Href = coordRoomURL(s.Room.Key, "around", m.Msg.Sequence) + "#message-" + strconv.FormatInt(m.Msg.Sequence, 10)
			if m.Msg.Intent != "" && isAttentionIntent(m.Msg.Intent) {
				row.Cats = append(row.Cats, "fragen")
			}
			row.Reply = a.inboxReplyComposer(l, token, m.Author, s.Room.Key, m.Msg.ID, 0)
		} else {
			row.Lead = l.msg("inbox.mentions", s.MentionUnread)
			if s.MentionUnread == 1 {
				row.Lead = l.msg("inbox.mentions_one")
			}
			row.In, row.InHref = "", ""
			row.Reply = a.inboxReplyComposer(l, token, "", s.Room.Key, 0, 0)
		}
		rows = append(rows, row)
	}
	sortInboxRows(rows)
	return rows, nil
}

// inboxDirect lists the direct and group conversations with something unread
// from somebody else: one row per conversation with its newest message. A group
// row names the group; a message that answers one of the viewer's own quotes it.
func (a *app) inboxDirect(r *http.Request) ([]inboxRow, error) {
	l := a.langFor(r)
	summaries, err := a.browserCoord(r).RoomSummaries()
	if errors.Is(err, store.ErrCoordNotFound) || errors.Is(err, store.ErrCoordForbidden) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current := browserPrincipal(r)
	token := csrfOf(r)
	var open []store.CoordRoomSummary
	var keys []string
	for _, s := range summaries {
		if (s.Room.Kind == store.RoomDirect || s.Room.Kind == store.RoomGroup) && s.Unread > 0 {
			open = append(open, s)
			keys = append(keys, s.Room.Key)
		}
	}
	if len(open) == 0 {
		return nil, nil
	}
	labels := a.inboxLabels(r, keys...)
	var rows []inboxRow
	for _, s := range open {
		m := a.inboxLatestUnread(r, s, labels, false)
		if !m.Present {
			continue
		}
		created := parseTime(m.Msg.CreatedAt)
		room := "/ui/coord?room=" + url.QueryEscape(s.Room.Key)
		row := inboxRow{Mark: "rooms", Actor: m.Author, Lead: m.Author, Body: questionTitle(m.Msg.Body, 220), Cats: []string{"direkt"},
			Age: shortAgo(l, overviewNow().UTC(), created), at: created, Mention: true,
			Href: coordRoomURL(s.Room.Key, "around", m.Msg.Sequence) + "#message-" + strconv.FormatInt(m.Msg.Sequence, 10)}
		if s.Room.Kind == store.RoomGroup {
			row.In, row.InHref = coordRoomLabel(l, s.Room, current.ID, labels), room
			if m.Quote != nil {
				row.Quote = &inboxQuote{Who: l.msg("inbox.you"), Text: questionTitle(m.Quote.Body, 140)}
			}
		}
		row.Reply = a.inboxReplyComposer(l, token, m.Author, s.Room.Key, m.Msg.ID, 0)
		row.Actions = []inboxAction{{Label: l.msg("inbox.act.read"), ReadRoom: s.Room.Key, ReadSeq: s.LastSequence}}
		rows = append(rows, row)
	}
	sortInboxRows(rows)
	return rows, nil
}

// sortInboxRows puts the newest row first.
func sortInboxRows(rows []inboxRow) {
	slices.SortStableFunc(rows, func(x, y inboxRow) int { return y.at.Compare(x.at) })
}

// inboxCats are the filters a waiting row belongs to: tasks to "aufgaben", and
// to "issues" when they hang on one; questions, approvals and blockers to
// "fragen"; a handoff or the knowledge proposals to "aufgaben".
func inboxCats(f forYouRow) []string {
	var cats []string
	switch {
	case f.taskID != 0:
		cats = append(cats, "aufgaben")
		if f.RequestID != 0 {
			cats = append(cats, "issues")
		}
	case f.decisionID != 0:
		cats = append(cats, "fragen")
	case f.attentionID != 0 && (f.Mark == "ask" || f.Mark == "approve" || f.Mark == "warn"):
		cats = append(cats, "fragen")
	default:
		cats = append(cats, "aufgaben")
	}
	return cats
}

func (a *app) inboxRows(r *http.Request, forYou []forYouRow) []inboxRow {
	l := a.langFor(r)
	token, forms := csrfOf(r), interactive(r)
	rows := make([]inboxRow, 0, len(forYou))
	for _, f := range forYou {
		row := inboxRow{Mark: f.Mark, Tone: f.Tone, Title: f.Title, Lead: f.Lead, Rest: f.Rest, Age: f.Age, AgeOld: f.AgeOld,
			Token: token, Forms: forms, taskID: f.taskID, TaskN: f.taskID, Actor: f.Actor, RequestID: f.RequestID, Href: f.Href, Cats: inboxCats(f), AttentionID: f.attentionID, DecisionN: f.decisionID, Brief: f.Brief}
		switch {
		case f.taskID != 0:
			row.Num, row.NumHref = f.num, f.Href
			row.Note = a.taskNoteFor(l, f.taskID, f.steps)
			if forms {
				row.Actions = append(row.Actions, inboxAction{Label: l.msg("inbox.act.done"), TaskID: f.taskID, Primary: true})
			} else {
				row.Actions = append(row.Actions, inboxAction{Label: l.msg("inbox.act.open"), Href: f.Href, Primary: true})
			}
		case f.attentionID != 0:
			if forms {
				if f.roomKey != "" && f.Mark == "ask" {
					row.Reply = a.inboxReplyComposer(l, token, f.Actor, f.roomKey, f.messageID, f.attentionID)
				}
				row.Actions = append(row.Actions, inboxAction{Label: l.msg("inbox.act.done"), AttentionID: f.attentionID})
			} else {
				row.Actions = []inboxAction{{Label: f.Action, Href: f.Href, Primary: true}}
			}
		case f.decisionID != 0:
			// Answered with a variant (the buttons, set by inboxProposals) or on its page.
			if f.Href != "" {
				row.Actions = []inboxAction{{Label: f.Action, Href: f.Href}}
			}
		default:
			row.Actions = []inboxAction{{Label: f.Action, Href: f.Href, Primary: true}}
		}
		rows = append(rows, row)
	}
	return rows
}

// inboxFilter reads ?f=; anything unknown is "alle".
func inboxFilter(r *http.Request) string {
	f := r.URL.Query().Get("f")
	if slices.Contains(inboxFilters, f) {
		return f
	}
	return "alle"
}

// inboxKeep keeps the rows that belong to the filter.
func inboxKeep(rows []inboxRow, filter string) []inboxRow {
	if filter == "alle" {
		return rows
	}
	var out []inboxRow
	for _, row := range rows {
		if slices.Contains(row.Cats, filter) {
			out = append(out, row)
		}
	}
	return out
}

func (a *app) inboxPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	w.Header().Set("Cache-Control", "no-store")
	project := selectedProject(a.shellFor(r, "inbox"))
	waiting := a.waitingFor(r, project)
	mentioned, err := a.inboxMentions(r, project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	direct, err := a.inboxDirect(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	v := &inboxView{Waiting: a.inboxRows(r, waiting), Mentioned: mentioned, Direct: direct}
	token, forms := csrfOf(r), interactive(r)
	if forms {
		a.inboxProposals(r, a.access(r), project, v.Waiting)
		a.inboxDecisionRows(l, token, v.Waiting)
	}
	for _, rows := range [][]inboxRow{v.Mentioned, v.Direct} {
		for i := range rows {
			rows[i].Token, rows[i].Forms = token, forms
			if !forms {
				rows[i].Actions, rows[i].Reply = nil, nil
			}
		}
	}
	for _, rows := range [][]inboxRow{v.Waiting, v.Mentioned, v.Direct} {
		for i := range rows {
			for j := range rows[i].Actions {
				rows[i].Actions[j].Token = token
			}
		}
	}
	pa := a.access(r)
	now := overviewNow().UTC()
	var ids []int64
	for _, row := range v.Waiting {
		if row.RequestID != 0 && !slices.Contains(ids, row.RequestID) {
			ids = append(ids, row.RequestID)
		}
	}
	live := a.liveByRequest(pa, ids, now, l)
	var idText []string
	for i := range v.Waiting {
		if n := v.Waiting[i].RequestID; n != 0 {
			v.Waiting[i].Live = live[n]
		}
	}
	for _, n := range ids {
		idText = append(idText, strconv.FormatInt(n, 10))
	}
	v.LiveIDs = strings.Join(idText, ",")
	base := a.shellBaseFor(r)
	v.Projects, v.AllSelected, v.CanAll = base.projects, base.all, base.who.kind == viewerOwner || len(base.projects) > 1
	v.Empty = len(v.Waiting) == 0 && len(v.Mentioned) == 0 && len(v.Direct) == 0
	v.Filter = inboxFilter(r)
	counts := map[string]int{"alle": len(v.Waiting) + len(v.Mentioned) + len(v.Direct)}
	for _, rows := range [][]inboxRow{v.Waiting, v.Mentioned, v.Direct} {
		for _, row := range rows {
			for _, c := range row.Cats {
				counts[c]++
			}
		}
	}
	for _, key := range inboxFilters {
		href := "/ui/inbox"
		q := url.Values{}
		if key != "alle" {
			q.Set("f", key)
		}
		if !v.AllSelected && project != "" {
			q.Set("project", project)
		}
		if len(q) > 0 {
			href += "?" + q.Encode()
		}
		v.Filters = append(v.Filters, inboxFilterChip{Key: key, Label: inboxFilterKeys[key], Href: href, Count: counts[key], Current: key == v.Filter})
	}
	waitingRows, directRows, mentionedRows := inboxKeep(v.Waiting, v.Filter), inboxKeep(v.Direct, v.Filter), inboxKeep(v.Mentioned, v.Filter)
	v.WaitingBox = inboxSection{ID: "waiting", Title: "inbox.waiting", None: "inbox.none", Rows: waitingRows, Warn: true}
	v.DirectBox = inboxSection{ID: "direct", Title: "inbox.direct", None: "inbox.none", Rows: directRows}
	v.MentionedBox = inboxSection{ID: "mentioned", Title: "inbox.mentioned", None: "inbox.none", Rows: mentionedRows}
	v.FilteredEmpty = !v.Empty && len(waitingRows)+len(directRows)+len(mentionedRows) == 0
	if code := r.URL.Query().Get("fail"); code != "" {
		v.Result = &requestResult{Text: l.msg(inboxFailKey(code)), Kind: "failed", Fail: true}
	}
	v.ShowDone, v.DoneDays = r.URL.Query().Get("done") == "1", store.InboxDoneDays
	q := url.Values{}
	if v.Filter != "alle" {
		q.Set("f", v.Filter)
	}
	if !v.AllSelected && project != "" {
		q.Set("project", project)
	}
	if !v.ShowDone {
		q.Set("done", "1")
	}
	v.ToggleHref = "/ui/inbox"
	if len(q) > 0 {
		v.ToggleHref += "?" + q.Encode()
	}
	if v.ShowDone {
		v.DoneBox = inboxSection{ID: "done", Title: "inbox.done", None: "inbox.done_none", Rows: inboxKeep(a.inboxDoneRows(r, pa, project, now), v.Filter)}
	}
	a.renderBrowser(w, r, "inbox", pageData{Title: l.msg("inbox.title"), InboxV: v})
}

// backTo is where a form of the inbox returns to: only the inbox itself.
func backTo(r *http.Request, fallback string) string {
	if strings.TrimSpace(r.FormValue("back")) == "inbox" {
		return "/ui/inbox"
	}
	return fallback
}

func (a *app) uiRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/inbox", http.StatusSeeOther)
}

// inboxHowKeys are the words for how a finished entry was finished.
var inboxHowKeys = map[string]string{"done": "inbox.how.done", "confirmed": "inbox.how.confirmed", "dropped": "inbox.how.dropped", "decided": "inbox.how.decided",
	"deferred": "inbox.how.deferred", "resolved": "inbox.how.resolved", "dismissed": "inbox.how.dismissed", "expired": "inbox.how.expired"}

// inboxDoneRows are the finished entries of the inbox for the switch "Show
// done": tasks, decisions and questions of the last 30 days, the newest first,
// from pa.Inbox, so with the rights of the lists they came from. The rows only
// show; there is nothing left to do on them.
func (a *app) inboxDoneRows(r *http.Request, pa *store.ProjectAccess, project string, now time.Time) []inboxRow {
	l := a.langFor(r)
	entries, err := pa.Inbox(a.browserCoord(r), store.InboxFilter{State: store.InboxDone, Project: project, Mine: true})
	if err != nil {
		return nil
	}
	rows := make([]inboxRow, 0, len(entries))
	for _, e := range entries {
		row := inboxRow{Done: true, Title: e.Title, Lead: e.DoneBy, Rest: label(l, inboxHowKeys, e.How), Age: shortAgo(l, now, parseTime(e.At)), Mark: e.Reason}
		if q := e.QuickAnswer; q != nil && strings.TrimSpace(q.Text) != "" {
			row.Quick = &inboxQuick{Text: q.Text, Person: q.Person}
		}
		switch e.Kind {
		case store.InboxTask:
			row.Cats = []string{"aufgaben"}
			row.Num = "#" + strconv.FormatInt(e.ID, 10)
			if e.RequestID != 0 && e.Project != "" {
				row.Cats = append(row.Cats, "issues")
				row.Href = repoHref(e.Project) + "/issues/" + strconv.FormatInt(e.RequestID, 10) + "#task-" + strconv.FormatInt(e.ID, 10)
				row.NumHref = row.Href
			}
			row.TaskN = e.ID
		case store.InboxDecision:
			row.Cats, row.Mark = []string{"fragen"}, "ask"
		default:
			row.Cats = []string{"fragen"}
			switch e.Reason {
			case "approval":
				row.Mark = "approve"
			case "blocker":
				row.Mark = "warn"
			case "handoff":
				row.Cats, row.Mark = []string{"aufgaben"}, "handoff"
			default:
				row.Mark = "ask"
			}
			if e.HomeRoomKey != "" {
				row.Href = "/ui/coord?room=" + url.QueryEscape(e.HomeRoomKey)
			}
		}
		rows = append(rows, row)
	}
	return rows
}
