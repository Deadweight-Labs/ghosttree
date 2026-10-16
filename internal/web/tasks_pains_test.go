package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func tpOwner(e shellEnv) *store.ProjectAccess {
	return e.St.Access(store.Principal{ID: "person:1", Label: "alice"})
}

func tpTask(t *testing.T, e shellEnv, project string, reqID int64, in humantask.CreateInput) humantask.Task {
	t.Helper()
	in.Project, in.RequestID = project, reqID
	if in.Assignee == "" {
		in.Everyone = true
	}
	if in.Kind == "" {
		in.Kind = "action"
	}
	task, err := tpOwner(e).CreateHumanTask(in, humantask.Actor{AccountID: 1, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func tpPost(t *testing.T, e shellEnv, c *http.Client, path string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
	return sameOriginPostForm(t, c, e.Base+path, form)
}

func TestWaitingBlockShowsOpenTasksForTheViewerAndNothingElse(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Pay the vendor"})
	hidden := rqMake(t, e.St, rqSpec{Title: "Hidden thing", Project: shellHiddenProject})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "secret", Title: "Create the vendor key", Location: "Agent Vault: vendor/prod"})
	tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Only for gina", Assignee: "gina"})
	tpTask(t, e, shellHiddenProject, hidden.Request.ID, humantask.CreateInput{Title: "SECRET-TASK"})

	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if !strings.Contains(page, "Waiting on you") || !strings.Contains(page, "Create the vendor key") || !strings.Contains(page, "Agent Vault: vendor/prod") {
		t.Errorf("member does not see the open task: %s", page)
	}
	if strings.Contains(page, "SECRET-TASK") {
		t.Error("task of a hidden project on the overview")
	}
	if strings.Contains(page, "Only for gina") {
		t.Error("a task assigned to somebody else is offered as one to do")
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/overview")
	if !strings.Contains(guest, "Only for gina") || strings.Contains(guest, "Create the vendor key") {
		t.Errorf("guest must see exactly the assigned task: %s", guest)
	}
	if code, page := fetchPage(t, e.Reviewer, e.Base+"/ui/overview?project="+url.QueryEscape(shellHiddenProject)); code == http.StatusOK && strings.Contains(page, "SECRET-TASK") {
		t.Error("explicit hidden project leaks a task")
	}
}

// The block is where a person looks for what waits on them; without tasks it
// says so in one line instead of disappearing, and it links to the task list.
func TestWaitingBlockIsThereWithoutTasks(t *testing.T) {
	e := ovEnv(t)
	rqMake(t, e.St, rqSpec{Title: "Just a request"})
	for name, c := range map[string]*http.Client{"member": e.Member, "guest": e.Guest} {
		_, page := fetchPage(t, c, e.Base+"/ui/overview")
		if !strings.Contains(page, "ov-waiting") || !strings.Contains(page, "Nothing waiting on you") || !strings.Contains(page, `href="/ui/tasks`) {
			t.Errorf("%s: the block must show, empty, with the link: %s", name, page)
		}
		if strings.Contains(page, `type="checkbox"`) {
			t.Errorf("%s: an empty block has nothing to tick", name)
		}
	}
}

func TestMarkSeveralTasksDoneFromTheOverviewForm(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	a := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	b := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Second thing"})
	c := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Third thing"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/overview")
	if n := strings.Count(page, `type="checkbox"`); n != 3 {
		t.Fatalf("expected three checkboxes, got %d", n)
	}
	if strings.Contains(page, `name="value"`) {
		t.Error("a task form must not carry a value field")
	}
	form := url.Values{"id": {strconv.FormatInt(a.ID, 10), strconv.FormatInt(b.ID, 10)}, "note": {"both are in place"}, "back": {"overview"}}
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("done = %d", resp.StatusCode)
	}
	for _, id := range []int64{a.ID, b.ID} {
		got, err := e.St.TaskByID(id)
		if err != nil || got.State != humantask.Done || got.DoneNote != "both are in place" || got.DoneVia != "web" {
			t.Errorf("task %d = %+v, %v", id, got, err)
		}
	}
	if got, _ := e.St.TaskByID(c.ID); got.State != humantask.Open {
		t.Errorf("an untouched task changed: %+v", got)
	}
	_, list := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	if !strings.Contains(list, "both are in place") || !strings.Contains(list, "Third thing") {
		t.Errorf("task page lacks the note or the open task: %s", list)
	}
	if i, j := strings.Index(list, "Third thing"), strings.Index(list, "First thing"); i < 0 || j < 0 || i > j {
		t.Error("open tasks must come before done ones")
	}
	_, detail := fetchPage(t, e.Member, e.Base+rqPath(req.Request.ID, ""))
	if !strings.Contains(detail, "Task done") || !strings.Contains(detail, "Tasks for people") {
		t.Errorf("request detail lacks the tasks or their history: %s", detail)
	}
}

