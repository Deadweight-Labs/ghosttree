package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var (
	bellCountRE   = regexp.MustCompile(`<b class="top-count">(\d+)</b>`)
	allChipRE     = regexp.MustCompile(`data-f="alle"[^>]*>All<span class="counter">(\d+)</span>`)
	sectionRowsRE = regexp.MustCompile(`class="row nrow`)
)

// numberAt reads the first group of re in page as a number; -1 when it is not there.
func numberAt(page string, re *regexp.Regexp) int {
	m := re.FindStringSubmatch(page)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// WP-R5-8 (N13, G8): the bell, the chip "All" and the rows of the page are one number. A task that waits
// and a message said to the person are two rows, so the bell says 2 (it said 1 and left the message out).
func TestBellCountsTheRowsTheInboxListsUnderAll(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	room, alice := inboxDM(t, e)
	if _, err := alice.Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "dm1", Body: "Do you have a minute?"}); err != nil {
		t.Fatal(err)
	}

	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	rows := len(sectionRowsRE.FindAllString(inbox, -1))
	if rows != 2 {
		t.Fatalf("the inbox lists %d rows, want the task and the message:\n%s", rows, visibleText(inbox))
	}
	if bell, chip := numberAt(inbox, bellCountRE), numberAt(inbox, allChipRE); bell != rows || chip != rows {
		t.Errorf("the inbox lists %d rows, the bell says %d and the chip \"All\" %d", rows, bell, chip)
	}
	// Every other page carries the same bell, whatever repo it is about.
	for _, path := range []string{"/ui/knowledge", "/ui/x/shell/issues", "/ui/agents"} {
		_, page := fetchPage(t, e.Member, e.Base+path)
		if bell := numberAt(page, bellCountRE); bell != rows {
			t.Errorf("%s: the bell says %d, the inbox lists %d", path, bell, rows)
		}
	}
	// The people who have nothing waiting have no number.
	if _, page := fetchPage(t, e.Guest, e.Base+"/ui/inbox"); numberAt(page, bellCountRE) != -1 {
		t.Errorf("a guest with nothing waiting has a bell number: %d", numberAt(page, bellCountRE))
	}

	// Reading the message takes it out of the list and off the bell.
	_, _ = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	postForm(t, e, e.Member, "/ui/coord/read", map[string][]string{"back": {"inbox"}, "room": {room}, "sequence": {"1"}})
	_, after := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	rows = len(sectionRowsRE.FindAllString(after, -1))
	if bell := numberAt(after, bellCountRE); bell != rows {
		t.Errorf("after reading: %d rows, the bell says %d", rows, bell)
	}
}

// The bell leads to the inbox, and the inbox lists every repo unless the address names one: a repo that
// was browsed earlier is not a filter nobody set (R09, G6).
func TestInboxIsNotNarrowedByARememberedRepo(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})

	// Browsing a repo remembers it for the pages of that repo (the client keeps the cookie, as a browser does).
	member := remembering(t, e.Base, e.Member)
	_, repo := fetchPage(t, member, e.Base+"/ui/knowledge?project="+shellProject)
	if _, again := fetchPage(t, member, e.Base+"/ui/knowledge"); selectedOption(again) != shellProject {
		t.Fatal("the repo was not remembered for the other pages")
	}
	if !strings.Contains(repo, "shell") {
		t.Fatal("the fixture repo is not named on its page")
	}
	_, inbox := fetchPage(t, member, e.Base+"/ui/inbox")
	if !strings.Contains(inbox, `<summary class="gbtn">All repos`) {
		t.Errorf("the inbox is narrowed by the repo that was browsed:\n%.1500s", inbox)
	}
	if selectedOption(inbox) != "" {
		t.Error("the header names a repo although none was chosen for the inbox")
	}
	// An address that names the repo does narrow it, and the header says which.
	_, named := fetchPage(t, member, e.Base+"/ui/inbox?project="+shellProject)
	if strings.Contains(named, `<summary class="gbtn">All repos`) || selectedOption(named) != shellProject {
		t.Error("a repo named in the address does not narrow the inbox")
	}
	// The bell does not depend on it.
	if a, b := numberAt(inbox, bellCountRE), numberAt(repo, bellCountRE); a != b {
		t.Errorf("the bell says %d in the inbox and %d on the page of a repo", a, b)
	}
}

// G10: the header of the inbox names the organization the person works in.
func TestInboxHeaderNamesTheOrganization(t *testing.T) {
	e := ovEnv(t)
	_, inbox := fetchPage(t, e.Owner, e.Base+"/ui/inbox")
	m := regexp.MustCompile(`<div class="hcrumb">(.*?)</div>`).FindStringSubmatch(inbox)
	if m == nil {
		t.Fatal("no header line")
	}
	if !strings.Contains(m[1], "Inbox") {
		t.Errorf("the header does not say Inbox: %s", m[1])
	}
	if strings.Contains(inbox, `data-new="org"`) && !strings.Contains(m[1], `class="org"`) {
		t.Errorf("the person has an organization, the header does not name it: %s", m[1])
	}
}

