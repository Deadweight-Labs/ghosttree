package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

// noFollow sagt dem Client, Weiterleitungen nicht zu verfolgen: die Tests lesen
// den Statuscode und das Ziel selbst.
func noFollow(c *http.Client) *http.Client {
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

func locationOf(t *testing.T, c *http.Client, target string) (int, string) {
	t.Helper()
	resp, err := noFollow(c).Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

func taskFixture(t *testing.T) (shellEnv, humantask.Task, humantask.Task) {
	t.Helper()
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	shown := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "file", Title: "Upload the vendor key", Description: "The key is needed for the build.",
		Location: "vault item vendor-key", CheckHint: "The file exists at the location and is not empty.",
		Steps: []humantask.Step{{Text: "Open the vault", URL: "https://vault.example.org/items"}, {Text: "Copy the file name"}}})
	hiddenReq := rqMake(t, e.St, rqSpec{Title: "Secret work", Project: shellHiddenProject})
	foreign := tpTask(t, e, shellHiddenProject, hiddenReq.Request.ID, humantask.CreateInput{Title: "SECRET-TASK in another repository"})
	return e, shown, foreign
}

func TestTaskPageShowsStepsCheckHintAndTheFormThatMarksItDone(t *testing.T) {
	e, task, _ := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	status, page := fetchPage(t, e.Member, e.Base+path)
	if status != http.StatusOK {
		t.Fatalf("task page: %d", status)
	}
	mustContain(t, "task page", page, "Upload the vendor key", "The key is needed for the build.", "vault item vendor-key",
		"How the agent checks it", "The file exists at the location and is not empty.",
		"Steps", "Open the vault", `href="https://vault.example.org/items"`, "Copy the file name", "0 of 2 done",
		`action="/ui/tasks/done"`, `name="back" value="task"`, `name="note"`, "Mark as done", `href="/ui/x/shell/issues/`)
	if strings.Contains(page, `name="value"`) {
		t.Error("a task form must not carry a value field")
	}
	// The form on the page leads back to the page.
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "note": {"it is there"}, "back": {"task"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != path {
		t.Fatalf("done from the page -> %d %q, want 303 %s", resp.StatusCode, resp.Header.Get("Location"), path)
	}
	_, after := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page after", after, "Done", "it is there")
	if strings.Contains(after, `action="/ui/tasks/done"`) {
		t.Error("a task that is done still offers the form")
	}
}

func TestTaskListOfTheRepositoryAndMyTasksLinkToTheCanonicalAddress(t *testing.T) {
	e, task, foreign := taskFixture(t)
	canonical := `href="/ui/x/shell/tasks/` + itoa(task.ID) + `"`
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/tasks")
	mustContain(t, "task list", list, "Upload the vendor key", canonical, `>1</span> Open`, `>0</span> Finished`)
	if strings.Contains(list, "SECRET-TASK") {
		t.Error("the list names a task of another repository")
	}
	_, mine := fetchPage(t, e.Owner, e.Base+"/ui/u/tasks")
	mustContain(t, "my tasks", mine, "Waiting for you", "Upload the vendor key", canonical, "SECRET-TASK in another repository", `href="/ui/x/hidden/tasks/`+itoa(foreign.ID)+`"`)
	_, memberMine := fetchPage(t, e.Member, e.Base+"/ui/u/tasks")
	mustContain(t, "my tasks of a member", memberMine, "Upload the vendor key")
	if strings.Contains(memberMine, "SECRET") {
		t.Error("my tasks names a task of a repository the viewer has no role in")
	}
	// The repository tab leads to the list.
	_, code := fetchPage(t, e.Member, e.Base+"/ui/x/shell")
	mustContain(t, "repository band", code, `href="/ui/x/shell/tasks"`, `href="/ui/x/shell/packages"`)
}

func TestTaskRightsStrangerGuestMemberAndForeignNumbers(t *testing.T) {
	e, task, foreign := taskFixture(t)
	own := "/ui/x/shell/tasks/" + itoa(task.ID)
	hiddenPath := "/ui/x/hidden/tasks/" + itoa(foreign.ID)

	// A member without a role in the other repository: it is unknown, in its own
	// address, in the address of the other repository and in the short form.
	var want string
	for _, path := range []string{hiddenPath, "/ui/x/shell/tasks/" + itoa(foreign.ID), "/ui/t/" + itoa(foreign.ID), "/ui/x/hidden/tasks", "/ui/x/shell/tasks/999999"} {
		status, body := fetchPage(t, e.Member, e.Base+path)
		if status != http.StatusNotFound {
			t.Errorf("member at %s: %d, want 404", path, status)
		}
		if strings.Contains(body, "SECRET") {
			t.Errorf("member at %s sees the foreign task", path)
		}
		if want == "" {
			want = body
		}
	}
	// The same answer as for a repository that does not exist.
	if _, unknown := fetchPage(t, e.Member, e.Base+"/ui/x/nothere/tasks/"+itoa(foreign.ID)); stripCSRF(unknown) != stripCSRF(want) {
		t.Error("a foreign task answers differently from an unknown address")
	}

	// A guest sees only the tasks assigned to them.
	if status, _ := fetchPage(t, e.Guest, e.Base+own); status != http.StatusNotFound {
		t.Errorf("guest at a task of everyone: %d, want 404", status)
	}
	if _, g := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/tasks"); strings.Contains(g, "Upload the vendor key") {
		t.Error("a guest's list names a task that is not theirs")
	}
	if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/t/"+itoa(task.ID)); status != http.StatusNotFound {
		t.Errorf("guest at the short form: %d, want 404", status)
	}

	// The owner has both repositories: the number in the wrong repository leads to
	// the right one, the short form too, and the target is a built page.
	for _, c := range []struct{ from, to string }{
		{"/ui/x/hidden/tasks/" + itoa(task.ID), own},
		{"/ui/t/" + itoa(task.ID), own},
		{"/ui/t/" + itoa(foreign.ID), hiddenPath},
	} {
		status, loc := locationOf(t, e.Owner, e.Base+c.from)
		if status != http.StatusFound || loc != c.to {
			t.Errorf("%s -> %d %q, want 302 %s", c.from, status, loc, c.to)
			continue
		}
		if status, _ := fetchPage(t, e.Owner, e.Base+loc); status != http.StatusOK {
			t.Errorf("the target %s answers %d", loc, status)
		}
	}
	// A request number leads to the page of its issue.
	status, loc := locationOf(t, e.Member, e.Base+"/ui/r/"+itoa(task.RequestID))
	if status != http.StatusFound || loc != "/ui/x/shell/issues/"+itoa(task.RequestID) {
		t.Errorf("/ui/r/n -> %d %q", status, loc)
	}
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/r/999999"); status != http.StatusNotFound {
		t.Errorf("unknown request: %d", status)
	}
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/t/nonsense"); status != http.StatusNotFound {
		t.Errorf("a number that is none: %d", status)
	}
	// The personal issue list is the issues section of the inbox.
	if status, loc := locationOf(t, e.Member, e.Base+"/ui/u/issues"); status != http.StatusFound || loc != "/ui/inbox?f=issues" {
		t.Errorf("/ui/u/issues -> %d %q", status, loc)
	}
}

