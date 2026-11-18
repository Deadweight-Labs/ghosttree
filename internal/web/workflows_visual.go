package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// The block view of a workflow file (REQ-697, stage E4). The page carries the
// text, what the block view draws from it (workflow.ViewOf), the catalogue
// for the palette and the texts; ui-wfvisual.js draws nodes and edges from
// that and lays them out from the order of the file, never from stored
// coordinates. An edit goes to POST /ui/workflows/visual, which applies it to
// the text on the server (workflow.ApplyEdit, the one place that writes the
// format) and answers with the new text and view; the form's textarea holds
// that text, so saving is the save of the text form. Without JavaScript the
// page shows the text.

// wfVisualView is what a page hands to the block view.
type wfVisualView struct {
	Model   string // JSON: {text, view}
	Catalog string // JSON: wfVisualCatalog
	L10n    string // JSON: the wfv.* texts
	Edit    bool
}

type wfvOption struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Events  []string `json:"events,omitempty"`
	Version string   `json:"version,omitempty"`
	Results []string `json:"results,omitempty"`
	Model   bool     `json:"model,omitempty"`
	Instr   bool     `json:"instruction,omitempty"`
}

type wfVisualCatalog struct {
	Events  []wfvOption `json:"events"`
	Inputs  []wfvOption `json:"inputs"`
	Blocks  []wfvOption `json:"blocks"`
	Kinds   []wfvOption `json:"kinds"`
	Aliases []string    `json:"aliases"`
	// DefaultAlias is the alias a new model block starts with.
	DefaultAlias string `json:"default_alias"`
}

type wfVisualModel struct {
	Text string           `json:"text"`
	View workflow.DocView `json:"view"`
}

func wfvJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// wfVisualCatalogOf lists what the palette offers for a project: the events,
// the inputs, the blocks a file may use (not the gardener's), the kinds of
// result and the project's model aliases.
func (a *app) wfVisualCatalogOf(l lang, project string) wfVisualCatalog {
	c := wfVisualCatalog{Aliases: []string{}, DefaultAlias: workflow.DefaultModelAlias}
	for _, e := range store.WorkflowTriggerKinds {
		c.Events = append(c.Events, wfvOption{Name: e, Label: l.msg(wfEventKeys[e])})
	}
	for _, in := range workflow.Inputs {
		c.Inputs = append(c.Inputs, wfvOption{Name: in.Name, Label: wfvLabel(l, wfInputKeys, in.Name), Events: in.Events})
	}
	cat := workflow.DefaultCatalog()
	for _, n := range cat.Names() {
		b := cat[n]
		if b.External {
			continue
		}
		c.Blocks = append(c.Blocks, wfvOption{Name: b.Name, Label: wfvLabel(l, wfBlockKeys, b.Name),
			Events: b.Events, Version: b.Version, Results: b.Results, Model: b.Model, Instr: b.Instruction})
	}
	// The blocks of Robin's examples first, the test blocks last.
	sort.SliceStable(c.Blocks, func(i, j int) bool { return wfvBlockRank(c.Blocks[i].Name) < wfvBlockRank(c.Blocks[j].Name) })
	for _, k := range workflow.ResultKinds {
		c.Kinds = append(c.Kinds, wfvOption{Name: k, Label: wfvLabel(l, wfKindKeys, k)})
	}
	if aliases, _, err := a.store.WorkflowModelAliases(project); err == nil {
		c.Aliases = append(c.Aliases, aliases...)
		slices.Sort(c.Aliases)
	}
	if len(c.Aliases) > 0 && !slices.Contains(c.Aliases, c.DefaultAlias) {
		c.DefaultAlias = c.Aliases[0]
	}
	return c
}

func wfvBlockRank(name string) int {
	switch name {
	case workflow.EchoName:
		return 3
	case workflow.AskName:
		return 2
	}
	return 1
}

// wfVisTexts are the texts of the block view; the page hands them over under
// their name without the prefix.
var wfVisTexts = []string{
	"wf.vis.visual",
	"wf.vis.text",
	"wf.vis.view",
	"wf.vis.errors",
	"wf.vis.errors_one",
	"wf.vis.checked",
	"wf.vis.stage",
	"wf.vis.stage_1",
	"wf.vis.stage_2",
	"wf.vis.stage_3",
	"wf.vis.add",
	"wf.vis.added",
	"wf.vis.checking",
	"wf.vis.check_failed",
	"wf.vis.first_error",
	"wf.vis.remove",
	"wf.vis.close",
	"wf.vis.palette",
	"wf.vis.max",
	"wf.vis.per_run",
	"wf.vis.per_month",
	"wf.vis.every_n",
	"wf.vis.minutes",
	"wf.vis.allowed",
	"wf.vis.research_on",
	"wf.vis.run_usd",
	"wf.vis.month_usd",
	"wf.vis.line",
	"wf.vis.file",
	"wf.vis.choose_on",
	"wf.vis.choose_uses",
	"wf.vis.group.inputs",
	"wf.vis.group.results",
	"wf.vis.group.more",
	"wf.vis.failed",
	"wf.vis.not_catalog",
	"wf.vis.alias",
	"wf.vis.branch",
	"wf.vis.node.description",
	"wf.vis.node.on",
	"wf.vis.node.branches",
	"wf.vis.node.every",
	"wf.vis.node.chain",
	"wf.vis.node.inputs",
	"wf.vis.node.uses",
	"wf.vis.node.body",
	"wf.vis.node.prompt",
	"wf.vis.node.model",
	"wf.vis.node.budget",
	"wf.vis.node.research",
	"wf.vis.node.text",
	"wf.vis.node.results",
}

