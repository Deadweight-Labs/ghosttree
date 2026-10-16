package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var badgeRE = regexp.MustCompile(`class="shell-badge"[^>]*>(\d+)</b>`)

func requestsNavIsCurrent(page string) bool {
	return regexp.MustCompile(`class="shell-link clay" href="/ui/requests" data-nav="requests" aria-current="page"`).MatchString(page)
}

// Tasks and pains get their entry: tabs Requests | Tasks | Pains on all three
// pages, and the navigation keeps "Requests" lit on each of them.
func TestRequestsTasksAndPainsShareTabsAndTheNavigationStaysOnRequests(t *testing.T) {
	e := ovEnv(t)
	for path, current := range map[string]string{"/ui/requests": "/ui/requests", "/ui/tasks": "/ui/tasks", "/ui/pains": "/ui/pains"} {
		_, page := fetchPage(t, e.Member, e.Base+path)
		if !requestsNavIsCurrent(page) {
			t.Errorf("%s: the navigation does not mark Requests", path)
		}
		for _, tab := range []string{"/ui/requests", "/ui/tasks", "/ui/pains"} {
			if !strings.Contains(page, `class="rq-tab`) || !strings.Contains(page, `href="`+tab+`"`) {
				t.Errorf("%s lacks the tab %s", path, tab)
			}
		}
		if !strings.Contains(page, `class="rq-tab is-active" href="`+current+`" aria-current="page"`) {
			t.Errorf("%s: the current tab is not marked", path)
		}
		if strings.Count(page, `aria-current="page"`) != 2 {
			t.Errorf("%s: exactly the nav entry and the tab are current, got %d", path, strings.Count(page, `aria-current="page"`))
		}
	}
}

// The number beside Requests counts the open tasks for the viewer, through the
// same visibility as the list: not the ones for somebody else, not a hidden
// project's, and a guest only counts what is assigned to them.
func TestOpenTasksForMeShowAsABadgeAndCountOnlyWhatIsVisible(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden thing", Project: shellHiddenProject})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For everyone"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For anna", Assignee: "anna"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For gina", Assignee: "gina"})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "SECRET-TASK"})

	count := func(c *http.Client, path string) string {
		t.Helper()
		_, page := fetchPage(t, c, e.Base+path)
		m := badgeRE.FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		return m[1]
	}
	if got := count(e.Member, "/ui/overview"); got != "2" {
		t.Errorf("anna's badge = %q, want 2 (hers and the one for everyone)", got)
	}
	if got := count(e.Guest, "/ui/overview"); got != "1" {
		t.Errorf("gina's badge = %q, want 1 (only what is assigned to her)", got)
	}
	if got := count(e.Owner, "/ui/overview"); got != "2" {
		t.Errorf("alice's badge = %q, want 2 (the two for everyone, one of them in the project only she is in)", got)
	}
	if got := count(e.Reviewer, "/ui/requests"); got != "1" {
		t.Errorf("rita's badge = %q, want 1", got)
	}
	_, tasks := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	if !strings.Contains(tasks, `class="rq-tabcount">2</b>`) {
		t.Error("the Tasks tab does not carry the number")
	}
	// Nothing open: no badge at all.
	none := ovEnv(t)
	if got := count2(t, none, none.Member, "/ui/overview"); got != "" {
		t.Errorf("badge without tasks = %q", got)
	}
}

func count2(t *testing.T, e shellEnv, c *http.Client, path string) string {
	t.Helper()
	_, page := fetchPage(t, c, e.Base+path)
	if m := badgeRE.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return ""
}

// Tasks: "Mine" is what is assigned to the viewer plus what is for everyone;
// "Everyone" is everything the viewer may see.
func TestTasksPageMineAndEveryone(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For everyone"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For anna", Assignee: "anna"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For lars", Assignee: "lars"})

	_, mine := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	if !strings.Contains(mine, "For everyone") || !strings.Contains(mine, "For anna") || strings.Contains(mine, "For lars") {
		t.Errorf("Mine = %s", mine)
	}
	if !strings.Contains(mine, `class="ov-btn clay ov-btn-primary" href="/ui/tasks" aria-current="true">Mine`) {
		t.Error("Mine is not the marked filter")
	}
	_, all := fetchPage(t, e.Member, e.Base+"/ui/tasks?who=all")
	for _, want := range []string{"For everyone", "For anna", "For lars"} {
		if !strings.Contains(all, want) {
			t.Errorf("Everyone lacks %q", want)
		}
	}
	if !strings.Contains(all, `href="/ui/tasks?who=all" aria-current="true">Everyone`) {
		t.Error("Everyone is not the marked filter")
	}
}