func TestMarkDoneIsAllOrNothingAndAGuestCannotTouchForeignTasks(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	own := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For gina", Assignee: "gina"})
	other := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "For all members"})
	resp := tpPost(t, e, e.Guest, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(own.ID, 10), strconv.FormatInt(other.ID, 10)}})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("guest marking a foreign task = %d", resp.StatusCode)
	}
	for _, id := range []int64{own.ID, other.ID} {
		if got, _ := e.St.TaskByID(id); got.State != humantask.Open {
			t.Errorf("task %d changed although the batch failed", id)
		}
	}
	if resp := tpPost(t, e, e.Guest, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(own.ID, 10)}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("guest marking the assigned task = %d", resp.StatusCode)
	}
	if got, _ := e.St.TaskByID(own.ID); got.State != humantask.Done {
		t.Errorf("assigned task not done: %+v", got)
	}
}

func TestSecretInTheDoneNoteIsRefusedWithoutAChange(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Set the key"})
	leaked := "ghp_" + strings.Repeat("a1B2", 9)
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {strconv.FormatInt(task.ID, 10)}, "note": {leaked}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=secret") {
		t.Fatalf("secret note = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Open || got.DoneNote != "" {
		t.Errorf("a refused note changed the task: %+v", got)
	}
	_, page := fetchPage(t, e.Member, e.Base+resp.Header.Get("Location"))
	if strings.Contains(page, leaked) || !strings.Contains(page, "looks like a secret") {
		t.Error("the page repeats the value or lacks the refusal")
	}
}

func TestTaskActionsNeedAnInteractiveSessionAndCSRF(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Do it"})
	resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/tasks/done", url.Values{"id": {strconv.FormatInt(task.ID, 10)}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("done without a CSRF token = %d", resp.StatusCode)
	}
	if got, _ := e.St.TaskByID(task.ID); got.State != humantask.Open {
		t.Error("task changed without a token")
	}
}

var painHref = regexp.MustCompile(`/ui/pains/(\d+)`)

