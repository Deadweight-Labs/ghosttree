package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// REQ-679 AC-3: a report with steps still open asks first, and the question names
// how many; a report with nothing open goes straight through.
func TestReportWithOpenStepsAsksFirst(t *testing.T) {
	e, task, _ := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	_, page := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page", page, "2 steps still open – report anyway?", "Report anyway", "Report task as done", "Check off", "Send note")
	if strings.Contains(page, `<details class="cfm" open>`) {
		t.Error("the question is open before anybody tried to report")
	}

	// A report without the answer to the question (the tick in the inbox) goes to the question.
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"inbox"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != path+"?ask=done#report" {
		t.Fatalf("report with open steps -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Open {
		t.Fatalf("a report with open steps went through unasked: %+v", got)
	}
	_, asked := fetchPage(t, e.Member, e.Base+path+"?ask=done")
	mustContain(t, "task page asking", asked, `<details class="cfm" open>`, "2 steps still open – report anyway?", `name="confirm_open" value="1"`)

	// One step ticked: the question counts what is left.
	tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/1", url.Values{"done": {"1"}}).Body.Close()
	_, page = fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page after a tick", page, "One step still open – report anyway?")

	// The answer yes reports it.
	resp = tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}, "confirm_open": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != path {
		t.Fatalf("confirmed report -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Done {
		t.Fatalf("a confirmed report did not go through: %+v", got)
	}
}

func TestReportWithNothingOpenGoesThroughAtOnce(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "action", Title: "Restart the worker"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tasks/"+itoa(task.ID))
	if strings.Contains(page, "report anyway") || !strings.Contains(page, "Report task as done") {
		t.Errorf("a task without steps asks a question or lacks its button: %s", page)
	}
	tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}}).Body.Close()
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Done {
		t.Fatalf("a report with nothing open did not go through: %+v", got)
	}
}

// REQ-679 AC-3: a note to the agent goes without the report; the task stays open,
// the agent reads the note with the task, and the state "done" is no warning colour.
func TestNoteToTheAgentDoesNotReportTheTask(t *testing.T) {
	e, task, foreign := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	noteURL := "/ui/tasks/" + itoa(task.ID) + "/note"

	resp := tpPost(t, e, e.Member, noteURL, url.Values{"note": {"I found the vault, one more minute"}, "back": {"task"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), path) {
		t.Fatalf("note -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, err := e.St.TaskByID(task.ID)
	if err != nil || got.State != humantask.Open || len(got.Notes) != 1 || got.Notes[0].Text != "I found the vault, one more minute" || got.Notes[0].By == "" {
		t.Fatalf("task after the note: %+v err=%v", got, err)
	}
	_, page := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page", page, "I found the vault, one more minute", `id="notes"`, `action="`+noteURL+`"`)

	// Nothing to say, a secret, no token, a guest and a foreign task: nothing is written.
	if r := tpPost(t, e, e.Member, noteURL, url.Values{"note": {"  "}}); r.StatusCode == http.StatusSeeOther {
		t.Error("an empty note was taken")
	}
	if r := tpPost(t, e, e.Member, noteURL, url.Values{"note": {"token ghp_" + strings.Repeat("Ab1", 5)}}); r.StatusCode < 400 && r.StatusCode != http.StatusSeeOther {
		t.Errorf("a secret note: %d", r.StatusCode)
	}
	bare := sameOriginPostForm(t, e.Member, e.Base+noteURL, url.Values{"note": {"no token"}})
	bare.Body.Close()
	if bare.StatusCode != http.StatusForbidden {
		t.Errorf("without the CSRF token: %d, want 403", bare.StatusCode)
	}
	if r := tpPost(t, e, e.Guest, noteURL, url.Values{"note": {"guest"}}); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusForbidden {
		t.Errorf("guest: %d", r.StatusCode)
	}
	if r := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(foreign.ID)+"/note", url.Values{"note": {"foreign"}}); r.StatusCode != http.StatusNotFound {
		t.Errorf("foreign task: %d, want 404", r.StatusCode)
	}
	if after, _ := e.St.TaskByID(task.ID); len(after.Notes) != 1 {
		t.Errorf("%d notes after the refused ones, want 1", len(after.Notes))
	}
	if other, _ := e.St.TaskByID(foreign.ID); len(other.Notes) != 0 {
		t.Error("a foreign task got a note")
	}

	// Reported done: the pill is not the colour of a warning, and the page no longer takes notes.
	tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}, "confirm_open": {"1"}}).Body.Close()
	_, done := fetchPage(t, e.Member, e.Base+path)
	if strings.Contains(done, "p-warn") || !strings.Contains(done, "Waiting for check") {
		t.Errorf("the done state is a warning or unnamed: %s", done)
	}
	if r := tpPost(t, e, e.Member, noteURL, url.Values{"note": {"too late"}}); r.StatusCode == http.StatusSeeOther {
		t.Error("a task that is reported done took a note")
	}
}
