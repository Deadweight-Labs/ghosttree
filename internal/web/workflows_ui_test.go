package web

import (
	"context"
	"io"
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

// Review of E0, finding 8: a run that cost less than a cent is not free, and
// German reads a decimal comma.
func TestCostOfARunKeepsSmallAmountsAndTheLanguagesDecimalMark(t *testing.T) {
	for _, c := range []struct {
		l    lang
		usd  float64
		want string
	}{
		{langEN, 0, "0.00 $"},
		{langEN, 0.004, "0.004 $"},
		{langDE, 0.004, "0,004 $"},
		{langDE, 0.00042, "0,0004 $"},
		{langEN, 0.00001, "<0.0001 $"},
		{langEN, 0.01, "0.01 $"},
		{langDE, 0.042, "0,04 $"},
		{langDE, 12.5, "12,50 $"},
	} {
		if got := wfCost(c.l, c.usd); got != c.want {
			t.Errorf("wfCost(%s, %v) = %q, want %q", c.l, c.usd, got, c.want)
		}
	}
}

// AC-695.9 and AC-695.2 in the browser: a lead creates a workflow in the tab
// and saves a new version; the same version history holds a save over the API
// path (as MCP sends it); a member sees the file and its versions read-only and
// may not save; the run page names the version and links it.
func TestWorkflowTabCreatesAndChangesAWorkflowAndMembersOnlyRead(t *testing.T) {
	e, _ := workflowEnv(t)
	text := "---\ndescription: Merges melden\non: pr.merged\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n"
	status, list := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows")
	if status != http.StatusOK {
		t.Fatalf("list: %d", status)
	}
	mustContain(t, "lead list", list, `href="/ui/x/shell/workflows/new"`)
	status, form := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows/new")
	if status != http.StatusOK {
		t.Fatalf("form: %d", status)
	}
	mustContain(t, "form", form, `action="/ui/workflows/save"`, `name="text"`, "ghosttree/echo")
	// A file with an error comes back with the problem and the text as typed.
	bad := url.Values{"project": {shellProject}, "mode": {"new"}, "name": {"melden"}, "text": {strings.Replace(text, "uses:", "retries: 2\nuses:", 1)}}
	bad.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, e.Lead, e.Base+"/ui/workflows/save", bad)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "unknown key retries") || !strings.Contains(body, "retries: 2") {
		t.Fatalf("bad file: %d", resp.StatusCode)
	}
	if r := postForm(t, e, e.Lead, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"new"}, "name": {"melden"}, "text": {text}}); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "/ui/x/shell/workflows/melden?done=created") {
		t.Fatalf("create: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	changed := strings.Replace(text, "[pr.title]", "[pr.title, pr.number]", 1)
	if r := postForm(t, e, e.Lead, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"edit"}, "name": {"melden"}, "base_version": {"1"}, "text": {changed}, "note": {"Nummer"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("change: %d", r.StatusCode)
	}
	// The API (what the MCP tool calls) writes into the same history.
	saved, _, err := workflow.SaveDef(e.St, "person:1", shellProject, "melden", text, "zurück", store.WorkflowViaMCP, 2)
	if err != nil || saved.Def.Version != 3 {
		t.Fatalf("save over the API path: %+v %v", saved, err)
	}
	versions, _ := e.St.WorkflowDefVersions(shellProject, "melden")
	if len(versions) != 3 || versions[0].Via != store.WorkflowViaMCP || versions[1].Via != store.WorkflowViaWeb || versions[1].Note != "Nummer" {
		t.Fatalf("versions: %+v", versions)
	}
	status, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/melden")
	if status != http.StatusOK {
		t.Fatalf("member page: %d", status)
	}
	mustContain(t, "member page", page, "Merges melden", "uses: ghosttree/echo", `href="/ui/x/shell/workflows/melden?from=2&amp;to=3"`, "via MCP", "in the browser")
	mustNotContain(t, "member page", page, "?edit=1", `action="/ui/workflows/enabled"`)
	_, diff := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/melden?from=1&to=2")
	mustContain(t, "diff", diff, `class="dl add"`, "pr.number")
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/melden?edit=1"); status != http.StatusForbidden {
		t.Fatalf("member opens the form: %d", status)
	}
	if r := postForm(t, e, e.Member, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"edit"}, "name": {"melden"}, "base_version": {"3"}, "text": {changed}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member saves: %d", r.StatusCode)
	}
	if r := postForm(t, e, e.Member, "/ui/workflows/enabled", url.Values{"project": {shellProject}, "name": {"melden"}, "enabled": {"0"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("member switches off: %d", r.StatusCode)
	}
	_, list = fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows")
	mustContain(t, "member list", list, `href="/ui/x/shell/workflows/melden"`, "Version 3")
	mustNotContain(t, "member list", list, `href="/ui/x/shell/workflows/new"`)
	// The next merge runs version 3, and its page names and links it.
	for _, state := range []string{"open", "merged"} {
		if _, err := e.St.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: shellProject, HeadRemote: shellProject, Number: 16, Action: "closed",
			Title: "feat: two", Head: "feat/two", Base: "main", DefaultBranch: "main", State: state}); err != nil {
			t.Fatal(err)
		}
	}
	eng := &workflow.Engine{Store: e.St, Resolver: workflow.NewDefResolver(e.St)}
	if _, err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.St.WorkflowRunsIn(shellProject, store.WorkflowRunFilter{Workflow: "melden"})
	if len(runs) != 1 || runs[0].WorkflowVersion != "3" || runs[0].State != store.RunDone {
		t.Fatalf("run of the workflow: %+v", runs)
	}
	_, runPage := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/runs/"+itoa(runs[0].ID))
	mustContain(t, "run page", runPage, `href="/ui/x/shell/workflows/melden?v=3"`, `href="/ui/x/shell/workflows/melden"`)
	// The lead switches it off from the list; the next merge starts nothing.
	if r := postForm(t, e, e.Lead, "/ui/workflows/enabled", url.Values{"project": {shellProject}, "name": {"melden"}, "enabled": {"0"}, "next": {"list"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("switch off: %d", r.StatusCode)
	}
	if d, _ := e.St.WorkflowDefByName(shellProject, "melden"); d.Enabled {
		t.Fatal("still on")
	}
}

// AC-695.8: the runbooks are called Runbooks in German as well, and the
// German address of the former "Abläufe" leads to them.
func TestRunbooksAreCalledRunbooksAndTheOldAddressLeadsThere(t *testing.T) {
	e, _ := workflowEnv(t)
	if messagesDE["repo.tab.runbooks"] != "Runbooks" || messagesDE["runbooks.new"] != "Neues Runbook" || messages["repo.tab.workflows"] != "Workflows" {
		t.Fatalf("names: %q %q", messagesDE["repo.tab.runbooks"], messagesDE["runbooks.new"])
	}
	for k, v := range messagesDE {
		if strings.HasPrefix(k, "runbooks.") || k == "repo.tab.runbooks" || k == "machine.runbook" || k == "err.back_runbooks" {
			if strings.Contains(v, "Ablauf") || strings.Contains(v, "Abläuf") {
				t.Errorf("%s still says %q", k, v)
			}
		}
	}
	noFollow := *e.Member
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for from, to := range map[string]string{"/ui/x/shell/ablaeufe": "/ui/x/shell/runbooks", "/ui/x/shell/ablaeufe/rollout": "/ui/x/shell/runbooks/rollout"} {
		resp, err := noFollow.Get(e.Base + from)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != to {
			t.Errorf("%s: %d %s", from, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}