func TestTaskPageNeedsNoWriteRightToRead(t *testing.T) {
	e, task, _ := taskFixture(t)
	for name, c := range map[string]*http.Client{"owner": e.Owner, "lead": e.Lead, "reviewer": e.Reviewer} {
		if status, _ := fetchPage(t, c, e.Base+"/ui/x/shell/tasks/"+itoa(task.ID)); status != http.StatusOK {
			t.Errorf("%s: %d", name, status)
		}
	}
	// The old list address leads on to the tasks of the inbox.
	if status, loc := locationOf(t, e.Owner, e.Base+"/ui/tasks?project="+url.QueryEscape(shellProject)); status != http.StatusFound || !strings.HasPrefix(loc, "/ui/inbox?f=aufgaben") {
		t.Errorf("old list address: %d %q", status, loc)
	}
}

// The old address /ui/tasks#task-N leads to the inbox with the anchor; the script of
// the inbox takes the anchor on to the page of the task, whose address /ui/t/N leads
// to the repository.
func TestOldTaskAnchorLeadsToThePageOfTheTask(t *testing.T) {
	e, task, _ := taskFixture(t)
	_, inbox := fetchPage(t, e.Member, e.Base+"/ui/inbox?f=aufgaben")
	if !strings.Contains(inbox, `/static/ui-inbox.js`) {
		t.Fatal("the inbox does not load its script")
	}
	_, script := fetchPage(t, e.Member, e.Base+"/static/ui-inbox.js")
	mustContain(t, "inbox script", script, `#task-(\d+)`, `/ui/t/`)
	if status, loc := locationOf(t, e.Member, e.Base+"/ui/t/"+itoa(task.ID)); status != http.StatusFound || loc != "/ui/x/shell/tasks/"+itoa(task.ID) {
		t.Errorf("/ui/t/n -> %d %q", status, loc)
	}
}

