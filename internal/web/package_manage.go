package web

import (
	"net/http"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Arbeitspakete anlegen und pflegen im Browser (REQ-712, P20): ein Paket anlegen,
// Issues hineinstellen und herausnehmen, es umbenennen, an einen PR binden und
// schließen. Alle Aktionen sind Lead-Aktionen (manage.go). Die Regeln sind die
// des Stores; hier steht nur, was die Formulare lesen und wie eine Ablehnung
// auf Deutsch an der Stelle des Formulars steht.

// packageChoicesMax is how many open issues the forms offer.
const packageChoicesMax = 60

// packageForm is the form that makes a package.
type packageForm struct {
	Project  string
	Branches []string
	Issues   []issueChoice
	Role     string
}

func (a *app) packageFormFor(r *http.Request, pa *store.ProjectAccess, project string) *packageForm {
	branches, _ := a.codeBranches(r, pa, project)
	return &packageForm{Project: project, Branches: branches, Role: requestdomain.RolePflicht,
		Issues: a.openIssueChoices(pa, project, nil, packageChoicesMax)}
}

// packageFailText is the refusal of the store in words, and the field it is about.
func packageFailText(l lang, err error) (text, field string) {
	var ids []string
	if rule, ok := err.(*requestdomain.RuleError); ok {
		ids = rule.IDs
	}
	first := ""
	if len(ids) > 0 {
		first = ids[0]
	}
	switch requestdomain.Code(err) {
	case "secret_detected":
		return l.msg("err.write_secret"), ""
	case "package_exists":
		return l.msg("packages.fail.exists", first), "branch"
	case "pr_taken":
		return l.msg("packages.fail.pr_taken", first), "pr"
	case "branch_required":
		return l.msg("packages.fail.branch"), "branch"
	case "label_too_long", "label_invalid", "name_required":
		return l.msg("packages.fail.label"), "name"
	case "invalid_pr":
		return l.msg("packages.fail.pr"), "pr"
	case "package_closed":
		return l.msg("packages.fail.closed"), ""
	case "removal_reason_required":
		return l.msg("packages.fail.reason"), "reason"
	case "package_not_found":
		return l.msg("packages.fail.gone"), ""
	}
	return l.msg("packages.fail"), ""
}

// packageIssueOK says whether an issue may go into or out of a package of the
// project: it lies there, the viewer reads it and may work on it. Anything else
// answers like an unknown number.
func packageIssueOK(a *app, pa *store.ProjectAccess, project string, n int64) bool {
	ref, err := a.store.RequestRef(store.RefRequest, n)
	if err != nil || ref.Project != project {
		pa.Filtered()
		return false
	}
	obj, visible := pa.RequestObject(ref)
	return visible && pa.Check(project, store.ResRequest, store.ActWork, obj) == nil
}

// packageRoleOf is the role the form chose: optional, or the default.
func packageRoleOf(s string) string {
	if strings.TrimSpace(s) == requestdomain.RoleOptional {
		return requestdomain.RoleOptional
	}
	return requestdomain.RolePflicht
}

// packageManage loads the package a form talks about and checks the right on it.
func (a *app) packageManage(w http.ResponseWriter, r *http.Request) (int64, string, *store.ProjectAccess, bool) {
	id := formNumber(r.PathValue("id"))
	pa := a.access(r)
	if id == 0 {
		http.NotFound(w, r)
		return 0, "", nil, false
	}
	project, err := a.store.PackageRef(id)
	if err != nil {
		pa.Filtered()
		http.NotFound(w, r)
		return 0, "", nil, false
	}
	if err := pa.Check(project, store.ResRequest, store.ActRead, store.Object{}); err != nil {
		if a.accessDenied(w, r, err) {
			return 0, "", nil, false
		}
	}
	if a.accessDenied(w, r, pa.Check(project, store.ResRequest, store.ActEdit, store.Object{})) {
		return 0, "", nil, false
	}
	return id, project, pa, true
}

func packageHref(project string, id int64) string {
	return repoHref(project) + "/packages/" + strconv.FormatInt(id, 10)
}

// packageCreate is POST /ui/packages/create.
func (a *app) packageCreate(w http.ResponseWriter, r *http.Request) {
	project, pa, ok := a.leadProject(w, r, store.ResRequest)
	if !ok {
		return
	}
	l := a.langFor(r)
	back := repoHref(project) + "/packages?panel=new#new"
	fail := func(err error) {
		text, field := packageFailText(l, err)
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
	}
	in := requestdomain.PackageInput{Project: project, Name: r.FormValue("name"), Branch: r.FormValue("branch"), Person: personOf(r)}
	if s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.FormValue("pr")), "#")); s != "" {
		if in.PR = formNumber(s); in.PR == 0 {
			fail(requestdomain.NewRuleError("invalid_pr", "pr must be a pull request number", "", nil))
			return
		}
	}
	role := packageRoleOf(r.FormValue("role"))
	seen := map[int64]bool{}
	for _, raw := range r.Form["issue"] {
		n := formNumber(raw)
		if n == 0 || seen[n] {
			continue
		}
		seen[n] = true
		if !packageIssueOK(a, pa, project, n) {
			a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg("packages.fail.issue", n), "")
			return
		}
		in.Members = append(in.Members, requestdomain.PackageMember{RequestID: n, Role: role})
	}
	detail, err := a.store.CreatePackage(in)
	if err != nil {
		fail(err)
		return
	}
	http.Redirect(w, r, packageHref(project, detail.ID)+"?done=created", http.StatusSeeOther)
}

