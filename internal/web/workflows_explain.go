package web

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// Why a run ended without a result, in words (S02, usability run 5). The
// server keeps the reason a run ended as a code or as the error text of the
// model call ("LLM HTTP 404: 404 page not found"); the page says what that
// means, where it is fixed and who may fix it, and keeps the raw text one
// click away.

// wfWhy explains how a run ended. Raw is what the server or the provider said,
// shown folded when it adds something to Text.
type wfWhy struct {
	Text    string
	Raw     string
	FixHref string
	FixKey  string
	Who     string
}

// wfHTTPStatus finds the status of a provider's answer in the error of a
// model call.
var wfHTTPStatus = regexp.MustCompile(`LLM HTTP (\d{3})`)

// wfModelWhy maps the error of a model call to a key; "" when the reason is
// no model error.
func wfModelWhy(reason string) string {
	switch {
	case reason == "":
		return ""
	case strings.Contains(reason, "this run has no model access"):
		return "wf.why.no_proxy"
	case strings.Contains(reason, "not available to this run"):
		return "wf.why.model_unavailable"
	case strings.Contains(reason, "cannot be used"):
		return "wf.why.key_unusable"
	case strings.Contains(reason, "connection address is not allowed"):
		return "wf.why.address_not_allowed"
	case strings.Contains(reason, "provider did not answer"):
		return "wf.why.no_answer"
	case strings.Contains(reason, "cannot call tools"):
		return "wf.why.no_tools"
	case strings.Contains(reason, "no JSON object of the expected form"), strings.Contains(reason, "decode LLM response"),
		strings.Contains(reason, "has no choices"), strings.Contains(reason, "no text content"), strings.Contains(reason, "handed in no results"):
		return "wf.why.answer_unusable"
	}
	m := wfHTTPStatus.FindStringSubmatch(reason)
	if m == nil {
		return ""
	}
	code, _ := strconv.Atoi(m[1])
	switch {
	case code == http.StatusNotFound:
		return "wf.why.http404"
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return "wf.why.key_refused"
	case code == http.StatusTooManyRequests:
		return "wf.why.rate"
	case code == http.StatusBadRequest:
		return "wf.why.bad_request"
	case code >= 500:
		return "wf.why.provider_down"
	}
	return "wf.why.http"
}

// wfAIReasons are the reasons whose fix lies with the connections and caps.
var wfAIReasons = map[string]bool{store.AICapRun: true, store.AICapWorkflow: true, store.AICapOwner: true,
	store.AICapConnection: true, store.AICapServer: true, store.AICapLoan: true}

// wfExplain says in words how a run ended; nil for a run that is still under
// way or ended done. org is the namespace of the project ("" when unknown):
// connections and the policy are kept in its settings.
func wfExplain(l lang, run store.WorkflowRun, org store.Org) *wfWhy {
	switch run.State {
	case store.RunFailed, store.RunBudget, store.RunSkipped:
	default:
		return nil
	}
	w := &wfWhy{}
	settings := func(fragment, fix, who string) {
		if org.Slug == "" {
			return
		}
		w.FixHref, w.FixKey, w.Who = orgSettingsHref(org, fragment), fix, l.msg(who, org.Name)
	}
	reason := run.Reason
	_, policyKind := strings.CutPrefix(reason, "policy.kind:")
	switch {
	case strings.HasPrefix(reason, "policy.") || policyKind:
		w.Text = wfReasonText(l, reason)
		settings("#workflows", "wf.fix.policy", "wf.who.policy")
	case wfAIReasons[reason]:
		w.Text = wfReasonText(l, reason)
		settings("#ai", "wf.fix.connection", "wf.who.ai")
	case wfReasonKeys[reason] != "":
		w.Text = wfReasonText(l, reason)
	default:
		if key := wfModelWhy(reason); key != "" {
			w.Text, w.Raw = l.msg(key), reason
			if key != "wf.why.answer_unusable" {
				settings("#ai", "wf.fix.connection", "wf.who.ai")
			}
		} else {
			// A block's own reason: say that the run broke off, and show it.
			w.Text, w.Raw = l.msg("wf.why.other"), reason
		}
	}
	if w.Raw != "" && strings.TrimSpace(w.Raw) == w.Text {
		w.Raw = ""
	}
	return w
}