func TestPainsPageDetailAndStateChange(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Make reading partial"})
	hidden := rqMake(t, e.St, rqSpec{Title: "HIDDEN-REQUEST", Project: shellHiddenProject})
	pa := tpOwner(e)
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Reading only works whole", Body: "Every read returns the full text.",
		Motivates: []pain.Target{{RequestID: req.Request.ID}}, Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.CreatePain(store.CreatePainInput{Project: shellHiddenProject, Title: "HIDDEN-PAIN", Motivates: []pain.Target{{RequestID: hidden.Request.ID}}, Actor: pain.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	_, list := fetchPage(t, e.Member, e.Base+"/ui/pains")
	if !strings.Contains(list, "Reading only works whole") || strings.Contains(list, "HIDDEN-PAIN") || !strings.Contains(list, "Active") {
		t.Errorf("pain list: %s", list)
	}
	_, detail := fetchPage(t, e.Member, e.Base+painURL(d.Pain.ID))
	for _, want := range []string{"Every read returns the full text.", "Make reading partial", "Change state"} {
		if !strings.Contains(detail, want) {
			t.Errorf("pain detail lacks %q", want)
		}
	}
	_, guestDetail := fetchPage(t, e.Guest, e.Base+painURL(d.Pain.ID))
	if strings.Contains(guestDetail, "Change state") {
		t.Error("a guest is offered the state form")
	}
	_, request := fetchPage(t, e.Member, e.Base+rqPath(req.Request.ID, ""))
	if !strings.Contains(request, "Motivated by") || !strings.Contains(request, "Reading only works whole") {
		t.Errorf("request page lacks the motivating pain: %s", request)
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/pains/9999"); code != http.StatusNotFound {
		t.Errorf("unknown pain = %d", code)
	}
	if resp := tpPost(t, e, e.Member, "/ui/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"/state", url.Values{"state": {"mitigated"}, "evidence_kind": {"commit"}, "evidence_ref": {"abc123"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=remaining_required") {
		t.Errorf("mitigated without remaining = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, failed := fetchPage(t, e.Member, e.Base+painURL(d.Pain.ID)+"?fail=remaining_required")
	if !strings.Contains(failed, "Say what is still unsolved.") {
		t.Errorf("the rule failure is not shown on the page")
	}
	resp := tpPost(t, e, e.Member, "/ui/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"/state", url.Values{"state": {"mitigated"}, "remaining": {"search still reads whole"}, "evidence_kind": {"commit"}, "evidence_ref": {"abc123"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("mitigate = %d", resp.StatusCode)
	}
	_, after := fetchPage(t, e.Member, e.Base+resp.Header.Get("Location"))
	for _, want := range []string{"State saved.", "search still reads whole", "Mitigated", "abc123"} {
		if !strings.Contains(after, want) {
			t.Errorf("after the change the page lacks %q", want)
		}
	}
	if resp := tpPost(t, e, e.Guest, "/ui/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"/state", url.Values{"state": {"obsolete"}, "note": {"x"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("guest changing a pain = %d", resp.StatusCode)
	}
}

func TestKnowledgePageNamesThePainsAnEntryMotivates(t *testing.T) {
	e := ovEnv(t)
	kid, err := e.St.InsertKnowledge(store.Knowledge{Type: "decision", Title: "Chunked writes", Body: "body", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpOwner(e).CreatePain(store.CreatePainInput{Project: shellProject, Title: "Writes are all or nothing", Motivates: []pain.Target{{KnowledgeID: kid}}, Actor: pain.Actor{AccountID: 1, Person: "alice"}}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/knowledge/"+strconv.FormatInt(kid, 10))
	if !strings.Contains(page, "Motivated by") || !strings.Contains(page, "Writes are all or nothing") {
		t.Errorf("knowledge page lacks the pain: %s", page)
	}
}

func TestTaskDescriptionShowsAndAFailedDoneExplainsItself(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	task := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Create the key", Description: "Test key, read-only scope"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/tasks")
	if !strings.Contains(page, "Test key, read-only scope") {
		t.Error("the description is not shown")
	}
	id := strconv.FormatInt(task.ID, 10)
	if resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {id}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("done = %d", resp.StatusCode)
	}
	// The second submit hits a task that is no longer open.
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {id}})
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=changed") {
		t.Fatalf("second done = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, after := fetchPage(t, e.Member, e.Base+resp.Header.Get("Location"))
	if !strings.Contains(after, "Someone changed this already") {
		t.Error("the failure is not explained")
	}
}

func TestResolvedPainStaysInTheList(t *testing.T) {
	e := ovEnv(t)
	pa := tpOwner(e)
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Gone again", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SetPainState(d.Pain.ID, "resolved", "", pain.Evidence{Kind: "commit", Ref: "abc123"}, "", pain.Actor{AccountID: 1, Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	_, list := fetchPage(t, e.Member, e.Base+"/ui/pains")
	if !strings.Contains(list, "Gone again") || !strings.Contains(list, "Resolved") {
		t.Errorf("a resolved pain vanished from the list: %s", list)
	}
}