// S14: the line of a task says who asked and which issue it hangs on, and a task that was just made is
// "just now", not "open for now".
func TestInboxTaskLineNamesTheAskerAndTheIssue(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	text := visibleText(inbox)
	if !strings.Contains(text, "Task on Ship it") {
		t.Errorf("the task line does not name the issue:\n%s", text)
	}
	if strings.Contains(text, "Task for Ship it") || strings.Contains(text, "open for now") {
		t.Errorf("the task line still reads \"Task for\" or \"open for now\":\n%s", text)
	}
	if !strings.Contains(inbox, "just now") {
		t.Errorf("a new task is not \"just now\":\n%s", text)
	}
	atNow(t, time.Now().Add(3*time.Hour))
	_, later := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(visibleText(later), "open for 3h") {
		t.Errorf("an old task does not say how long it is open:\n%s", visibleText(later))
	}
}

func TestOpenSinceSaysJustNowInTheFirstMinute(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, l := range []lang{langEN, langDE} {
		if got := openSince(l, now, now.Add(-20*time.Second)); got != l.msg("home.open_new") || strings.Contains(got, "%") {
			t.Errorf("%v: %q", l, got)
		}
		if got := openSince(l, now, now.Add(-5*time.Minute)); got != l.msg("home.open_since", "5m") && !strings.Contains(got, "5") {
			t.Errorf("%v: %q", l, got)
		}
	}
}

// The comma behind a mention stands next to the name: "Jonas Brandt, kannst du", not "Jonas Brandt , kannst du".
func TestPreviewDropsTheSpaceBetweenAMentionAndItsComma(t *testing.T) {
	names := map[string]string{"jonas": "Jonas Brandt"}
	for in, want := range map[string]string{
		"Siehe das. @jonas , kannst du das ansehen?": "Siehe das. Jonas Brandt, kannst du das ansehen?",
		"@jonas ; ok":           "Jonas Brandt; ok",
		"@jonas kannst du ,das": "Jonas Brandt kannst du ,das",
	} {
		if got := nameMentions(in, names); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// R08: a task the person reported done is "waiting for check", not a bare "reported done".
func TestReportedTaskNamesWhatHappensNext(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	postForm(t, e, e.Member, "/ui/tasks/done", map[string][]string{"id": {itoa(task.ID)}, "back": {"task"}, "confirm_open": {"1"}})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tasks/"+itoa(task.ID))
	if !strings.Contains(page, "Waiting for check") || strings.Contains(page, "Reported done") {
		t.Errorf("the task does not say it waits for a check:\n%s", visibleText(page))
	}
}

// G1, P13: when the session of an agent is over, the work on the issue is no longer "Working" - on the
// issue and on the package page.
func TestWorkLineOfAnEndedSessionIsNotWorking(t *testing.T) {
	f := newLiveFixture(t)
	f.hook(t, store.HookTool)
	_, page := fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if !strings.Contains(page, `title="Working"`) {
		t.Fatalf("the fixture does not work:\n%s", visibleText(page))
	}
	f.hook(t, store.HookSessionEnd)
	_, page = fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if strings.Contains(page, `title="Working"`) {
		t.Errorf("the ended session still works on the issue:\n%s", visibleText(page))
	}
	// A session that starts again works again.
	f.hook(t, store.HookTool)
	_, page = fetchPage(t, f.e.Owner, f.e.Base+f.issue)
	if !strings.Contains(page, `title="Working"`) {
		t.Error("a resumed session does not work again")
	}
}

func TestPackageSessionOfAnEndedRunIsNotWorking(t *testing.T) {
	e, pkg, _ := packageFixture(t)
	path := "/ui/x/shell/packages/" + itoa(pkg.ID)
	_, page := fetchPage(t, e.Owner, e.Base+path)
	if !strings.Contains(page, ">Working<") {
		t.Fatalf("the fixture does not work:\n%s", visibleText(page))
	}
	if _, err := e.St.RegisterCoordAgent(store.CoordAgent{ExternalID: "claude:build-1:pkg-sess", PrincipalID: "person:1", Person: "alice", Provider: "claude", DisplayName: "alice-agent",
		RoomKey: store.RoomKeyForProject(shellProject), Role: "member", SessionID: "pkg-sess", HookActivity: true, HookEvent: store.HookSessionEnd}); err != nil {
		t.Fatal(err)
	}
	_, page = fetchPage(t, e.Owner, e.Base+path)
	if strings.Contains(page, ">Working<") || !strings.Contains(page, "Session ended") {
		t.Errorf("the package page still says the ended session works:\n%s", visibleText(page))
	}
}

// P20: an inbox with nothing in it offers the next step, not only a line.
func TestEmptyInboxOffersTheNextStep(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Guest, e.Base+"/ui/inbox")
	if !strings.Contains(page, "Nothing is waiting for you.") || !strings.Contains(page, `href="/ui/inbox?done=1"`) || !strings.Contains(page, `href="/ui/requests"`) {
		t.Errorf("the empty inbox offers nothing to do:\n%s", visibleText(page))
	}
}