// wfInputTexts and wfBlockKeys name the inputs and blocks of the catalogue for people.
var wfInputTexts = []string{
	"wf.in.event",
	"wf.in.pr.number",
	"wf.in.pr.title",
	"wf.in.pr.base",
	"wf.in.pr.head",
	"wf.in.pr.merge_sha",
	"wf.in.idea.number",
	"wf.in.idea.title",
	"wf.in.schedule.slot",
	"wf.in.pr.text",
	"wf.in.pr.commits",
	"wf.in.pr.files",
	"wf.in.pr.diff",
	"wf.in.pr.knowledge",
	"wf.in.pr.sessions.user_messages",
	"wf.in.knowledge.related",
	"wf.in.requests.open",
	"wf.in.idea.text",
	"wf.in.idea.concept",
	"wf.in.idea.decisions",
	"wf.in.idea.related",
	"wf.in.features.related",
}

var wfBlockKeys = map[string]string{
	"ghosttree/echo":         "wf.blk.echo",
	"ghosttree/ask":          "wf.blk.ask",
	"ghosttree/pr-knowledge": "wf.blk.pr_knowledge",
	"ghosttree/pr-wishes":    "wf.blk.pr_wishes",
	"ghosttree/idea-match":   "wf.blk.idea_match",
	"ghosttree/criteria":     "wf.blk.criteria",
}

// wfInputKeys maps an input to its text: the text key is wf.in.<input>.
var wfInputKeys = func() map[string]string {
	m := map[string]string{}
	for _, k := range wfInputTexts {
		m[wfvKeyName(k)] = k
	}
	return m
}()

// wfvKeyName is a text key without its first two parts: the input of
// wf.in.pr.diff, the name of wf.vis.palette.
func wfvKeyName(key string) string {
	parts := strings.SplitN(key, ".", 3)
	return parts[len(parts)-1]
}

// wfvLabel is the label of a catalogue entry, or its name when it has none.
func wfvLabel(l lang, keys map[string]string, name string) string {
	if k, ok := keys[name]; ok {
		return l.msg(k)
	}
	return name
}

// wfVisualL10n are the texts of the block view in the page's language.
func wfVisualL10n(l lang) string {
	out := map[string]string{}
	for _, k := range wfVisTexts {
		out[wfvKeyName(k)] = l.msg(k)
	}
	return wfvJSON(out)
}

// wfVisualFor builds the block view of a text for a page.
func (a *app) wfVisualFor(l lang, project, text string, edit bool) *wfVisualView {
	env := workflow.EnvFor(a.store, project)
	return &wfVisualView{
		Model:   wfvJSON(wfVisualModel{Text: text, View: workflow.ViewOf(text, env)}),
		Catalog: wfvJSON(a.wfVisualCatalogOf(l, project)),
		L10n:    wfVisualL10n(l),
		Edit:    edit,
	}
}

// workflowVisual is POST /ui/workflows/visual: one edit of the block view.
// It takes the text of the form and an edit (JSON, workflow.Edit; empty: only
// read the text) and answers with the new text and what the block view draws
// from it. It stores nothing; saving stays POST /ui/workflows/save.
func (a *app) workflowVisual(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	project := strings.TrimSpace(r.FormValue("project"))
	if project == "" || !a.projectVisible(r, pa, project) || !pa.CanSeeWorkflows(project) {
		http.NotFound(w, r)
		return
	}
	if a.accessDenied(w, r, pa.Check(project, store.ResWorkflow, store.ActEdit, store.Object{})) {
		return
	}
	text := strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n")
	if len(text) > workflow.MaxDefBytes {
		writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "the file is too large"})
		return
	}
	if raw := strings.TrimSpace(r.FormValue("edit")); raw != "" {
		var e workflow.Edit
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "the edit is no JSON object"})
			return
		}
		next, err := workflow.ApplyEdit(text, e)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, workflow.ErrEdit) {
				msg = strings.TrimPrefix(msg, workflow.ErrEdit.Error()+": ")
			}
			writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
			return
		}
		text = next
	}
	writeJSONStatus(w, http.StatusOK, wfVisualModel{Text: text, View: workflow.ViewOf(text, workflow.EnvFor(a.store, project))})
}

// workflowCheckAnswer is what the live check of the text mode answers with.
type workflowCheckAnswer struct {
	OK       bool               `json:"ok"`
	Stage    int                `json:"stage"`
	Errors   []workflow.Problem `json:"errors"`
	Warnings []workflow.Problem `json:"warnings"`
}

// workflowCheck is POST /ui/workflows/check: the check of a text, for the text
// mode while a person types (REQ-708). It stores nothing and changes nothing,
// so it takes the rights of reading workflows, not of editing them; the check
// is the same as at saving.
func (a *app) workflowCheck(w http.ResponseWriter, r *http.Request) {
	pa := a.access(r)
	project := strings.TrimSpace(r.FormValue("project"))
	if project == "" || !a.projectVisible(r, pa, project) || !pa.CanSeeWorkflows(project) {
		http.NotFound(w, r)
		return
	}
	text := strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n")
	if len(text) > workflow.MaxDefBytes {
		writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "the file is too large"})
		return
	}
	_, rep := workflow.Check(text, workflow.EnvFor(a.store, project))
	ans := workflowCheckAnswer{OK: rep.OK(), Stage: rep.Stage, Errors: rep.Errors, Warnings: rep.Warnings}
	if ans.Errors == nil {
		ans.Errors = []workflow.Problem{}
	}
	if ans.Warnings == nil {
		ans.Warnings = []workflow.Problem{}
	}
	writeJSONStatus(w, http.StatusOK, ans)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
