package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
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
	// Defs are the workflows of the project (REQ-695); CanEdit shows the
	// button for a new one and the switches.
	Defs    []wfDefRow
	CanEdit bool
	NewHref string
	Token   string
	Project string
	// Templates are the templates a person with the right creates with one
	// click (REQ-696).
	Templates []wfTemplateOption
	// Waiting counts the proposals of runs that wait on the review page (S12).
	Waiting    int
	ReviewHref string
	// ReviewNeed is, for a reader who may not review, the role it takes and
	// whom to ask, beside the proposals that wait (N-C).
	ReviewNeed string
	// Need is, for a reader without the right, the role it takes and whom to
	// ask (REQ-704); the button stays visible but disabled.
	Need string
}

// wfTemplateKeys word the templates of workflow.TemplateNames.
var wfTemplateKeys = map[string]string{
	"wissen-nach-merge": "wf.tpl.wissen_nach_merge", "wuensche-im-pr": "wf.tpl.wuensche_im_pr",
	"idee-abgleich": "wf.tpl.idee_abgleich", "kriterien-aus-diskussion": "wf.tpl.kriterien_aus_diskussion",
}

// wfTemplateOption is one template in the choice "From template".
type wfTemplateOption struct{ Name, Key string }

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
	// VersionHref leads to the version of a stored workflow the run ran with.
	VersionHref  string
	WorkflowHref string
	// Results are what the run created or was refused (REQ-695).
	Results []wfResultRow
	Token   string
	// Tools are the tool calls of an own prompt (REQ-698), from the full text.
	Tools []wfToolRow
	// Why says in words how a run that has no result ended (S02).
	Why *wfWhy
	// CanRerun offers to start the run again (leads and owners).
	CanRerun bool
	Project  string
	// Waiting counts the results of this run that wait on the review page.
	Waiting    int
	ReviewHref string
	ReviewNeed string
	Duration   string
	// NoCall: the run reached no model, so there is no cost to show.
	NoCall bool
	// Active: the run is queued or running; the page says so and reloads by
	// itself until it ends (N-E).
	Active bool
	// Summary words how the results stand now (N-G), not as the run left them.
	Summary string
}

// wfToolRow is one tool call of the loop of an own prompt: the round, the
// tool, how it went, its input and (shortened) result.
type wfToolRow struct {
	Round           int
	Tool            string
	StateKey, Tone  string
	ReasonKey       string
	Input, Result   string
	Refused, Submit bool
}

var wfToolLook = map[string]struct{ key, tone string }{
	workflow.ToolOK:      {"wf.tool.ok", "ok"},
	workflow.ToolFailed:  {"wf.tool.failed", "warn"},
	workflow.ToolRefused: {"wf.tool.refused", "dan"},
}

var wfToolReasonKeys = map[string]string{
	workflow.ToolReasonNotDeclared: "wf.tool.why.not_declared",
	workflow.ToolReasonPerRound:    "wf.tool.why.per_round",
	workflow.ToolReasonAfterSubmit: "wf.tool.why.after_submit",
}

// wfToolRows reads the tool calls of a run's full text.
func wfToolRows(raw string) []wfToolRow {
	if raw == "" {
		return nil
	}
	var calls []store.WorkflowToolCall
	if json.Unmarshal([]byte(raw), &calls) != nil {
		return nil
	}
	out := make([]wfToolRow, 0, len(calls))
	for _, c := range calls {
		look, ok := wfToolLook[c.State]
		if !ok {
			look = wfToolLook[workflow.ToolFailed]
		}
		out = append(out, wfToolRow{Round: c.Round, Tool: c.Tool, StateKey: look.key, Tone: look.tone, ReasonKey: wfToolReasonKeys[c.Reason],
			Input: c.Input, Result: c.Result, Refused: c.State == workflow.ToolRefused, Submit: c.Tool == workflow.SubmitTool})
	}
	return out
}

