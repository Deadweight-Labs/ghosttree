package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestGardenerHarnessNameMatchesTheGardener(t *testing.T) {
	if gardenerHarness != garden.Harness {
		t.Fatalf("web reads harness %q, the gardener writes %q", gardenerHarness, garden.Harness)
	}
}

// gardenNote files a note the way the gardener does.
func gardenNote(t *testing.T, st *store.Store, project string, id int, label, subject, reason string) int64 {
	t.Helper()
	k, ok := garden.Proposal(project, garden.Verdict{Kind: garden.Stale, Reason: reason, Entry: garden.Entry{ID: int64(id), Title: subject}})
	if !ok {
		t.Fatal("no proposal")
	}
	k.Person = "gardener"
	if label == "Korrektur?" {
		k.Title = strings.Replace(k.Title, "Veraltet?", "Korrektur?", 1)
	}
	got, err := st.InsertKnowledge(k)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func backdate(t *testing.T, st *store.Store, table string, id int64, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := st.DB().Exec(`UPDATE `+table+` SET created_at=? WHERE id=?`, at, id); err != nil {
		t.Fatal(err)
	}
}

func rowsOf(page, class string) []string {
	var out []string
	for _, part := range strings.Split(page, `<li class="`+class)[1:] {
		if i := strings.Index(part, "</li>"); i >= 0 {
			part = part[:i]
		}
		out = append(out, part)
	}
	return out
}

// "For you" is one list: what holds an agent up first, then tasks, then ONE row
// for the gardener and ONE for the proposals of agents.
func TestHomeForYouIsOneListInTheOrderOfWhatBlocks(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Pay the vendor"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "secret", Title: "Create the vendor key", Location: "Agent Vault: vendor/prod"})
	backdate(t, e.St, "human_tasks", task.ID, 4*24*time.Hour)
	for _, title := range []string{"P one", "P two", "P three"} {
		ovKnowledge(t, e.St, shellProject, title, "staged")
	}
	gardenNote(t, e.St, shellProject, 11, "Veraltet?", "Old entry A", "The file moved.")
	gardenNote(t, e.St, shellProject, 12, "Veraltet?", "Old entry B", "The flag is gone.")
	ovAgent(t, e.St, shellProject, "claude:asker", "asker", "person:2")
	if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: store.RoomKeyForProject(shellProject),
		SenderExternalID: "claude:asker", AuthorPrincipalID: "person:2", AuthorKind: store.AuthorAgent, ClientID: "q1",
		Body: "Which of the two schemas?", Intent: store.IntentQuestion, Mentions: []string{"person:5"}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Reviewer, e.Base+"/ui/overview")
	section := page[strings.Index(page, `id="for-you"`):]
	section = section[:strings.Index(section, `id="running"`)]
	rows := rowsOf(section, "g-row")
	if len(rows) != 4 {
		t.Fatalf("want question, task, one gardener row, one proposals row; got %d: %s", len(rows), section)
	}
	for i, want := range []string{"Which of the two schemas?", "Create the vendor key", "2 notes on outdated knowledge", "3 knowledge proposals are waiting for you"} {
		if !strings.Contains(rows[i], want) {
			t.Errorf("row %d lacks %q: %s", i, want, rows[i])
		}
	}
	if !strings.HasPrefix(rows[0], ` is-open"`) || strings.Contains(rows[1], "is-open") {
		t.Error("only the first row is the unfolded one")
	}
	if !strings.Contains(rows[1], `class="g-age is-old"`) {
		t.Errorf("a task of four days is not marked old: %s", rows[1])
	}
	for _, title := range []string{"P one", "P two", "Old entry A", "Old entry B"} {
		if strings.Contains(page, title) {
			t.Errorf("%q is listed on its own", title)
		}
	}
	if !strings.Contains(rows[2], `href="/ui/review?source=gardener"`) || !strings.Contains(rows[3], `href="/ui/review"`) {
		t.Error("the bundles do not link to their overviews")
	}
	for i, row := range rows {
		if n := strings.Count(row, `class="g-btn`); n != 1 {
			t.Errorf("row %d has %d actions, want one", i, n)
		}
	}
	if !strings.Contains(rows[0], "g-btn--primary") || strings.Contains(rows[1], "g-btn--primary") {
		t.Error("only the row that holds an agent up has the filled action")
	}
}

