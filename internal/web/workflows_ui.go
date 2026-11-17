package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The tab "Workflows" of a repository (REQ-693, stage E0): the runs of the
// project with state, trigger, cost and result, and the page of one run. Stage
// E2 adds the workflows themselves. Runs are for members and up (ResWorkflow).

// wfRunRow is one run in a list.
type wfRunRow struct {
	ID                   int64
	Href, Workflow       string
	StateKey, Icon, Tone string
	Reason               string
	Trigger              wfTrigger
	Result, Cost, Age    string
}

// wfTrigger names the event of a run and links its subject: the pull request,
// the idea.
type wfTrigger struct {
	EventKey, Label, Href string
}

type workflowsView struct {
	Rows  []wfRunRow
	Empty bool
}

type workflowRunView struct {
	wfRunRow
	Version, Connection, Model string
	Started, Finished          string
	Chain                      int
	Inputs, Output             string
	// Purged is when the full text went; Kept says it never had one.
	Purged string
	NoText bool
	Base   string
}

var wfStateLook = map[string]struct{ icon, tone, key string }{
	store.RunQueued:  {"clock", "out", "wf.state.queued"},
	store.RunRunning: {"play", "warn", "wf.state.running"},
	store.RunDone:    {"done", "ok", "wf.state.done"},
	store.RunFailed:  {"x", "dan", "wf.state.failed"},
	store.RunBudget:  {"tri", "warn", "wf.state.budget"},
	store.RunSkipped: {"drop", "out", "wf.state.skipped"},
}

var wfEventKeys = map[string]string{
	store.EventPRMerged:             "wf.event.pr.merged",
	store.EventIdeaCreated:          "wf.event.idea.created",
	store.EventIdeaDiscussionClosed: "wf.event.idea.discussion_closed",
	store.EventSchedule:             "wf.event.schedule",
}

// wfReasonKeys word the reasons the server gives; a block's own reason is
// shown as it is.
var wfReasonKeys = map[string]string{
	store.SkipChain:       "wf.reason.chain",
	store.SkipChainDepth:  "wf.reason.chain_depth",
	"unknown_workflow":    "wf.reason.unknown_workflow",
	"interrupted":         "wf.reason.interrupted",
	store.AICapRun:        "wf.reason.cap.run",
	store.AICapWorkflow:   "wf.reason.cap.workflow",
	store.AICapOwner:      "wf.reason.cap.owner",
	store.AICapConnection: "wf.reason.cap.connection",
	store.AICapServer:     "wf.reason.cap.server",
	store.AICapLoan:       "wf.reason.cap.loan",
}

func workflowRunHref(base string, id int64) string {
	return base + "/workflows/runs/" + strconv.FormatInt(id, 10)
}

func wfCost(usd float64) string { return aiUSD(usd) }

func (a *app) wfRow(l lang, base string, run store.WorkflowRun) wfRunRow {
	look := wfStateLook[run.State]
	row := wfRunRow{ID: run.ID, Href: workflowRunHref(base, run.ID), Workflow: run.Workflow, StateKey: look.key, Icon: look.icon, Tone: look.tone,
		Result: run.Result, Cost: wfCost(run.CostUSD), Trigger: wfTriggerOf(base, run)}
	row.Reason = run.Reason
	if key, ok := wfReasonKeys[run.Reason]; ok {
		row.Reason = l.msg(key)
	}
	if t := parseTime(run.CreatedAt); !t.IsZero() {
		row.Age = shortAgo(l, overviewNow().UTC(), t)
	}
	return row
}

func wfTriggerOf(base string, run store.WorkflowRun) wfTrigger {
	t := wfTrigger{EventKey: wfEventKeys[run.EventKind]}
	switch run.SubjectKind {
	case "pr":
		t.Label, t.Href = "#"+run.SubjectID, base+"/pulls/"+run.SubjectID
	case "idea":
		t.Label, t.Href = "IDEA-"+run.SubjectID, base+"/ideas/"+run.SubjectID
	}
	return t
}

// workflowsPage lists the runs of the project of the repository path.
func (a *app) workflowsPage(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	rv := repoFrom(r)
	if rv == nil {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	runs, err := pa.WorkflowRuns(rv.Project, store.WorkflowRunFilter{Limit: 200})
	if err != nil {
		if errors.Is(err, store.ErrAccessNotFound) || errors.Is(err, store.ErrAccessForbidden) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	v := &workflowsView{}
	for _, run := range runs {
		v.Rows = append(v.Rows, a.wfRow(l, rv.Href, run))
	}
	v.Empty = len(v.Rows) == 0
	a.renderBrowser(w, r, "workflows", pageData{Title: l.msg("repo.tab.workflows"), Project: rv.Project, WorkflowsV: v})
}

// workflowRunPage is the page of one run.
func (a *app) workflowRunPage(w http.ResponseWriter, r *http.Request, remote, id string) {
	l := a.langFor(r)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	run, text, err := pa.WorkflowRun(remote, n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r2, view := a.repoDetailRequest(r, pa, remote, "workflows")
	v := &workflowRunView{wfRunRow: a.wfRow(l, view.Href, run), Version: run.WorkflowVersion, Connection: run.Connection, Model: run.Model,
		Chain: run.ChainDepth, Base: view.Href + "/workflows"}
	stamp := func(s string) string {
		if t := parseTime(s); !t.IsZero() {
			return l.dateTime(t.Local())
		}
		return ""
	}
	v.Started, v.Finished = stamp(run.StartedAt), stamp(run.FinishedAt)
	switch {
	case text != nil:
		v.Inputs, v.Output = text.Inputs, text.Output
	case run.TextPurgedAt != "":
		v.Purged = stamp(run.TextPurgedAt)
	default:
		v.NoText = true
	}
	a.renderBrowser(w, r2, "workflowrun", pageData{Title: run.Workflow + " #" + id, Project: remote, WorkflowRunV: v})
}

// pullWorkflowRuns are the runs a pull request started, for the side of its
// page; nil when the viewer may not see runs or there are none.
func pullWorkflowRuns(l lang, a *app, pa *store.ProjectAccess, base, remote string, n int64) []wfRunRow {
	if !pa.CanSeeWorkflows(remote) {
		return nil
	}
	runs, err := pa.WorkflowRuns(remote, store.WorkflowRunFilter{SubjectKind: "pr", SubjectID: strconv.FormatInt(n, 10), Limit: 10})
	if err != nil {
		return nil
	}
	var out []wfRunRow
	for _, run := range runs {
		out = append(out, a.wfRow(l, base, run))
	}
	return out
}

// orgWorkflowsSave stores how long the full text of workflow runs stays in an
// organization (POST /ui/orgs/workflows); only its owner may.
func (a *app) orgWorkflowsSave(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("retention_days")))
	if err != nil {
		a.orgFailure(w, r, store.ErrWorkflowSettings)
		return
	}
	if err := a.store.SetWorkflowSettings(browserPrincipal(r).ID, store.WorkflowSettings{OrgID: o.ID, RetentionDays: days}); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "workflows")
}