// wfResultRow is one result of a run: its kind, how it stands, why it was
// refused, what it created, and the undo of an automatic approval.
type wfResultRow struct {
	ID                int64
	KindKey, StateKey string
	Tone, ReasonKey   string
	Ref, RefHref      string
	CanUndo           bool
	// Waiting: the result waits for a person on the review page.
	Waiting bool
	// State is the stored state, for the summary of the run.
	State string
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
	"disabled":            "wf.reason.disabled",
	"invalid":             "wf.reason.invalid",
	"author_rights":       "wf.reason.author_rights",
	"external":            "wf.reason.external",
	"policy.instruction":  "wf.reason.policy.instruction",
	"policy.prompt":       "wf.reason.policy.prompt",
	"policy.research":     "wf.reason.policy.research",
	"interrupted":         "wf.reason.interrupted",
	"timeout":             "wf.reason.timeout",
	store.SkipRate:        "wf.reason.rate_limit",
	store.AICapRun:        "wf.reason.cap.run",
	store.AICapWorkflow:   "wf.reason.cap.workflow",
	store.AICapOwner:      "wf.reason.cap.owner",
	store.AICapConnection: "wf.reason.cap.connection",
	store.AICapServer:     "wf.reason.cap.server",
	store.AICapLoan:       "wf.reason.cap.loan",
}

// wfDuration words how long a run took: seconds below two minutes, then
// minutes; "" while it has not ended.
func wfDuration(l lang, started, finished string) string {
	a, b := parseTime(started), parseTime(finished)
	if a.IsZero() || b.IsZero() || b.Before(a) {
		return ""
	}
	d := b.Sub(a)
	switch {
	case d < time.Second:
		return l.msg("wf.duration.under_second")
	case d < 2*time.Minute:
		return l.msg("wf.duration.seconds", int(d.Seconds()))
	}
	return l.msg("wf.duration.minutes", int(d.Minutes()))
}

func workflowRunHref(base string, id int64) string {
	return base + "/workflows/runs/" + strconv.FormatInt(id, 10)
}

