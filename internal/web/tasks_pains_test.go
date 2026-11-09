package web

import (
	"net/http"
	"net/url"
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

func TestMarkSeveralTasksDoneFromTheInbox(t *testing.T) {
	e := ovEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Ship it"})
	a := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "First thing"})
	b := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Kind: "input", Title: "Second thing"})
	c := tpTask(t, e, shellProject, req.Request.ID, humantask.CreateInput{Title: "Third thing"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/inbox")
	// One row per task, leading to the page of the task.
	for _, task := range []humantask.Task{a, b, c} {
		if !strings.Contains(page, `href="/ui/x/shell/tasks/`+strconv.FormatInt(task.ID, 10)+`"`) {
			t.Fatalf("no link to the page of task %d", task.ID)
		}
	}
	if strings.Contains(page, `name="value"`) {
		t.Error("a task form must not carry a value field")
	}
	form := url.Values{"id": {strconv.FormatInt(a.ID, 10), strconv.FormatInt(b.ID, 10)}, "note": {"both are in place"}, "back": {"inbox"}}
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
	_, list := fetchPage(t, e.Member, e.Base+"/ui/inbox?done=1")
	if !strings.Contains(list, "Third thing") || !strings.Contains(list, "First thing") || !strings.Contains(list, `id="done"`) {
		t.Errorf("inbox lacks the open task or the finished ones: %s", list)
	}
	if i, j := strings.Index(list, "Third thing"), strings.Index(list, "First thing"); i < 0 || j < 0 || i > j {
		t.Error("open tasks must come before done ones")
	}
	_, detail := fetchPage(t, e.Member, e.Base+rqPath(req.Request.ID, ""))
	if strings.Count(detail, `data-act="task.done"`) != 2 || !strings.Contains(detail, "First thing") || !strings.Contains(detail, "Second thing") {
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
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues?q=is%3Apain+is%3Aopen")
	if !strings.Contains(list, "Reading only works whole") || strings.Contains(list, "HIDDEN-PAIN") {
		t.Errorf("pain list: %s", list)
	}
	// The old address leads to the page in the repository.
	if resp, err := e.Member.Get(e.Base + painURL(d.Pain.ID)); err != nil || resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10) {
		t.Errorf("old pain address = %v", resp)
	}
	_, detail := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	for _, want := range []string{"Every read returns the full text.", "Make reading partial", `action="/ui/pains/` + strconv.FormatInt(d.Pain.ID, 10) + `/state"`, "Set to Mitigated"} {
		if !strings.Contains(detail, want) {
			t.Errorf("pain detail lacks %q", want)
		}
	}
	_, guestDetail := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	if strings.Contains(guestDetail, `/state"`) {
		t.Error("a guest is offered the state form")
	}
	// The same page lives in the repository, with its head; a foreign project does not know it.
	code, inRepo := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	if code != http.StatusOK || !strings.Contains(inRepo, "Make reading partial") || !strings.Contains(inRepo, `class="rband`) {
		t.Errorf("pain in the repository: %d", code)
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/x/hidden/pains/"+strconv.FormatInt(d.Pain.ID, 10)); code != http.StatusNotFound {
		t.Errorf("pain under a foreign project = %d", code)
	}
	_, request := fetchPage(t, e.Member, e.Base+rqPath(req.Request.ID, ""))
	if !strings.Contains(request, "Arose from") || !strings.Contains(request, "Reading only works whole") {
		t.Errorf("request page lacks the motivating pain: %s", request)
	}
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/pains/9999"); code != http.StatusNotFound {
		t.Errorf("unknown pain = %d", code)
	}
	if resp := tpPost(t, e, e.Member, "/ui/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"/state", url.Values{"state": {"mitigated"}, "evidence_kind": {"commit"}, "evidence_ref": {"abc123"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "fail=remaining_required") {
		t.Errorf("mitigated without remaining = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, failed := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10)+"?fail=remaining_required")
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
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues?q=is%3Apain+is%3Aall")
	if !strings.Contains(list, "Gone again") {
		t.Errorf("a resolved pain vanished from the list: %s", list)
	}
}