// Only what the viewer may read and verify reaches a row, a count or a tab.
func TestHomeForYouCountsOnlyWhatTheViewerMaySee(t *testing.T) {
	e := ovEnv(t)
	gardenNote(t, e.St, shellProject, 21, "Veraltet?", "Visible entry", "Gone.")
	gardenNote(t, e.St, shellHiddenProject, 22, "Veraltet?", "SECRET-ENTRY", "Gone too.")
	ovKnowledge(t, e.St, shellHiddenProject, "SECRET-PROPOSAL", "staged")
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden thing", Project: shellHiddenProject})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "SECRET-TASK"})
	_, page := fetchPage(t, e.Reviewer, e.Base+"/ui/overview")
	if !strings.Contains(page, "Gardener report: 1 note on outdated knowledge") {
		t.Errorf("the report does not count its one visible note: %s", page)
	}
	for _, leak := range []string{"SECRET", shellHiddenProject, "knowledge proposals are waiting"} {
		if strings.Contains(page, leak) {
			t.Errorf("reviewer sees %q", leak)
		}
	}
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		page := fetchBody(t, c, e.Base+"/ui/overview")
		if strings.Contains(page, "Gardener report") || strings.Contains(page, "source=gardener") || strings.Contains(page, "SECRET") {
			t.Errorf("%s is offered the review bundles or a hidden entry", name)
		}
	}
}

// Mine is what is assigned to the viewer plus what is for everyone; another
// person's tasks are on that person's tab, everything on "All". Nobody else
// to switch to: no tabs.
func TestHomeForYouTabsMineOtherPersonAndAll(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For everyone"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For gina only", Assignee: "gina"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For anna only", Assignee: "anna"})

	_, mine := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(mine, "For everyone") || !strings.Contains(mine, "For anna only") || strings.Contains(mine, "For gina only") {
		t.Errorf("the default list is not mine plus everyone: %s", mine)
	}
	if !strings.Contains(mine, `aria-label="For whom"`) || !strings.Contains(mine, ">gina</a>") || !strings.Contains(mine, ">All</a>") {
		t.Error("the switch lacks the other person or All")
	}
	_, gina := fetchPage(t, e.Member, e.Base+"/ui/overview?wer=3")
	if !strings.Contains(gina, "For gina only") || strings.Contains(gina, "For anna only") || strings.Contains(gina, "For everyone") {
		t.Errorf("one person's tab shows more than that person's tasks: %s", gina)
	}
	_, all := fetchPage(t, e.Member, e.Base+"/ui/overview?wer=alle")
	for _, want := range []string{"For everyone", "For gina only", "For anna only"} {
		if !strings.Contains(all, want) {
			t.Errorf("All lacks %q", want)
		}
	}
	_, odd := fetchPage(t, e.Member, e.Base+"/ui/overview?wer=999")
	if strings.Contains(odd, "For gina only") || !strings.Contains(odd, "For anna only") {
		t.Error("an unknown person is not answered with the viewer's own list")
	}
	// Gina sees only what is assigned to her, so there is nobody to switch to.
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/overview")
	if !strings.Contains(guest, "For gina only") || strings.Contains(guest, "For anna only") || strings.Contains(guest, `class="g-seg"`) {
		t.Errorf("guest: %s", guest)
	}
}

func TestHomeForYouEmptyIsOneLine(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "Just a request"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	section := page[strings.Index(page, `id="for-you"`):]
	section = section[:strings.Index(section, "</section>")]
	if !strings.Contains(section, "Nothing waiting on you") || strings.Contains(section, "<li") || strings.Contains(section, "g-btn") {
		t.Errorf("empty list: %s", section)
	}
}

// "Running": by project, the state in words and in the dot from the same
// place, a hanging agent with its time, and quiet agents of more than a day
// only counted.
func TestHomeRunningGroupsByProjectAndDotAndWordsAgree(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	register := func(id, event string) {
		t.Helper()
		if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: id, PrincipalID: "person:2", Person: "anna", Provider: "claude",
			DisplayName: id, RoomKey: room, HookActivity: true, HookEvent: event}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"claude:midturn", "claude:oldquiet"} {
		register(id, store.HookTurnStart)
		register(id, store.HookTurnEnd)
		register(id, store.HookTurnStart)
	}
	past := time.Now().UTC().Add(-(store.StuckAfter + 24*time.Minute)).Format(time.RFC3339)
	if _, err := e.St.DB().Exec(`UPDATE coord_agents SET last_hook_at=? WHERE external_id='claude:midturn'`, past); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-30 * time.Hour).Format(time.RFC3339)
	if _, err := e.St.DB().Exec(`UPDATE coord_agents SET last_hook_at=?, last_seen_at=? WHERE external_id='claude:oldquiet'`, old, old); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	rows := rowsOf(page, "g-agent")
	var stuck string
	for _, row := range rows {
		if strings.Contains(row, `title="claude:midturn"`) {
			stuck = row
		}
		if strings.Contains(row, "oldquiet") {
			t.Errorf("a quiet agent of more than a day is listed: %s", row)
		}
	}
	if !strings.Contains(stuck, "Stuck? for 34m") || !strings.Contains(stuck, `class="g-dot is-stuck"`) || strings.Contains(stuck, "is-work") {
		t.Errorf("a hanging agent says one thing in words and another in the dot: %s", stuck)
	}
	if !strings.Contains(stuck, `class="g-btn clay" href="/ui/coord?room=`) {
		t.Error("a hanging agent has no way to its room")
	}
	if !strings.Contains(page, "and 1 quiet agent") || !strings.Contains(page, `href="/ui/agents"`) {
		t.Error("the quiet agent is neither counted nor linked")
	}
	if strings.Count(page, `class="g-group-head"`) != 1 || !strings.Contains(page, `<span>shell</span>`) {
		t.Error("agents are not grouped under their project")
	}
}