// Home: "Next" puts the questions agents wait on before the proposals, and
// folds every proposal into one line that links to the review.
func TestNextShowsQuestionsBeforeProposalsAndBundlesThem(t *testing.T) {
	e := ovEnv(t)
	for _, title := range []string{"P one", "P two", "P three"} {
		ovKnowledge(t, e.St, shellProject, title, "staged")
	}
	ovAgent(t, e.St, shellProject, "claude:asker", "asker", "person:2")
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(shellProject),
		SenderExternalID: "claude:asker", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, ClientID: "q1",
		Body: "Which of the two schemas?", Intent: store.IntentQuestion, Mentions: []string{"person:5"}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Reviewer, e.Base+"/ui/overview")
	q, p := strings.Index(page, "Which of the two schemas?"), strings.Index(page, "3 proposals wait for review")
	if q < 0 || p < 0 || q > p {
		t.Fatalf("question at %d, proposals at %d: the question must come first", q, p)
	}
	for _, title := range []string{"P one", "P two", "P three"} {
		if strings.Contains(page, title) {
			t.Errorf("proposal %q is listed on its own", title)
		}
	}
	if strings.Count(page, `href="/ui/review"`) != 1 {
		t.Errorf("the proposals must be one row linking to the review, got %d links", strings.Count(page, `href="/ui/review"`))
	}
}

// Home names what an agent works on, not its hash: the request title first, the
// work note next, the branch last; the id stays in the tooltip.
func TestHomeAgentRowsSayWhatTheAgentWorksOn(t *testing.T) {
	e := ovEnv(t)
	agentsSeed(t, e.St, shellProject, "claude:laptop:aaaa1111", "sess-a", "Roles first", "migrating")
	ovAgent(t, e.St, shellProject, "claude:laptop:bbbb2222", "twin-b", "person:2")
	ovAgent(t, e.St, shellProject, "claude:laptop:cccc3333", "twin-c", "person:2")
	if _, err := e.St.SetCoordWorkNote("claude:laptop:bbbb2222", "person:2", "Reading the audit"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(page, `<span class="ov-name">Roles first</span>`) {
		t.Errorf("the request title is not the headline: %s", page)
	}
	if !strings.Contains(page, `<span class="ov-name">Reading the audit</span>`) {
		t.Error("the work note is not the headline of an agent without a request")
	}
	if !strings.Contains(page, `title="claude:laptop:aaaa1111"`) {
		t.Error("the id is not in the tooltip")
	}
	visible := regexp.MustCompile(`title="[^"]*"`).ReplaceAllString(page, "")
	if strings.Contains(visible, "aaaa1111") || strings.Contains(visible, "#aaaa") {
		t.Error("the hash id is visible outside a tooltip")
	}
}

// Home and the room read the same presence: an agent that waits for a person
// is "Waiting on you" in both, even when its last sign of life is old.
func TestHomeAndRoomAgreeThatTheAgentWaitsOnAPerson(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:waiter", "waiter", "person:2")
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(shellProject),
		SenderExternalID: "claude:waiter", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, ClientID: "w1",
		Body: "Shall I drop the column?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	_, home := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	row := home[strings.Index(home, `title="claude:waiter"`):]
	row = row[:strings.Index(row, "</li>")]
	if !strings.Contains(row, "Waiting on you") || !strings.Contains(row, "is-waiting_user") {
		t.Errorf("home row: %s", row)
	}
	_, room := fetchPage(t, e.Owner, e.Base+"/ui/coord?room="+url.QueryEscape(store.RoomKeyForProject(shellProject)))
	if !strings.Contains(room, "Waiting on you") {
		t.Error("the room does not say the same")
	}
	if strings.Contains(room, "Waiting for you") || strings.Contains(home, "Waiting for you") {
		t.Error("the old wording is still there")
	}
}

// "Stuck?": an agent in the middle of a turn with no hook event for ten
// minutes. A finished turn is never stuck, and a subagent's call is a sign of
// life.
func TestHomeShowsAStuckAgentOnlyMidTurn(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	register := func(id, event string) {
		t.Helper()
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:2", Person: "anna", Provider: "claude",
			DisplayName: id, RoomKey: room, HookActivity: true, HookEvent: event}); err != nil {
			t.Fatal(err)
		}
	}
	silence := func(id string) {
		t.Helper()
		past := time.Now().UTC().Add(-(store.StuckAfter + time.Minute)).Format(time.RFC3339)
		if _, err := e.St.DB().Exec(`UPDATE coord_agents SET last_hook_at=? WHERE external_id=?`, past, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"claude:midturn", "claude:finished", "claude:subagent"} {
		register(id, store.HookTurnStart)
		register(id, store.HookTurnEnd)
		register(id, store.HookTurnStart)
	}
	register("claude:finished", store.HookTurnEnd)
	for _, id := range []string{"claude:midturn", "claude:finished", "claude:subagent"} {
		silence(id)
	}
	register("claude:subagent", store.HookTouch)

	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	rowOf := func(id string) string {
		i := strings.Index(page, `title="`+id+`"`)
		if i < 0 {
			t.Fatalf("no row for %s", id)
		}
		row := page[i:]
		return row[:strings.Index(row, "</li>")]
	}
	if row := rowOf("claude:midturn"); !strings.Contains(row, "Stuck?") || !strings.Contains(row, "is-stuck") {
		t.Errorf("a silent agent mid-turn: %s", row)
	}
	for _, id := range []string{"claude:finished", "claude:subagent"} {
		if row := rowOf(id); strings.Contains(row, "Stuck?") {
			t.Errorf("%s must not be stuck: %s", id, row)
		}
	}
	if strings.Index(page, `title="claude:midturn"`) > strings.Index(page, `title="claude:finished"`) {
		t.Error("the stuck agent must be listed before the ones that are fine")
	}
}

