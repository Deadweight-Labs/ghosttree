package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// The workflows themselves in the tab "Workflows" (REQ-695, stage E2): the
// list with on/off and the last run, the page of one workflow with its text,
// its versions (each with a diff to the one before) and its runs, and the form
// that creates or changes one. The form saves through workflow.SaveDef, the
// same check and the same version as the MCP tool. Members read, leads and
// owners change (ResWorkflow, until IDEA-46 cuts the right finer).

const maxWorkflowForm = 80 << 10

// workflowNew is the address segment of the form for a new workflow; the
// store refuses it as a name.
const workflowNew = "new"

// wfViaKeys word where a version came from.
var wfViaKeys = map[string]string{store.WorkflowViaWeb: "wf.via.web", store.WorkflowViaMCP: "wf.via.mcp", store.WorkflowViaAPI: "wf.via.api"}

// wfDefRow is one workflow in the list.
type wfDefRow struct {
	Name, Href, Summary, Uses, EventKey string
	Version                             int
	Enabled                             bool
	Last                                *wfRunRow
}

// wfVersionRow is one saved version.
type wfVersionRow struct {
	N               int
	Href, DiffHref  string
	Author, At, Via string
	Note            string
	Current, Shown  bool
}

type workflowDefView struct {
	Name, Project, Base, Href string
	Text, Summary, Uses       string
	EventKey                  string
	Branches                  string
	Version, Shown            int
	Old                       bool
	Author, At                string
	Enabled, CanEdit          bool
	EditHref, Token           string
	Versions                  []wfVersionRow
	Diff                      *docDiffView
	Runs                      []wfRunRow
	Result                    *requestResult
	CmpFrom, CmpTo            int
}

type workflowFormView struct {
	New                 bool
	Project, Name, Text string
	Note, Token, Cancel string
	Base                int
	Errors, Warnings    []workflow.Problem
	ErrorKey            string
	Blocks              []string
}

// workflowStarter is the text a new workflow starts with: a block that needs
// no model, on the default branch.
const workflowStarter = `---
description:
on: pr.merged
inputs: [pr.title]
uses: ghosttree/echo
---
`

func workflowHref(remote, name string) string {
	return repoHref(remote) + "/workflows/" + url.PathEscape(name)
}

func (a *app) accountName(id int64) string {
	if id <= 0 {
		return ""
	}
	acc, err := a.store.AccountByPrincipalID(fmt.Sprintf("person:%d", id))
	if err != nil {
		return ""
	}
	return acc.Name
}

// workflowDefRows lists the workflows of a project with their last runs.
func (a *app) workflowDefRows(l lang, pa *store.ProjectAccess, base, remote string) []wfDefRow {
	defs, err := pa.WorkflowDefs(remote)
	if err != nil {
		return nil
	}
	last, _ := a.store.LastWorkflowRuns(remote)
	var out []wfDefRow
	for _, d := range defs {
		row := wfDefRow{Name: d.Name, Href: workflowHref(remote, d.Name), Summary: d.Summary, Uses: d.Uses, EventKey: wfEventKeys[d.Event],
			Version: d.Version, Enabled: d.Enabled}
		if run, ok := last[d.Name]; ok {
			r := a.wfRow(l, base, run)
			row.Last = &r
		}
		out = append(out, row)
	}
	return out
}