func TestHomeThisWeekIsOneSentenceOrNothing(t *testing.T) {
	e := ovEnv(t)
	_, empty := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if strings.Contains(empty, "This week") {
		t.Error("a week without news has a section")
	}
	ovRequest(t, e.St, shellProject, "Visible request", 4, 1)
	ovRequest(t, e.St, shellHiddenProject, "SECRET-REQ", 3, 3)
	ovKnowledge(t, e.St, shellProject, "Lesson one", "trusted")
	ovKnowledge(t, e.St, shellProject, "Lesson two", "trusted")
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(page, "Visible request is at 1 of 4 criteria. 2 new lessons.") {
		t.Errorf("the sentence is not there: %s", page)
	}
	if strings.Contains(page, "SECRET") || strings.Contains(page, "3 of 3") {
		t.Error("a hidden request is in the sentence")
	}
	if strings.Contains(page, "Lesson one") {
		t.Error("lessons are listed again")
	}
}

func TestHomeSpeaksGermanAndEnglishAndNeverBoth(t *testing.T) {
	for _, tc := range []struct {
		lang        string
		want, avoid []string
	}{
		{"de", []string{"Für dich", "Läuft", "Nichts wartet"}, []string{"For you", "Running", "Nothing waiting"}},
		{"en", []string{"For you", "Running", "Gardener report"}, []string{"Für dich", "Läuft", "Nichts wartet"}},
	} {
		old := testWebOptions
		testWebOptions = append(append([]Option{}, old...), WithLanguage(tc.lang))
		e := ovEnv(t)
		testWebOptions = old
		ovAgent(t, e.St, shellProject, "claude:a", "agent-a", "person:2")
		ovKnowledge(t, e.St, shellProject, "P one", "staged")
		gardenNote(t, e.St, shellProject, 31, "Veraltet?", "Entry", "Gone.")
		_, page := fetchPage(t, e.Reviewer, e.Base+"/ui/overview")
		for _, w := range tc.want {
			if tc.lang == "de" && w == "Nichts wartet" {
				continue // there is something waiting in this seed
			}
			if !strings.Contains(page, w) {
				t.Errorf("%s: lacks %q", tc.lang, w)
			}
		}
		for _, w := range tc.avoid {
			if strings.Contains(page, w) {
				t.Errorf("%s: shows %q", tc.lang, w)
			}
		}
		if tc.lang == "de" && (!strings.Contains(page, "Gärtner-Bericht: 1 Hinweis auf veraltetes Wissen") || !strings.Contains(page, "1 Wissensvorschlag wartet auf dich")) {
			t.Errorf("German bundles: %s", page)
		}
		_, review := fetchPage(t, e.Reviewer, e.Base+"/ui/review")
		if tc.lang == "de" && (!strings.Contains(review, "1 Hinweis auf Wissen, das veraltet sein könnte") || !strings.Contains(review, "Alle 1 freigeben") || strings.Contains(review, "Approve all")) {
			t.Errorf("German review: %s", review)
		}
	}
}

// ---- Review: the gardener's report and the bulk decision ----

func gardenReviewEnv(t *testing.T) (shellEnv, []int64, int64) {
	t.Helper()
	e := ovEnv(t)
	ids := []int64{
		gardenNote(t, e.St, shellProject, 41, "Veraltet?", "Roles table docs", "The table was renamed in migration 0042."),
		gardenNote(t, e.St, shellProject, 42, "Korrektur?", "Backup schedule", "The job runs at 03:00 now."),
	}
	hidden := gardenNote(t, e.St, shellHiddenProject, 43, "Veraltet?", "SECRET-ENTRY", "Gone.")
	ovKnowledge(t, e.St, shellProject, "Agent proposal", "staged")
	return e, ids, hidden
}

