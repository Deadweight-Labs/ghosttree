package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/knref"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Abläufe anlegen und bearbeiten im Web (REQ-604 K1). Das Formular schreibt eine
// neue Fassung wie runbook_write: versioniert, mit denselben Prüfungen
// (runbook.ParseSpec) und denselben Rechten (CheckRunbookCreate, CheckRunbook
// ActEdit). Was ausführbar macht, bleibt außerhalb des Webs: ein ausführbares Ziel
// wird hier nur angezeigt und bei einer Bearbeitung unverändert mitgenommen, nie
// angelegt oder geändert; ob eine Fassung auf einer Maschine läuft, entscheidet
// die Erlaubnis des Betreibers (ctx runbook allow), die es im Web nicht gibt. Eine
// hier geschriebene Fassung ist freigegeben, wenn der Schreiber freigeben darf
// (Owner, Lead) und es ankreuzt, sonst nicht: ein Mitglied ändert also nichts an
// dem, was ausgeliefert und ausgeführt wird.

const (
	// maxRunbookForm begrenzt das Formular: ein Ablauf ist kleiner als runbook.MaxSpecBytes.
	maxRunbookForm = 2 * runbook.MaxSpecBytes
	// runbookNew ist die Adresse des Formulars für einen neuen Ablauf und deshalb
	// kein Name.
	runbookNew = "new"
)

type runbookParamRow struct {
	Name, Type, Description, Default, Values string
	Optional                                 bool
}

type runbookStepRow struct{ Title, Command, Check string }

type runbookTargetRow struct {
	Name, Comment string
	Commands      []string
}

// runbookFormView ist das Formular: leer, aus einer Fassung gefüllt oder aus dem
// abgeschickten Formular (nach einem Fehler, damit nichts verloren geht).
type runbookFormView struct {
	New                   bool
	Project, Name         string
	Machine               string
	Purpose               string
	Params                []runbookParamRow
	Preconditions         string
	Steps, Rollback       []runbookStepRow
	Pitfalls              string
	Keywords, Cmds, Paths string
	Note                  string
	Targets               []runbookTargetRow
	CanApprove, Approve   bool
	// BaseVersion and BaseHash name the version the form was made from: a form
	// made from an older one is refused, so that nobody approves or overwrites
	// what they have not seen.
	BaseVersion       int
	BaseHash          string
	Types             []string
	Token, Base, Back string
	// Error is a catalog key ("" none), Detail the text behind it.
	Error, Detail string
}

// blankRows fills a list up to at least n rows with empty ones, so that the form
// offers room for the next lines.
func padParams(rows []runbookParamRow, extra int) []runbookParamRow {
	for i := 0; i < extra; i++ {
		rows = append(rows, runbookParamRow{Type: runbook.TypeString})
	}
	return rows
}

func filledParams(rows []runbookParamRow) []runbookParamRow {
	var out []runbookParamRow
	for _, p := range rows {
		if p.Name != "" || p.Description != "" || p.Default != "" || p.Values != "" {
			out = append(out, p)
		}
	}
	return out
}

func filledSteps(rows []runbookStepRow) []runbookStepRow {
	var out []runbookStepRow
	for _, s := range rows {
		if s.Title != "" || s.Command != "" || s.Check != "" {
			out = append(out, s)
		}
	}
	return out
}

func padSteps(rows []runbookStepRow, extra int) []runbookStepRow {
	for i := 0; i < extra; i++ {
		rows = append(rows, runbookStepRow{})
	}
	return rows
}

func stepRows(in []runbook.Step) []runbookStepRow {
	var out []runbookStepRow
	for _, s := range in {
		out = append(out, runbookStepRow{Title: s.Title, Command: s.Command, Check: s.Check})
	}
	return out
}

func lines(in []string) string { return strings.Join(in, "\n") }