// packageUpdate is POST /ui/packages/{id}/update: name and pull request.
func (a *app) packageUpdate(w http.ResponseWriter, r *http.Request) {
	id, project, _, ok := a.packageManage(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	back := packageHref(project, id) + "?panel=edit#edit"
	name := strings.TrimSpace(r.FormValue("name"))
	var pr int64
	if s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.FormValue("pr")), "#")); s != "" && s != "0" {
		if pr = formNumber(s); pr == 0 {
			text, field := packageFailText(l, requestdomain.NewRuleError("invalid_pr", "", "", nil))
			a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
			return
		}
	}
	if _, err := a.store.UpdatePackage(id, requestdomain.PackagePatch{Name: &name, PR: &pr}); err != nil {
		text, field := packageFailText(l, err)
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
		return
	}
	http.Redirect(w, r, packageHref(project, id)+"?done=updated", http.StatusSeeOther)
}

// packageState is POST /ui/packages/{id}/state: close or open again.
func (a *app) packageState(w http.ResponseWriter, r *http.Request) {
	id, project, _, ok := a.packageManage(w, r)
	if !ok {
		return
	}
	state := r.FormValue("state")
	if state != requestdomain.PackageOpen && state != requestdomain.PackageClosed {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if _, err := a.store.UpdatePackage(id, requestdomain.PackagePatch{State: &state}); err != nil {
		text, field := packageFailText(a.langFor(r), err)
		a.failFormTo(w, r, packageHref(project, id), http.StatusUnprocessableEntity, text, field)
		return
	}
	http.Redirect(w, r, packageHref(project, id)+"?done="+state, http.StatusSeeOther)
}

// packageAdd is POST /ui/packages/{id}/add: one issue with its role.
func (a *app) packageAdd(w http.ResponseWriter, r *http.Request) {
	id, project, pa, ok := a.packageManage(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	back := packageHref(project, id) + "#add"
	n := formNumber(r.FormValue("issue"))
	if n == 0 || !packageIssueOK(a, pa, project, n) {
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg("packages.fail.issue", n), "issue")
		return
	}
	if _, err := a.store.AddPackageRequests(id, []requestdomain.PackageMember{{RequestID: n, Role: packageRoleOf(r.FormValue("role"))}}, "", personOf(r)); err != nil {
		text, field := packageFailText(l, err)
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
		return
	}
	http.Redirect(w, r, packageHref(project, id)+"?done=added", http.StatusSeeOther)
}

// packageRemove is POST /ui/packages/{id}/remove: one issue out, with the reason the log keeps.
func (a *app) packageRemove(w http.ResponseWriter, r *http.Request) {
	id, project, pa, ok := a.packageManage(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	n := formNumber(r.FormValue("issue"))
	back := packageHref(project, id)
	if n == 0 || !packageIssueOK(a, pa, project, n) {
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg("packages.fail.issue", n), "")
		return
	}
	reason, valid := cleanText(r.FormValue("reason"), maxFieldChars)
	if !valid {
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg("packages.fail.reason"), "reason")
		return
	}
	if _, err := a.store.RemovePackageRequest(id, n, reason, personOf(r)); err != nil {
		text, field := packageFailText(l, err)
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
		return
	}
	http.Redirect(w, r, back+"?done=removed", http.StatusSeeOther)
}

