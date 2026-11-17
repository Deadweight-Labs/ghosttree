package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// workflowEnv is pullsEnv with ghosttree/echo switched on for merges into the
// default branch, PR 15 merged into it, and one tick of the engine.
func workflowEnv(t *testing.T) (shellEnv, store.WorkflowRun) {
	t.Helper()
	e, _ := pullsEnv(t)
	for _, state := range []string{"open", "merged"} {
		if res, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: 15, Action: "closed",
			Title: "feat: echo", Head: "feat/echo", Base: "main", DefaultBranch: "main", State: state}); err != nil || !res.Applied {
			t.Fatalf("record PR 15: %+v %v", res, err)
		}
	}
	if _, err := e.St.SetWorkflowTriggerAsOperator(store.WorkflowTrigger{Project: shellProject, Workflow: workflow.EchoName, EventKind: store.EventPRMerged, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	eng := &workflow.Engine{Store: e.St, Resolver: workflow.DefaultBuiltins()}
	if rep, err := eng.Tick(context.Background()); err != nil || rep.Executed != 1 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	runs, err := e.St.WorkflowRunsIn(shellProject, store.WorkflowRunFilter{SubjectKind: "pr", SubjectID: "15"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs of PR 15: %+v %v", runs, err)
	}
	return e, runs[0]
}

// AC-693.1 in the browser: the run of a merge is listed under the repository
// with its trigger linked to the pull request, has a page, and the pull
// request links its run.
func TestMergeRunIsListedLinkedToThePullRequestAndHasAPage(t *testing.T) {
	e, run := workflowEnv(t)
	runHref := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	status, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows")
	if status != http.StatusOK {
		t.Fatalf("list: %d", status)
	}
	mustContain(t, "list", list, `data-tab="workflows"`, "ghosttree/echo", `href="`+runHref+`"`, `href="/ui/x/shell/pulls/15"`, "pr.merged PR #15", "0.00 $", "ui-workflows.css")

	status, page := fetchPage(t, e.Member, e.Base+runHref)
	if status != http.StatusOK {
		t.Fatalf("run page: %d", status)
	}
	mustContain(t, "run page", page, "ghosttree/echo", `href="/ui/x/shell/pulls/15"`, "builtin", `&#34;subject_id&#34;: &#34;15&#34;`, "pr.merged PR #15")

	status, pull := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pulls/15")
	if status != http.StatusOK {
		t.Fatalf("pull: %d", status)
	}
	mustContain(t, "pull", pull, `href="`+runHref+`"`, "wfside")
	_, open := fetchPage(t, e.Member, e.Base+"/ui/x/shell/pulls/12")
	mustNotContain(t, "open pull", open, "wfside")
}

func TestGuestsSeeNoWorkflowsAndRunsStayInTheirRepository(t *testing.T) {
	e, run := workflowEnv(t)
	runHref := "/ui/x/shell/workflows/runs/" + itoa(run.ID)
	for _, p := range []string{"/ui/x/shell/workflows", runHref} {
		if status, _ := fetchPage(t, e.Guest, e.Base+p); status != http.StatusNotFound {
			t.Errorf("guest at %s: %d", p, status)
		}
	}
	_, code := fetchPage(t, e.Guest, e.Base+"/ui/x/shell")
	mustNotContain(t, "guest tabs", code, `data-tab="workflows"`)
	_, pull := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/pulls/15")
	mustNotContain(t, "guest pull", pull, "wfside", runHref)
	// The number of a run under another repository is unknown there.
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/x/hidden/workflows/runs/"+itoa(run.ID)); status != http.StatusNotFound {
		t.Fatalf("run under another repository: %d", status)
	}
	for _, bad := range []string{"0", "x", "99999"} {
		if status, _ := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+bad); status != http.StatusNotFound {
			t.Errorf("run %s: %d", bad, status)
		}
	}
}

// AC-693.3 on the page: after the retention the full text is gone and the
// page says when; cost, result and version stay.
func TestRunPageAfterTheRetentionKeepsCostResultAndVersion(t *testing.T) {
	e, run := workflowEnv(t)
	if _, err := e.St.PurgeWorkflowRunTexts(time.Now().AddDate(0, 0, store.WorkflowRetentionDefault+1)); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(run.ID))
	mustContain(t, "purged run", page, "pr.merged PR #15", "builtin", "0.00 $", "Full text deleted on")
	mustNotContain(t, "purged run", page, "&#34;subject_id&#34;")
}

func TestOwnerSetsTheRetentionOfAnOrganization(t *testing.T) {
	e, run := workflowEnv(t)
	_, settings := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "settings", settings, `action="/ui/orgs/workflows"`, `name="retention_days"`, `value="90"`)
	// The section stands among the sections, not inside the menu of them.
	if nav, form := strings.Index(settings, "</nav>"), strings.Index(settings, `action="/ui/orgs/workflows"`); nav < 0 || form < nav || !strings.Contains(settings, `href="#workflows"`) {
		t.Fatalf("section placement: nav end %d, form %d", nav, form)
	}
	_, other := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "member settings", other, `action="/ui/orgs/workflows"`)

	post := func(c *http.Client, days string) int {
		t.Helper()
		_, page := fetchPage(t, c, e.Base+"/ui/orgs?org=alpha")
		form := url.Values{"csrf_token": {csrfOn(t, page)}, "org": {"alpha"}, "retention_days": {days}}
		resp := sameOriginPostForm(t, c, e.Base+"/ui/orgs/workflows", form)
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := post(e.Member, "7"); status != http.StatusForbidden {
		t.Fatalf("member sets the retention: %d", status)
	}
	if status := post(e.Owner, "0"); status != http.StatusBadRequest {
		t.Fatalf("zero days: %d", status)
	}
	if status := post(e.Owner, "14"); status != http.StatusSeeOther {
		t.Fatalf("owner: %d", status)
	}
	if got := e.St.WorkflowSettingsFor(run.OrgID).RetentionDays; got != 14 {
		t.Fatalf("retention: %d", got)
	}
	_, settings = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha&notice=workflows")
	mustContain(t, "saved", settings, `value="14"`, "Retention saved")
	if !strings.Contains(settings, `id="workflows"`) {
		t.Fatal("section anchor")
	}
}