func TestReviewLeadsWithTheGardenersReportAndOffersOnlyWhatMayBeDone(t *testing.T) {
	e, ids, _ := gardenReviewEnv(t)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{"2 notes on knowledge that may be outdated", "Roles table docs", "Backup schedule", "The table was renamed in migration 0042.", "Outdated?", "Correction?", "Reject all", "Approve all 2", `id="gbulk"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if strings.Index(page, "Roles table docs") > strings.Index(page, "Agent proposal") {
		t.Error("the gardener does not come first")
	}
	if strings.Contains(page, "SECRET") {
		t.Error("a hidden note is in the report")
	}
	if strings.Contains(page, "[Gärtner]") {
		t.Error("the raw title of the note is shown")
	}
	for _, id := range ids {
		if !strings.Contains(page, `action="/ui/review/`+strconv.FormatInt(id, 10)+`/approve"`) {
			t.Errorf("no approve form for note %d", id)
		}
	}
	// A reviewer may approve but not reject: the page offers no rejecting.
	_, reviewer := fetchPage(t, e.Reviewer, e.Base+"/ui/review")
	if !strings.Contains(reviewer, "Approve all 2") || strings.Contains(reviewer, "Reject all") || strings.Contains(reviewer, "/reject") {
		t.Error("a reviewer is offered to reject, or not to approve")
	}
	_, only := fetchPage(t, e.Lead, e.Base+"/ui/review?source=gardener")
	if strings.Contains(only, "Agent proposal") || !strings.Contains(only, "Roles table docs") {
		t.Error("the gardener filter does not narrow the page")
	}
	_, member := fetchPage(t, e.Member, e.Base+"/ui/review")
	if strings.Contains(member, "Approve all") || strings.Contains(member, `id="gbulk"`) {
		t.Error("a member is offered bulk buttons")
	}
}

func entryState(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	k, err := st.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return k.Confidence + "/" + k.Status
}

func TestReviewBulkIsAllOrNothingAndChecksEveryEntry(t *testing.T) {
	e, ids, hidden := gardenReviewEnv(t)
	post := func(c *http.Client, form url.Values) int {
		t.Helper()
		form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/review"))
		resp := sameOriginPostForm(t, c, e.Base+"/ui/review/bulk", form)
		resp.Body.Close()
		return resp.StatusCode
	}
	idv := func(list ...int64) []string {
		var out []string
		for _, id := range list {
			out = append(out, strconv.FormatInt(id, 10))
		}
		return out
	}
	// A note the reviewer may not read: the whole batch answers "not found", none changes.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids[0], hidden)}); code != http.StatusNotFound {
		t.Errorf("a batch with a hidden note = %d, want 404", code)
	}
	if got := entryState(t, e.St, ids[0]); got != "staged/active" {
		t.Errorf("a refused batch changed an entry: %s", got)
	}
	// A plain member may not verify.
	if code := post(e.Member, url.Values{"verdict": {"approve"}, "id": idv(ids...)}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("member bulk = %d", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/active" {
		t.Errorf("a member changed an entry: %s", got)
	}
	// Without CSRF nothing happens.
	resp := sameOriginPostForm(t, e.Reviewer, e.Base+"/ui/review/bulk", url.Values{"verdict": {"approve"}, "id": idv(ids...)})
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusOK {
		t.Errorf("bulk without a token = %d", resp.StatusCode)
	}
	// A verdict that does not exist.
	if code := post(e.Reviewer, url.Values{"verdict": {"delete"}, "id": idv(ids...)}); code != http.StatusNotFound {
		t.Errorf("unknown verdict = %d", code)
	}
	// The reviewer approves one note.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids[0])}); code != http.StatusSeeOther {
		t.Fatalf("bulk approve = %d", code)
	}
	if got := entryState(t, e.St, ids[0]); got != "verified/active" {
		t.Errorf("approved note = %s", got)
	}
	// A batch with a note that is decided already is refused whole.
	if code := post(e.Reviewer, url.Values{"verdict": {"approve"}, "id": idv(ids...)}); code != http.StatusConflict {
		t.Errorf("a batch with a decided note = %d, want 409", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/active" {
		t.Errorf("a refused batch changed the other entry: %s", got)
	}
	// The open one alone goes through.
	if code := post(e.Reviewer, url.Values{"verdict": {"reject"}, "id": idv(ids[1])}); code != http.StatusForbidden {
		t.Errorf("a reviewer rejecting in bulk = %d, want 403", code)
	}
	if code := post(e.Lead, url.Values{"verdict": {"reject"}, "id": idv(ids[1]), "source": {"gardener"}}); code != http.StatusSeeOther {
		t.Fatalf("bulk reject = %d", code)
	}
	if got := entryState(t, e.St, ids[1]); got != "staged/deprecated" {
		t.Errorf("rejected note = %s", got)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/review?done=bulk_reject&n=1&source=gardener")
	if !strings.Contains(page, "1 notes rejected") {
		t.Errorf("no result line after the bulk decision: %s", page)
	}
	if entryState(t, e.St, hidden) != "staged/active" {
		t.Error("the hidden note changed")
	}
}
