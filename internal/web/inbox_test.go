package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestRootAndIndexLeadToTheInbox(t *testing.T) {
	e := ovEnv(t)
	fillVisible(t, e.St)
	resp, err := e.Owner.Get(e.Base + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Errorf("/ui/ -> %d %q, want 303 /ui/inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The overview address leads to the inbox once the instance has content.
	resp, err = e.Owner.Get(e.Base + "/ui/overview")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/inbox" {
		t.Errorf("/ui/overview -> %d %q, want a redirect to the inbox", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestInboxHeaderLinkCarriesTheNumberWaiting(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(page, `data-top="inbox"`) || strings.Contains(page, `class="top-count"`) {
		t.Error("without anything waiting the inbox link shows no number")
	}
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Second thing"})
	for _, path := range []string{"/ui/inbox", "/ui/knowledge", "/ui/x/shell/issues"} {
		_, page = fetchPage(t, e.Member, e.Base+path)
		if !regexp.MustCompile(`class="top-count"[^>]*>2<`).MatchString(page) {
			t.Errorf("GET %s: the inbox link lacks the number 2", path)
		}
	}
}

func TestInboxListsWhatWaitsAndNothingHidden(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Pay the vendor"})
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden thing", Project: shellHiddenProject})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "secret", Title: "Create the vendor key", Location: "Agent Vault: vendor/prod"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Only for gina", Assignee: "gina"})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "SECRET-TASK"})

	status, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	for _, want := range []string{"Waiting for you", "Mentioned", "Create the vendor key", task.HumanID(), `action="/ui/tasks/done"`, "Done: leaves the inbox", `name="back" value="inbox"`} {
		if !strings.Contains(page, want) {
			t.Errorf("inbox lacks %q", want)
		}
	}
	for _, leak := range []string{"SECRET-TASK", "Only for gina", shellHiddenProject} {
		if strings.Contains(page, leak) {
			t.Errorf("inbox shows %q to a member", leak)
		}
	}
	// A guest marks no task done (Robin, 2026-11-09), so nothing waits for them,
	// not even the task assigned to them; they still read it on its page.
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/inbox")
	if strings.Contains(guest, "Only for gina") || strings.Contains(guest, "Create the vendor key") || strings.Contains(guest, "SECRET-TASK") {
		t.Errorf("guest inbox is wrong: %s", guest)
	}
}

func TestInboxEmptyIsOneLineAndOneAction(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(page, "Nothing is waiting for you.") || !strings.Contains(page, `href="/ui/overview">Open home<`) {
		t.Errorf("empty inbox: %s", page)
	}
	if strings.Contains(page, `class="gh-rows"`) || strings.Contains(page, "Nothing here.") {
		t.Error("an empty inbox is one line and one action, not two empty sections")
	}
}

func TestInboxTaskDoneFallsOutAndComesBackToTheInbox(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"inbox"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox?f=aufgaben" {
		t.Fatalf("done -> %d %q, want 303 /ui/inbox?f=aufgaben", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, "First thing") {
		t.Error("a finished task stays in the inbox")
	}
}

func TestInboxMentionsAreOneRowPerRoomAndMarkReadFallsOut(t *testing.T) {
	e := ovEnv(t)
	room := store.RoomKeyForProject(shellProject)
	ovAgent(t, e.St, shellProject, "claude:anna-box", "anna-agent", "person:2")
	for i, text := range []string{"@anna first question for you", "@anna second one"} {
		if _, err := e.St.AppendCoordMessage(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room,
			SenderExternalID: "person:1", ClientID: "m" + itoa(int64(i)), Body: text, Mentions: []string{"person:2"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if !strings.Contains(page, "2 unread mentions") || !strings.Contains(page, `href="/ui/coord?room=`+url.QueryEscape(room)+`"`) {
		t.Fatalf("mentions are not one row with the number: %s", page)
	}
	m := regexp.MustCompile(`name="room" value="([^"]+)"><input type="hidden" name="sequence" value="(\d+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no mark-as-read form")
	}
	resp := tpPost(t, e, e.Member, "/ui/coord/read", url.Values{"room": {room}, "sequence": {m[2]}, "back": {"inbox"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/inbox" {
		t.Fatalf("mark read -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, page = fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(page, "unread mention") {
		t.Error("a mention that was marked read stays in the inbox")
	}
}

func TestInboxShowsFinishedEntriesOnlyWhenAskedAndOnlyOnesTheViewerMayRead(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	mine := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Tick this off"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Still open"})
	hiddenReq := rqMake(t, e.St, rqSpec{Title: "Hidden work", Project: shellHiddenProject})
	secret := tpTask(t, e, shellHiddenProject, hiddenReq.Request.ID, humantask.CreateInput{Title: "SECRET-DONE-TASK"})
	tpPost(t, e, e.Owner, "/ui/tasks/done", url.Values{"id": {itoa(secret.ID)}, "back": {"inbox"}}).Body.Close()
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(mine.ID)}, "back": {"inbox"}})
	resp.Body.Close()

	_, plain := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	if strings.Contains(plain, "Tick this off") || !strings.Contains(plain, "Show done") || strings.Contains(plain, `id="done"`) {
		t.Error("the finished task shows without the switch, or the switch is missing")
	}
	if !strings.Contains(plain, `href="/ui/inbox?done=1"`) {
		t.Error("the switch does not lead to the finished entries")
	}
	_, withDone := fetchPage(t, e.Member, e.Base+"/ui/inbox?done=1")
	for _, want := range []string{`id="done"`, "Tick this off", "Still open", "ticked off", `aria-checked="true"`, `href="/ui/inbox"`} {
		if !strings.Contains(withDone, want) {
			t.Errorf("the inbox with finished entries lacks %q", want)
		}
	}
	if strings.Contains(withDone, "SECRET-DONE-TASK") || strings.Contains(withDone, "Hidden work") {
		t.Error("a finished entry of a project the viewer cannot read shows")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/inbox?done=1")
	if strings.Contains(guest, "Tick this off") {
		t.Error("a guest sees the finished task of the project")
	}
	_, narrowed := fetchPage(t, e.Member, e.Base+"/ui/inbox?done=1&f=fragen")
	if strings.Contains(narrowed, "Tick this off") {
		t.Error("the filter does not narrow the finished entries")
	}
}