// wfCost words a cost in US dollars: two decimals from a cent up, so that a
// run that cost 0.004 $ does not read as free, and the decimal comma in German.
func wfCost(l lang, usd float64) string {
	var s string
	switch {
	case usd <= 0:
		s = "0.00"
	case usd >= 0.01:
		s = fmt.Sprintf("%.2f", usd)
	case usd < 0.0001:
		s = "<0.0001"
	default:
		s = strings.TrimRight(fmt.Sprintf("%.4f", usd), "0")
	}
	if l == langDE {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s + " $"
}

func (a *app) wfRow(l lang, base string, run store.WorkflowRun) wfRunRow {
	look := wfStateLook[run.State]
	row := wfRunRow{ID: run.ID, Href: workflowRunHref(base, run.ID), Workflow: run.Workflow, StateKey: look.key, Icon: look.icon, Tone: look.tone,
		Result: run.Result, Cost: wfCost(l, run.CostUSD), Trigger: wfTriggerOf(base, run)}
	row.Reason = wfReasonText(l, run.Reason)
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
	v := &workflowsView{Defs: a.workflowDefRows(l, pa, rv.Href, rv.Project), CanEdit: interactive(r) && pa.CanEditWorkflows(rv.Project),
		NewHref: rv.Href + "/workflows/" + workflowNew, Token: csrfOf(r), Project: rv.Project}
	if !v.CanEdit && interactive(r) {
		v.Need = roleNeededShort(l, roleNeededOf(pa.Check(rv.Project, store.ResWorkflow, store.ActEdit, store.Object{})))
	}
	if v.CanEdit {
		for _, t := range workflow.TemplateNames {
			if key, ok := wfTemplateKeys[t]; ok {
				v.Templates = append(v.Templates, wfTemplateOption{Name: t, Key: key})
			}
		}
	}
	for _, run := range runs {
		v.Rows = append(v.Rows, a.wfRow(l, rv.Href, run))
	}
	v.Empty = len(v.Rows) == 0 && len(v.Defs) == 0
	if v.Waiting = a.store.WorkflowProposalsWaiting(rv.Project); v.Waiting > 0 && reviews(pa.Role(rv.Project)) {
		v.ReviewHref = rv.Href + "/review"
	} else if v.Waiting > 0 && interactive(r) {
		if v.ReviewNeed = reviewNeedLine(l, pa, rv.Project); v.ReviewNeed == "" {
			v.Waiting = 0
		}
	} else {
		v.Waiting = 0
	}
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
		Chain: run.ChainDepth, Base: view.Href + "/workflows", Project: remote}
	stamp := func(s string) string {
		if t := parseTime(s); !t.IsZero() {
			return l.date(t.Local(), true) + " " + t.Local().Format("15:04")
		}
		return ""
	}
	v.Started, v.Finished = stamp(run.StartedAt), stamp(run.FinishedAt)
	v.Duration = wfDuration(l, run.StartedAt, run.FinishedAt)
	v.NoCall = run.CostUSD == 0 && run.Connection == ""
	org, _ := a.orgOfProject(r, remote)
	v.Why = wfExplain(l, run, org)
	v.CanRerun = interactive(r) && run.Rerunnable() && pa.CanEditWorkflows(remote)
	if !strings.Contains(run.Workflow, "/") && run.WorkflowVersion != "" {
		if _, err := pa.WorkflowDef(remote, run.Workflow); err == nil {
			v.VersionHref = workflowHref(remote, run.Workflow) + "?v=" + url.QueryEscape(run.WorkflowVersion)
		}
		v.WorkflowHref = workflowHref(remote, run.Workflow)
	}
	v.Results, v.Token = a.wfResultRows(r, pa, remote, run.ID), csrfOf(r)
	for _, res := range v.Results {
		if res.Waiting {
			v.Waiting++
		}
	}
	v.Summary = wfResultSummary(l, v.Results)
	v.Active = run.State == store.RunQueued || run.State == store.RunRunning
	refresh := 0
	if v.Active {
		refresh = wfActiveRefreshSeconds
	}
	a.wfInvalidWhy(l, pa, remote, run, v)
	if v.Waiting > 0 {
		if reviews(pa.Role(remote)) {
			v.ReviewHref = view.Href + "/review"
		} else {
			v.ReviewNeed = reviewNeedLine(l, pa, remote)
		}
	}
	switch {
	case text != nil:
		v.Inputs, v.Output, v.Tools = text.Inputs, text.Output, wfToolRows(text.Tools)
	case run.TextPurgedAt != "":
		v.Purged = stamp(run.TextPurgedAt)
	default:
		v.NoText = true
	}
	a.renderBrowser(w, r2, "workflowrun", pageData{Title: run.Workflow + " #" + id, Project: remote, WorkflowRunV: v, Refresh: refresh})
}

// pullWorkflowRuns are the runs a pull request started, for the side of its
// page; nil when the viewer may not see runs or there are none.
func pullWorkflowRuns(l lang, a *app, pa *store.ProjectAccess, base, remote string, n int64) []wfRunRow {
	return subjectWorkflowRuns(l, a, pa, base, remote, "pr", n)
}

// subjectWorkflowRuns are the runs a pull request or an idea started, for the
// side of its page, so that a run that failed is seen where it began (S02).
func subjectWorkflowRuns(l lang, a *app, pa *store.ProjectAccess, base, remote, kind string, n int64) []wfRunRow {
	if !pa.CanSeeWorkflows(remote) {
		return nil
	}
	runs, err := pa.WorkflowRuns(remote, store.WorkflowRunFilter{SubjectKind: kind, SubjectID: strconv.FormatInt(n, 10), Limit: 10})
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