var packageDoneKeys = map[string]string{
	"created": "packages.done.created", "updated": "packages.done.updated", "added": "packages.done.added", "removed": "packages.done.removed",
	requestdomain.PackageOpen: "packages.done.open", requestdomain.PackageClosed: "packages.done.closed",
}

// packageResult is the line a package page shows after a form: the success as ?done=.
func packageResult(r *http.Request, l lang) *requestResult {
	if key := packageDoneKeys[r.URL.Query().Get("done")]; key != "" {
		return &requestResult{Text: l.msg(key), Kind: "done"}
	}
	return nil
}

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("packages.new", "New package", "Neues Paket")
	add("packages.create", "Create package", "Paket anlegen")
	add("packages.f.name", "Name", "Name")
	add("packages.f.branch", "Branch", "Branch")
	add("packages.f.pr", "Pull request", "Pull Request")
	add("packages.f.issues", "Issues", "Issues")
	add("packages.f.role", "Role of the chosen issues", "Rolle der gewählten Issues")
	add("packages.f.reason", "Why does it leave?", "Warum geht es raus?")
	add("packages.f.add", "Add an issue", "Issue hinzufügen")
	add("packages.f.pick", "Choose an issue …", "Issue wählen …")
	add("packages.edit", "Edit", "Bearbeiten")
	add("packages.save", "Save", "Speichern")
	add("packages.close", "Close package", "Paket schließen")
	add("packages.reopen", "Open again", "Wieder öffnen")
	add("packages.add", "Add", "Hinzufügen")
	add("packages.remove", "Take out", "Herausnehmen")
	add("packages.no_open_issues", "No open issue left to add", "Kein offenes Issue mehr zum Hinzufügen")
	add("packages.done.created", "Package created.", "Paket angelegt.")
	add("packages.done.updated", "Package saved.", "Paket gespeichert.")
	add("packages.done.added", "Issue added.", "Issue hinzugefügt.")
	add("packages.done.removed", "Issue taken out.", "Issue herausgenommen.")
	add("packages.done.open", "Package opened again.", "Paket wieder geöffnet.")
	add("packages.done.closed", "Package closed.", "Paket geschlossen.")
	add("packages.fail", "The package could not be saved.", "Das Paket konnte nicht gespeichert werden.")
	add("packages.fail.exists", "This branch already has the open package %s.", "Dieser Branch hat schon das offene Paket %s.")
	add("packages.fail.pr_taken", "This pull request already belongs to %s.", "Dieser Pull Request gehört schon zu %s.")
	add("packages.fail.branch", "A package needs a branch.", "Ein Paket braucht einen Branch.")
	add("packages.fail.label", "Name and branch are one line, at most 200 characters.", "Name und Branch sind eine Zeile, höchstens 200 Zeichen.")
	add("packages.fail.pr", "The pull request is a number, like 42.", "Der Pull Request ist eine Zahl, etwa 42.")
	add("packages.fail.closed", "A closed package takes nothing. Open it again first.", "Ein geschlossenes Paket nimmt nichts mehr auf. Öffne es zuerst wieder.")
	add("packages.fail.reason", "Say why the issue leaves the package.", "Sag, warum das Issue das Paket verlässt.")
	add("packages.fail.issue", "Issue #%d is not in this repository or you may not change it.", "Das Issue #%d gibt es in diesem Repository nicht, oder du darfst es nicht ändern.")
	add("packages.fail.gone", "This package does not exist any more.", "Dieses Paket gibt es nicht mehr.")
	add("packages.empty_lead", "No package yet.", "Noch kein Paket.")
}