// formFromSpec fills the form from a version.
func formFromSpec(spec runbook.Spec) *runbookFormView {
	v := &runbookFormView{Purpose: spec.Purpose, Preconditions: lines(spec.Preconditions), Keywords: lines(spec.Triggers.Keywords),
		Cmds: lines(spec.Triggers.Commands), Paths: lines(spec.Triggers.Paths)}
	for _, p := range spec.Params {
		v.Params = append(v.Params, runbookParamRow{Name: p.Name, Type: p.Type, Description: p.Description, Optional: p.Optional, Default: p.Default, Values: strings.Join(p.Values, ",")})
	}
	var ids []string
	for _, id := range spec.Pitfalls {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	v.Pitfalls = strings.Join(ids, ", ")
	v.Steps, v.Rollback = stepRows(spec.Steps), stepRows(spec.Rollback)
	for _, t := range spec.Targets {
		row := runbookTargetRow{Name: t.Name, Comment: t.Comment}
		for _, s := range t.Steps {
			row.Commands = append(row.Commands, strings.Join(s.Argv, " "))
		}
		v.Targets = append(v.Targets, row)
	}
	return v
}

// formFromRequest reads the form the way it was sent.
func formFromRequest(r *http.Request) *runbookFormView {
	f := r.PostForm
	v := &runbookFormView{Project: scope.NormalizeRemote(strings.TrimSpace(f.Get("project"))), Name: strings.TrimSpace(f.Get("name")), Machine: strings.TrimSpace(f.Get("machine")),
		Purpose: strings.TrimSpace(f.Get("purpose")), Preconditions: f.Get("preconditions"), Pitfalls: f.Get("pitfalls"), Keywords: f.Get("keywords"), Cmds: f.Get("commands"), Paths: f.Get("paths"),
		Note: strings.TrimSpace(f.Get("note")), New: f.Get("mode") == runbookNew, Approve: f.Get("approve") == "1", BaseHash: strings.TrimSpace(f.Get("base_hash"))}
	v.BaseVersion, _ = strconv.Atoi(f.Get("base_version"))
	at := func(list []string, i int) string {
		if i < len(list) {
			return strings.TrimSpace(list[i])
		}
		return ""
	}
	for i := range f["param_name"] {
		v.Params = append(v.Params, runbookParamRow{Name: at(f["param_name"], i), Type: at(f["param_type"], i), Description: at(f["param_desc"], i),
			Optional: at(f["param_req"], i) == "optional", Default: at(f["param_default"], i), Values: at(f["param_values"], i)})
	}
	for i := range f["step_title"] {
		v.Steps = append(v.Steps, runbookStepRow{Title: at(f["step_title"], i), Command: at(f["step_command"], i), Check: at(f["step_check"], i)})
	}
	for i := range f["back_title"] {
		v.Rollback = append(v.Rollback, runbookStepRow{Title: at(f["back_title"], i), Command: at(f["back_command"], i), Check: at(f["back_check"], i)})
	}
	return v
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// toSpec builds the version from the form; blank rows are left out. targets are
// the ones of the version being edited, kept as they are. The spec goes through
// runbook.ParseSpec like the one of runbook_write, so every rule of the model holds.
func (v *runbookFormView) toSpec(targets []runbook.Target) (runbook.Spec, error) {
	spec := runbook.Spec{Purpose: v.Purpose, Preconditions: splitLines(v.Preconditions), Targets: targets,
		Triggers: runbook.Triggers{Keywords: splitLines(v.Keywords), Commands: splitLines(v.Cmds), Paths: splitLines(v.Paths)}}
	for _, p := range v.Params {
		if p.Name == "" && p.Description == "" && p.Default == "" && p.Values == "" {
			continue
		}
		if !slices.Contains(runbookTypes, p.Type) {
			return runbook.Spec{}, errors.New("parameter " + p.Name + ": the type is one of " + strings.Join(runbookTypes, ", "))
		}
		row := runbook.Param{Name: p.Name, Type: p.Type, Description: p.Description, Optional: p.Optional, Default: p.Default}
		for _, val := range strings.Split(p.Values, ",") {
			if val = strings.TrimSpace(val); val != "" {
				row.Values = append(row.Values, val)
			}
		}
		spec.Params = append(spec.Params, row)
	}
	steps := func(rows []runbookStepRow) (out []runbook.Step) {
		for _, s := range rows {
			if s.Title != "" || s.Command != "" || s.Check != "" {
				out = append(out, runbook.Step{Title: s.Title, Command: s.Command, Check: s.Check})
			}
		}
		return out
	}
	spec.Steps, spec.Rollback = steps(v.Steps), steps(v.Rollback)
	for _, part := range strings.Split(v.Pitfalls, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, ok := knref.Parse(part)
		if !ok {
			return runbook.Spec{}, errors.New("the pitfalls are numbers of knowledge entries, e.g. KN-4294, KN-4293")
		}
		spec.Pitfalls = append(spec.Pitfalls, id)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return runbook.Spec{}, err
	}
	return runbook.ParseSpec(raw)
}

// runbookTypes are the kinds of a parameter, as the model names them.
var runbookTypes = []string{runbook.TypeString, runbook.TypeInt, runbook.TypeSHA, runbook.TypeBool, runbook.TypeEnum, runbook.TypePath}

// runbookForm renders the form: new (empty) or the version being edited.
func (a *app) renderRunbookForm(w http.ResponseWriter, r *http.Request, v *runbookFormView, status int) {
	l := a.langFor(r)
	v.Types = runbookTypes
	v.Token = csrfOf(r)
	// The blank rows of a form that was sent back are dropped before the room for new
	// lines is added, or each refusal would add a set of rows.
	v.Params = filledParams(v.Params)
	v.Params = padParams(v.Params, max(1, 3-len(v.Params)))
	v.Steps = padSteps(filledSteps(v.Steps), 3)
	v.Rollback = padSteps(filledSteps(v.Rollback), 2)
	v.Base = repoHref(v.Project) + "/runbooks"
	title := l.msg("runbooks.form.title_new")
	if !v.New {
		title = l.msg("runbooks.form.title_edit", v.Name)
	}
	r = a.withRepoBand(r, v.Project, "runbooks")
	w.WriteHeader(status)
	a.renderBrowser(w, r, "runbookform", pageData{Title: title, Project: v.Project, RunbookFormV: v})
}

// runbookNewPage is GET /ui/{org}/{repo}/runbooks/new.
func (a *app) runbookNewPage(w http.ResponseWriter, r *http.Request, remote string) {
	pa := a.access(r)
	if !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	if a.accessDenied(w, r, pa.CheckRunbookCreate(remote, "")) {
		return
	}
	a.renderRunbookForm(w, r, &runbookFormView{New: true, Project: remote, CanApprove: pa.MayApproveRunbook(remote, "", true)}, http.StatusOK)
}

// runbookEditPage is GET /ui/{org}/{repo}/runbooks/{name}?edit=1.
func (a *app) runbookEditPage(w http.ResponseWriter, r *http.Request, remote string, ref store.RunbookRef) {
	pa := a.access(r)
	if !interactive(r) {
		a.notInteractive(w, r)
		return
	}
	if a.accessDenied(w, r, pa.CheckRunbook(ref, store.ActEdit)) {
		return
	}
	cur, err := a.store.RunbookView(ref.ID, 0)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := formFromSpec(cur.Spec)
	v.Project, v.Name, v.Machine = remote, ref.Name, ref.Machine
	v.BaseVersion, v.BaseHash = cur.Version, cur.Hash
	v.CanApprove = pa.MayApproveRunbook(ref.Project, ref.Machine, pa.OwnsRunbook(ref))
	a.renderRunbookForm(w, r, v, http.StatusOK)
}

// runbookWrite is POST /ui/runbooks/write: a new runbook or a new version of one.
// An unknown project is as unknown as one the viewer has no role in.
func (a *app) runbookWrite(w http.ResponseWriter, r *http.Request) {
	l := a.langFor(r)
	v := formFromRequest(r)
	pa := a.access(r)
	if v.Project == "" || !a.projectVisible(r, pa, v.Project) {
		http.NotFound(w, r)
		return
	}
	// First the rights, then anything that can answer with the form: a refusal of
	// the input must not tell a person who may not read the runbook that it exists,
	// and the form must not carry targets that person may not see.
	account := a.accountNumber(r)
	own := true
	var targets []runbook.Target
	var head runbook.View
	ref, err := a.store.RunbookRefByScope(v.Project, v.Machine, v.Name, account)
	switch {
	case err == nil:
		if v.New {
			// Only a reader hears that the name is taken; everyone else gets the
			// answer for a runbook that is not there.
			if !pa.CanSeeRunbook(ref) {
				pa.Filtered()
				http.NotFound(w, r)
				return
			}
			break
		}
		if a.accessDenied(w, r, pa.CheckRunbook(ref, store.ActEdit)) {
			return
		}
		own = pa.OwnsRunbook(ref)
		// Executable targets are only displayed: whatever the form says, the new
		// version takes the ones of the stored version unchanged.
		if head, err = a.store.RunbookView(ref.ID, 0); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		targets = head.Spec.Targets
	case errors.Is(err, store.ErrRunbookNotFound):
		if !v.New {
			pa.Filtered()
			http.NotFound(w, r)
			return
		}
		if a.accessDenied(w, r, pa.CheckRunbookCreate(v.Project, v.Machine)) {
			return
		}
	default:
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	// From here the viewer may write; the targets of an edited runbook are theirs to see.
	v.Targets = formFromSpec(runbook.Spec{Targets: targets}).Targets
	v.CanApprove = pa.MayApproveRunbook(v.Project, v.Machine, own)
	// A refusal goes back to the form (not to the address of this POST), with the text
	// over it and every row as it was typed.
	formPage := repoHref(v.Project) + "/runbooks/new"
	if !v.New {
		formPage = runbookHrefIn(v.Project, v.Name) + "?edit=1"
	}
	fail := func(key, detail string) {
		text, field := runbookFailText(l, key, detail)
		a.failFormTo(w, r, formPage, http.StatusUnprocessableEntity, text, field)
	}
	if v.New && err == nil {
		fail("runbooks.form.err.exists", "")
		return
	}
	if !v.New && head.Hash != v.BaseHash {
		// Someone wrote another version since this form was made (or the form
		// names none). What is saved or approved here must be what the person has
		// seen, and the targets of the head are part of it: show the form again with
		// the targets as they are now, nothing approved, and let them decide anew.
		v.BaseVersion, v.BaseHash, v.Approve = head.Version, head.Hash, false
		v.Error = "runbooks.form.err.changed"
		a.renderRunbookForm(w, r, v, http.StatusConflict)
		return
	}
	if err := runbook.ValidName(v.Name); err != nil {
		fail("runbooks.form.err.invalid", err.Error())
		return
	}
	if v.New && v.Name == runbookNew {
		fail("runbooks.form.err.name_new", "")
		return
	}
	if tooLong(v.Note, 500) {
		fail("runbooks.form.err.invalid", l.msg("runbooks.form.err.note"))
		return
	}
	spec, err := v.toSpec(targets)
	if err != nil {
		fail("runbooks.form.err.invalid", err.Error())
		return
	}
	res, err := a.store.WriteRunbook(store.RunbookWrite{
		Project: v.Project, Machine: v.Machine, Name: v.Name, Spec: spec, Note: v.Note,
		Person: personOf(r), AuthorID: account, Approve: v.Approve && v.CanApprove,
		Anchor: gitanchor.Web(),
	})
	if err != nil {
		fail("runbooks.form.err.failed", "")
		return
	}
	done := "saved"
	switch {
	case !res.NewVersion:
		done = "unchanged"
	case res.Approved:
		done = "approved"
	}
	http.Redirect(w, r, runbookHrefIn(v.Project, v.Name)+"?done="+done, http.StatusSeeOther)
}

func runbookHrefIn(project, name string) string {
	return repoHref(project) + "/runbooks/" + pathEscapeAll(name)
}

// projectVisible: the viewer has a role in the project (the same test as the list of the header).
func (a *app) projectVisible(r *http.Request, pa *store.ProjectAccess, remote string) bool {
	opts, _ := a.projectOptions(r, pa)
	for _, o := range opts {
		if o.Remote == remote {
			return true
		}
	}
	return false
}