// workflowDefPage is GET /ui/{org}/{repo}/workflows/{name}: the text of the
// newest version (?v=N an older one), the versions, a diff (?from=&to=) and
// the runs of the workflow; ?edit=1 is the form.
func (a *app) workflowDefPage(w http.ResponseWriter, r *http.Request, remote, name string) {
	l := a.langFor(r)
	pa := a.access(r)
	def, err := pa.WorkflowDef(remote, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("edit") == "1" {
		a.workflowEditPage(w, r, pa, def)
		return
	}
	versions, err := pa.WorkflowDefVersions(remote, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	href := workflowHref(remote, name)
	v := &workflowDefView{Name: def.Name, Project: remote, Base: repoHref(remote) + "/workflows", Href: href, Version: def.Version, Shown: def.Version,
		Enabled: def.Enabled, CanEdit: interactive(r) && pa.CanEditWorkflows(remote), Token: csrfOf(r), CmpFrom: max(1, def.Version-1), CmpTo: def.Version}
	if v.CanEdit {
		v.EditHref = href + "?edit=1"
	}
	byN := map[int]store.WorkflowDefVersion{}
	for _, ver := range versions {
		byN[ver.Version] = ver
	}
	if n, err := strconv.Atoi(q.Get("v")); err == nil {
		if _, ok := byN[n]; !ok {
			http.NotFound(w, r)
			return
		}
		v.Shown, v.Old = n, n != def.Version
	}
	shown := byN[v.Shown]
	v.Text, v.Author, v.At = shown.Text, a.accountName(shown.Author), shown.CreatedAt
	parsed, _ := workflow.Parse(shown.Text)
	v.Summary, v.Uses, v.EventKey, v.Branches = parsed.Description, parsed.Uses, wfEventKeys[parsed.On], strings.Join(parsed.Branches, ", ")
	if from, to := q.Get("from"), q.Get("to"); from != "" {
		f, errF := strconv.Atoi(from)
		t, errT := strconv.Atoi(to)
		if to == "" {
			t, errT = def.Version, nil
		}
		fv, okF := byN[f]
		tv, okT := byN[t]
		if errF != nil || errT != nil || !okF || !okT {
			http.NotFound(w, r)
			return
		}
		if f > t {
			fv, tv, f, t = tv, fv, t, f
		}
		lines, added, removed := docLineDiff(fv.Text, tv.Text)
		v.Diff = &docDiffView{From: f, To: t, Lines: lines, Added: added, Removed: removed}
		v.CmpFrom, v.CmpTo = f, t
	}
	for _, ver := range versions {
		row := wfVersionRow{N: ver.Version, Href: href + "?v=" + strconv.Itoa(ver.Version), Author: a.accountName(ver.Author), At: ver.CreatedAt,
			Via: l.msg(wfViaKeys[ver.Via]), Note: ver.Note, Current: ver.Version == def.Version, Shown: ver.Version == v.Shown && v.Diff == nil}
		if ver.Version > 1 {
			row.DiffHref = fmt.Sprintf("%s?from=%d&to=%d", href, ver.Version-1, ver.Version)
		}
		v.Versions = append(v.Versions, row)
	}
	r2, view := a.repoDetailRequest(r, pa, remote, "workflows")
	if runs, err := pa.WorkflowRuns(remote, store.WorkflowRunFilter{Workflow: name, Limit: 50}); err == nil {
		for _, run := range runs {
			v.Runs = append(v.Runs, a.wfRow(l, view.Href, run))
		}
	}
	switch q.Get("done") {
	case "saved":
		v.Result = &requestResult{Text: l.msg("wf.def.saved", def.Version), Kind: "done"}
	case "created":
		v.Result = &requestResult{Text: l.msg("wf.def.created"), Kind: "done"}
	case "unchanged":
		v.Result = &requestResult{Text: l.msg("wf.def.unchanged"), Kind: "done"}
	}
	a.renderBrowser(w, r2, "workflowdef", pageData{Title: def.Name, Project: remote, WorkflowDefV: v})
}

// workflowNewPage is GET /ui/{org}/{repo}/workflows/new.
func (a *app) workflowNewPage(w http.ResponseWriter, r *http.Request, remote string) {
	pa := a.access(r)
	if !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	if a.accessDenied(w, r, pa.Check(remote, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	a.renderWorkflowForm(w, r, &workflowFormView{New: true, Project: remote, Text: workflowStarter}, http.StatusOK)
}

func (a *app) workflowEditPage(w http.ResponseWriter, r *http.Request, pa *store.ProjectAccess, def store.WorkflowDef) {
	if !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	if a.accessDenied(w, r, pa.Check(def.Project, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	a.renderWorkflowForm(w, r, &workflowFormView{Project: def.Project, Name: def.Name, Text: def.Text, Base: def.Version}, http.StatusOK)
}

func (a *app) renderWorkflowForm(w http.ResponseWriter, r *http.Request, v *workflowFormView, status int) {
	l := a.langFor(r)
	v.Token = csrfOf(r)
	v.Blocks = workflow.DefaultCatalog().Names()
	v.Cancel = repoHref(v.Project) + "/workflows"
	title := l.msg("wf.def.new")
	if !v.New {
		v.Cancel = workflowHref(v.Project, v.Name)
		title = l.msg("wf.def.edit", v.Name)
	}
	r = a.withRepoBand(r, v.Project, "workflows")
	w.WriteHeader(status)
	a.renderBrowser(w, r, "workflowform", pageData{Title: title, Project: v.Project, WorkflowFormV: v})
}

// workflowSave is POST /ui/workflows/save: a new workflow or a new version.
// A text that does not pass the check comes back in the form with the
// problems, nothing typed is lost.
func (a *app) workflowSave(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	project := strings.TrimSpace(r.FormValue("project"))
	if project == "" || !a.projectVisible(r, pa, project) || !pa.CanSeeWorkflows(project) {
		http.NotFound(w, r)
		return
	}
	if a.accessDenied(w, r, pa.Check(project, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	v := &workflowFormView{New: r.FormValue("mode") == "new", Project: project, Name: strings.TrimSpace(r.FormValue("name")),
		Text: strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n"), Note: r.FormValue("note")}
	if !v.New {
		v.Base, _ = strconv.Atoi(r.FormValue("base_version"))
		if v.Base <= 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	saved, rep, err := workflow.SaveDef(a.store, browserPrincipal(r).ID, project, v.Name, v.Text, v.Note, store.WorkflowViaWeb, v.Base)
	switch {
	case errors.Is(err, workflow.ErrCheck):
		v.Errors, v.Warnings = rep.Errors, rep.Warnings
		a.renderWorkflowForm(w, r, v, http.StatusUnprocessableEntity)
		return
	case errors.Is(err, store.ErrWorkflowDefName):
		v.ErrorKey = "wf.def.err.name"
	case errors.Is(err, store.ErrWorkflowDefExists):
		v.ErrorKey = "wf.def.err.exists"
	case errors.Is(err, store.ErrWorkflowDefConflict):
		v.ErrorKey = "wf.def.err.changed"
	case errors.Is(err, store.ErrAccessForbidden):
		a.accessDenied(w, r, err)
		return
	case err != nil:
		v.ErrorKey = "wf.def.err.failed"
	}
	if v.ErrorKey != "" {
		a.renderWorkflowForm(w, r, v, http.StatusConflict)
		return
	}
	done := "saved"
	switch {
	case saved.Created:
		done = "created"
	case !saved.Changed:
		done = "unchanged"
	}
	http.Redirect(w, r, workflowHref(project, saved.Def.Name)+"?done="+done, http.StatusSeeOther)
}

// workflowEnable is POST /ui/workflows/enabled: switch a workflow on or off.
func (a *app) workflowEnable(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	project, name := strings.TrimSpace(r.FormValue("project")), strings.TrimSpace(r.FormValue("name"))
	if project == "" || !a.projectVisible(r, pa, project) || !pa.CanSeeWorkflows(project) {
		http.NotFound(w, r)
		return
	}
	err := a.store.SetWorkflowDefEnabled(browserPrincipal(r).ID, project, name, r.FormValue("enabled") == "1")
	switch {
	case errors.Is(err, store.ErrWorkflowDefMissing):
		http.NotFound(w, r)
		return
	case err != nil:
		a.accessDenied(w, r, err)
		return
	}
	back := workflowHref(project, name)
	if r.FormValue("next") == "list" {
		back = repoHref(project) + "/workflows"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// settleWorkflowProposal makes the proposal of a workflow run effective when a
// person approves its staged entry on the review page (REQ-695), before the
// entry is confirmed: the issue, criterion or relation it stands for is
// written, and the run's record says who accepted it. A rejected entry marks
// the proposal rejected. Entries that are no waiting proposal pass unchanged.
func (a *app) settleWorkflowProposal(r *http.Request, k store.Knowledge, plan reviewPlan) error {
	if k.Harness != workflow.Harness || !store.ReservedForWorkflowRun(k.SessionRef) {
		return nil
	}
	by := a.accountNumber(r)
	switch plan.done {
	case "approved":
		_, err := workflow.AcceptProposal(a.store, k.ID, by)
		if errors.Is(err, workflow.ErrNotProposal) {
			return nil
		}
		return err
	case "rejected", "retired":
		if res, err := a.store.WorkflowResultOfNote(k.ID); err == nil {
			_ = a.store.SettleWorkflowResult(res.ID, store.ResultProposed, store.ResultRejected, "", by)
		}
	}
	return nil
}

var wfResultStates = map[string]struct{ key, tone string }{
	store.ResultProposed: {"wf.result.proposed", "neu"},
	store.ResultAccepted: {"wf.result.accepted", "ok"},
	store.ResultAuto:     {"wf.result.auto", "ok"},
	store.ResultRejected: {"wf.result.rejected", "out"},
	store.ResultUndone:   {"wf.result.undone", "out"},
}

var wfResultReasons = map[string]string{
	store.ResultNotDeclared: "wf.result.why.not_declared",
	store.ResultOverMax:     "wf.result.why.over_max",
	store.ResultNoEvidence:  "wf.result.why.no_evidence",
	store.ResultPolicy:      "wf.result.why.policy",
	store.ResultBadTarget:   "wf.result.why.bad_target",
	store.ResultInvalid:     "wf.result.why.invalid",
	"":                      "wf.result.why.declined",
}

// wfResultRows are the results of a run for its page.
func (a *app) wfResultRows(r *http.Request, pa *store.ProjectAccess, remote string, runID int64) []wfResultRow {
	results, err := a.store.WorkflowResultsOf(runID)
	if err != nil {
		return nil
	}
	canUndo := interactive(r) && reviews(pa.Role(remote))
	var out []wfResultRow
	for _, res := range results {
		st := wfResultStates[res.State]
		row := wfResultRow{ID: res.ID, KindKey: wfKindKeys[res.Kind], StateKey: st.key, Tone: st.tone, Ref: res.Ref}
		if row.KindKey == "" {
			row.KindKey = "wf.kind.other"
		}
		if res.State == store.ResultRejected {
			row.ReasonKey = wfResultReasons[res.Reason]
			if row.ReasonKey == "" {
				row.ReasonKey = "wf.result.why.invalid"
			}
		}
		switch {
		case strings.HasPrefix(res.Ref, "KN-"):
			row.RefHref = repoHref(remote) + "/knowledge/" + strings.TrimPrefix(res.Ref, "KN-")
		case strings.HasPrefix(res.Ref, "REQ-"):
			n, _, _ := strings.Cut(strings.TrimPrefix(res.Ref, "REQ-"), "#")
			row.RefHref = repoHref(remote) + "/issues/" + n
		}
		if strings.HasPrefix(res.Ref, "relation:") {
			row.Ref = ""
		}
		row.CanUndo = canUndo && res.State == store.ResultAuto
		out = append(out, row)
	}
	return out
}

var wfKindKeys = map[string]string{
	workflow.ResultKnowledge: "wf.kind.knowledge", workflow.ResultIssue: "wf.kind.issue", workflow.ResultCriterion: "wf.kind.criterion",
	workflow.ResultRelation: "wf.kind.relation", workflow.ResultDescription: "wf.kind.description",
}

// workflowUndo is POST /ui/workflows/undo: take back an automatic approval of
// a workflow. Whoever may review the project's proposals may, as with the
// gardener's automatic approvals.
func (a *app) workflowUndo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	res, err := a.store.WorkflowResultByID(id)
	pa := a.access(r)
	if err != nil || !pa.CanSeeWorkflows(res.Project) || !reviews(pa.Role(res.Project)) {
		http.NotFound(w, r)
		return
	}
	if err := workflow.UndoAuto(a.store, id, a.accountNumber(r), personOf(r)); err != nil {
		http.Error(w, "could not undo", http.StatusConflict)
		return
	}
	http.Redirect(w, r, workflowRunHref(repoHref(res.Project), res.RunID), http.StatusSeeOther)
}

// wfPolicyView is the owner's policy in the settings of a namespace.
type wfPolicyView struct {
	DenyInstruction, DenyPrompt, DenyResearch bool
	Kinds                                     []wfPolicyKind
}

type wfPolicyKind struct {
	Kind, Key           string
	Deny, Auto, CanAuto bool
}

func wfPolicyViewOf(p store.WorkflowPolicy) *wfPolicyView {
	v := &wfPolicyView{DenyInstruction: p.DenyInstruction, DenyPrompt: p.DenyPrompt, DenyResearch: p.DenyResearch}
	for _, k := range workflow.ResultKinds {
		v.Kinds = append(v.Kinds, wfPolicyKind{Kind: k, Key: wfKindKeys[k], Deny: slices.Contains(p.DenyKinds, k), Auto: p.Auto(k),
			CanAuto: slices.Contains(store.WorkflowAutoKinds, k)})
	}
	return v
}

// orgWorkflowPolicySave stores the owner's policy for the workflows of a
// namespace (POST /ui/orgs/workflow-policy); only its owner may.
func (a *app) orgWorkflowPolicySave(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	p := store.WorkflowPolicy{DenyInstruction: r.FormValue("deny_instruction") == "1", DenyPrompt: r.FormValue("deny_prompt") == "1",
		DenyResearch: r.FormValue("deny_research") == "1"}
	for _, k := range r.Form["deny_kind"] {
		if slices.Contains(workflow.ResultKinds, k) && !slices.Contains(p.DenyKinds, k) {
			p.DenyKinds = append(p.DenyKinds, k)
		}
	}
	for _, k := range r.Form["auto_kind"] {
		if slices.Contains(store.WorkflowAutoKinds, k) && !slices.Contains(p.AutoKinds, k) {
			p.AutoKinds = append(p.AutoKinds, k)
		}
	}
	if err := a.store.SetWorkflowPolicy(browserPrincipal(r).ID, o.ID, p); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "workflows")
}