func TestStepsOfATaskAreTickedFromThePageWithOneClick(t *testing.T) {
	e, task, foreign := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	_, page := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page", page, `action="/ui/tasks/`+itoa(task.ID)+`/steps/1"`, `action="/ui/tasks/`+itoa(task.ID)+`/steps/2"`, `name="done" value="1"`, "Mark done", "0 of 2 done")

	resp := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/1", url.Values{"done": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != path+"#step-1" {
		t.Fatalf("tick -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, after := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "task page after the tick", after, "1 of 2 done", `cbx on`, "anna", `name="done" value="0"`, "Undo")
	// Taking the tick back.
	tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/1", url.Values{"done": {"0"}}).Body.Close()
	if _, again := fetchPage(t, e.Member, e.Base+path); !strings.Contains(again, "0 of 2 done") {
		t.Error("the tick was not taken back")
	}

	// Without the token of the form nothing happens; a step that does not exist is a conflict.
	bare := sameOriginPostForm(t, e.Member, e.Base+"/ui/tasks/"+itoa(task.ID)+"/steps/2", url.Values{"done": {"1"}})
	bare.Body.Close()
	if bare.StatusCode != http.StatusForbidden {
		t.Errorf("without the CSRF token: %d, want 403", bare.StatusCode)
	}
	if r := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/9", url.Values{"done": {"1"}}); r.StatusCode != http.StatusConflict {
		t.Errorf("a step that does not exist: %d, want 409", r.StatusCode)
	}
	// A guest has no right to do a task of everyone; a foreign task is unknown to a member.
	if r := tpPost(t, e, e.Guest, "/ui/tasks/"+itoa(task.ID)+"/steps/1", url.Values{"done": {"1"}}); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusForbidden {
		t.Errorf("guest: %d", r.StatusCode)
	}
	if r := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(foreign.ID)+"/steps/1", url.Values{"done": {"1"}}); r.StatusCode != http.StatusNotFound {
		t.Errorf("foreign task: %d, want 404", r.StatusCode)
	}
	if marks, _ := e.St.TaskStepMarks(foreign.ID); len(marks) != 0 {
		t.Error("a foreign task got a tick")
	}
	// A task that is done keeps its ticks and shows no button.
	tick := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/2", url.Values{"done": {"1"}})
	tick.Body.Close()
	tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}}).Body.Close()
	_, closed := fetchPage(t, e.Member, e.Base+path)
	mustContain(t, "closed task", closed, "1 of 2 done")
	if strings.Contains(closed, `/steps/`) {
		t.Error("a task that is done still offers to tick steps")
	}
	if r := tpPost(t, e, e.Member, "/ui/tasks/"+itoa(task.ID)+"/steps/1", url.Values{"done": {"1"}}); r.StatusCode != http.StatusConflict {
		t.Errorf("tick on a done task: %d, want 409", r.StatusCode)
	}
}