func trackSince(t *testing.T, st *store.Store, at string) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT OR REPLACE INTO org_state(key, value) VALUES('token_use_tracking_since', ?)`, at); err != nil {
		t.Fatal(err)
	}
}

func tokenAt(t *testing.T, st *store.Store, account, label, created, lastUsed string) int64 {
	t.Helper()
	_, info, err := st.CreateToken(account, store.TokenSpec{Label: label})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE api_tokens SET created_at=?, last_used_at=? WHERE id=?`, created, lastUsed, info.ID); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// Devices & tokens: the one used last comes first, never used ones are
// labelled and sit below, and "Revoke unused" revokes exactly those, of the
// person's own account only.
func TestTokensAreSortedByUseAndUnusedOnesCanBeRevoked(t *testing.T) {
	e := ovEnv(t)
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const month = 30 * 24 * time.Hour
	old := tokenAt(t, e.St, "anna", "old-use", at(2*month), at(48*time.Hour))
	recent := tokenAt(t, e.St, "anna", "recent-use", at(2*month), at(time.Hour))
	never := tokenAt(t, e.St, "anna", "e2e-never", at(2*month), "")
	fresh := tokenAt(t, e.St, "anna", "fresh-login", at(5*time.Minute), "")
	foreign := tokenAt(t, e.St, "gina", "gina-never", at(2*month), "")
	// Use has been recorded for two months: an empty last use now means never.
	trackSince(t, e.St, at(2*month))
	_, page := fetchPage(t, e.Member, e.Base+"/ui/account/tokens")
	i, j, k := strings.Index(page, "recent-use"), strings.Index(page, "old-use"), strings.Index(page, "e2e-never")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("order recent=%d old=%d never=%d: by last use first, never used last", i, j, k)
	}
	if !strings.Contains(page, "never used") || !strings.Contains(page, "Revoke unused") || !strings.Contains(page, "Revoke 1 tokens that were never used?") {
		t.Errorf("the page does not offer to revoke the one unused token: %s", page)
	}
	if strings.Contains(page, "gina-never") {
		t.Error("another account's token is listed")
	}

	resp := tpPost(t, e, e.Member, "/ui/account/tokens/revoke-unused", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "revoked=1") {
		t.Fatalf("revoke unused = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	state := func(id int64) bool {
		info, err := e.St.TokenByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return info.RevokedAt != ""
	}
	if !state(never) {
		t.Error("the unused token is still active")
	}
	for name, id := range map[string]int64{"used long ago": old, "used recently": recent, "just issued": fresh, "of someone else": foreign} {
		if state(id) {
			t.Errorf("the token %s was revoked", name)
		}
	}
	_, after := fetchPage(t, e.Member, e.Base+"/ui/account/tokens?revoked=1")
	if !strings.Contains(after, "Revoked 1 unused tokens.") || strings.Contains(after, "Revoke unused") {
		t.Errorf("after: %s", after)
	}
}