// wfReasonText words a reason code of the server, or returns the reason as it
// is when it is none.
func wfReasonText(l lang, reason string) string {
	if key, ok := wfReasonKeys[reason]; ok {
		return l.msg(key)
	}
	if kind, ok := strings.CutPrefix(reason, "policy.kind:"); ok {
		return l.msg("wf.reason.policy.kind", kind)
	}
	if key := wfModelWhy(reason); key != "" {
		return l.msg(key)
	}
	return reason
}

// workflowRerun is POST /ui/workflows/rerun: start a run that ended without a
// result once more, for the same subject. Leads and owners may. The executor
// takes the queued run at its next round.
func (a *app) workflowRerun(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	project := strings.TrimSpace(r.FormValue("project"))
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || project == "" || !a.projectVisible(r, pa, project) || !pa.CanSeeWorkflows(project) {
		http.NotFound(w, r)
		return
	}
	run, err := pa.RerunWorkflowRun(project, id)
	switch {
	case errors.Is(err, store.ErrAccessNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, store.ErrWorkflowRerun):
		http.Redirect(w, r, workflowRunHref(repoHref(project), id), http.StatusSeeOther)
		return
	case err != nil:
		a.accessDenied(w, r, err)
		return
	}
	http.Redirect(w, r, workflowRunHref(repoHref(project), run.ID), http.StatusSeeOther)
}

// wfActiveRefreshSeconds is how often the page of a queued or running run
// reloads itself: a run with a model call takes a minute or three.
const wfActiveRefreshSeconds = 8

// wfResultSummary words how the results of a run stand now: what waits, what
// was applied or accepted, what was undone or refused. The sentence the run
// stored when it ended ("1 proposed, 0 applied") goes stale as soon as a person
// accepts a proposal (N-G).
func wfResultSummary(l lang, rows []wfResultRow) string {
	count := map[string]int{}
	for _, r := range rows {
		count[r.State]++
	}
	var parts []string
	for _, k := range []struct{ state, key string }{
		{store.ResultProposed, "wf.sum.proposed"}, {store.ResultAccepted, "wf.sum.accepted"}, {store.ResultAuto, "wf.sum.auto"},
		{store.ResultUndone, "wf.sum.undone"}, {"linked", "wf.sum.linked"}, {store.ResultRejected, "wf.sum.rejected"},
	} {
		if n := count[k.state]; n > 0 {
			parts = append(parts, l.msg(k.key, n))
		}
	}
	return strings.Join(parts, " · ")
}

// wfInvalidWhy completes the explanation of a run skipped because the newest
// version does not pass the check: it leads to the workflow and shows the first
// error of the check (N-D). Only what the viewer may read is shown.
func (a *app) wfInvalidWhy(l lang, pa *store.ProjectAccess, remote string, run store.WorkflowRun, v *workflowRunView) {
	if v.Why == nil || run.Reason != "invalid" || strings.Contains(run.Workflow, "/") {
		return
	}
	def, err := pa.WorkflowDef(remote, run.Workflow)
	if err != nil {
		return
	}
	v.Why.FixHref, v.Why.FixKey = workflowHref(remote, run.Workflow), "wf.fix.workflow_view"
	if pa.CanEditWorkflows(remote) {
		v.Why.FixHref, v.Why.FixKey = workflowHref(remote, run.Workflow)+"?edit=1", "wf.fix.workflow"
	}
	if msg := workflowBlocked(def.Text, workflow.EnvFor(a.store, remote)); msg != "" {
		v.Why.Raw = msg
	}
}

// reviewNeedLine is the line beside proposals a reader may not decide on: the
// role that verifies knowledge and the people who give it ("Needs Lead · ask
// Mara Keller"). "" when the reader has no refusal to explain.
func reviewNeedLine(l lang, pa *store.ProjectAccess, project string) string {
	return roleNeededShort(l, roleNeededOf(pa.Check(project, store.ResKnowledge, store.ActVerify, store.Object{})))
}