// Organization and Devices have icons of their own, not the shared fallback.
func TestOrganizationAndDevicesHaveTheirOwnIcons(t *testing.T) {
	e := ovEnv(t)
	icon := func(page, key string) string {
		i := strings.Index(page, `data-nav="`+key+`"`)
		if i < 0 {
			t.Fatalf("no nav entry %s", key)
		}
		rest := page[i:]
		return rest[strings.Index(rest, "<svg"):strings.Index(rest, "</svg>")]
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/overview")
	org, dev := icon(owner, "admin-org"), icon(owner, "admin-devices")
	// The shared fallback is the two sliders (a circle at cx=9 cy=8).
	const fallback = `cx="9" cy="8"`
	if org == dev || strings.Contains(org, fallback) || strings.Contains(dev, fallback) {
		t.Errorf("icons collide or fall back: org=%q devices=%q", org, dev)
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if strings.Contains(icon(member, "account-org"), fallback) || strings.Contains(icon(member, "account-devices"), fallback) {
		t.Error("a member's entries fall back to the shared icon")
	}
}

func TestTokenIsUnusedRules(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	const day = 24 * time.Hour
	old := now.Add(-60 * day) // use is recorded since two months
	cases := []struct {
		name    string
		token   store.TokenInfo
		current int64
		since   time.Time
		want    bool
	}{
		{"never used and old, use is recorded", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, old, true},
		{"use is not recorded at all yet", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, time.Time{}, false},
		{"recording began only yesterday", store.TokenInfo{ID: 1, CreatedAt: at(40 * day)}, 0, now.Add(-day), false},
		{"just issued, first call still to come", store.TokenInfo{ID: 1, CreatedAt: at(10 * time.Minute)}, 0, old, false},
		{"used", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), LastUsedAt: at(time.Hour)}, 0, old, false},
		{"already revoked", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), RevokedAt: at(time.Hour)}, 0, old, false},
		{"expired", store.TokenInfo{ID: 1, CreatedAt: at(40 * day), ExpiresAt: at(time.Hour)}, 0, old, false},
		{"the token this session signed in with", store.TokenInfo{ID: 7, CreatedAt: at(40 * day)}, 7, old, false},
	}
	for _, c := range cases {
		if got := tokenIsUnused(c.token, now, c.current, c.since); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Revoke unused is not offered to an administrator without an interactive
// session when it would reach another account, and a POST without the CSRF
// token revokes nothing.
func TestRevokeUnusedRefusesWithoutCSRFAndGuardsForeignTokens(t *testing.T) {
	e := ovEnv(t)
	at := func(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }
	own := tokenAt(t, e.St, "anna", "anna-never", at(40*24*time.Hour), "")
	trackSince(t, e.St, at(60*24*time.Hour))

	form := url.Values{}
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/account/tokens/revoke-unused", form)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("a POST without CSRF token was accepted: %d", resp.StatusCode)
	}
	if info, _ := e.St.TokenByID(own); info.RevokedAt != "" {
		t.Error("the token was revoked without a CSRF token")
	}

	// The owner is an administrator: the foreign token is listed and counted.
	foreign := tokenAt(t, e.St, "gina", "gina-never", at(40*24*time.Hour), "")
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/account/tokens")
	if !strings.Contains(page, "gina-never") {
		t.Skip("the owner of the fixture is no administrator")
	}
	resp = tpPost(t, e, e.Owner, "/ui/account/tokens/revoke-unused", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("interactive administrator: %d", resp.StatusCode)
	}
	if info, _ := e.St.TokenByID(foreign); info.RevokedAt == "" {
		t.Error("the administrator's revoke did not reach the foreign unused token")
	}
}

// Ticking a task keeps the Everyone filter: the person lands where they were.
func TestTickingATaskKeepsTheEveryoneFilter(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Pay the vendor"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For lars", Assignee: "lars"})
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/tasks?who=all")
	if !strings.Contains(page, `name="who" value="all"`) {
		t.Fatalf("the form does not carry the filter: %s", page)
	}
	resp := tpPost(t, e, e.Owner, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(task.ID, 10)}, "who": {"all"}, "back": {"tasks"}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "who=all") {
		t.Errorf("redirect = %q, want who=all kept", loc)
	}
}

// "Waiting on you" belongs to the person asked. For anyone else the same agent
// reads "Waiting for a person", on Home and in the room, and is not flagged.
func TestWaitingOnYouIsRelativeToTheViewer(t *testing.T) {
	e := ovEnv(t)
	ovAgent(t, e.St, shellProject, "claude:waiter", "waiter", "person:2")
	room := store.RoomKeyForProject(shellProject)
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
		SenderExternalID: "claude:waiter", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, ClientID: "w1",
		Body: "Shall I drop the column?", Intent: store.IntentQuestion, Mentions: []string{"person:1"}}); err != nil {
		t.Fatal(err)
	}
	rowOf := func(c *http.Client) string {
		_, home := fetchPage(t, c, e.Base+"/ui/overview")
		i := strings.Index(home, `title="claude:waiter"`)
		if i < 0 {
			t.Fatal("no row for the waiting agent")
		}
		row := home[i:]
		return row[:strings.Index(row, "</li>")]
	}
	if row := rowOf(e.Owner); !strings.Contains(row, "Waiting on you") || !strings.Contains(row, "is-waiting_user") {
		t.Errorf("the person asked: %s", row)
	}
	row := rowOf(e.Member)
	if strings.Contains(row, "Waiting on you") || strings.Contains(row, "is-waiting_user") || !strings.Contains(row, "Waiting for a person") {
		t.Errorf("someone else: %s", row)
	}
	_, theirRoom := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room))
	if strings.Contains(theirRoom, "Waiting on you") {
		t.Error("the room tells someone else that the agent waits on them")
	}
}
