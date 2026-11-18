package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-712 (P20, S19, R13): packages, features, labels and moving in the browser. A lead uses the forms; a
// member sees the same buttons switched off with the role they need; no form works without the token or the role.

func manageEnv(t *testing.T) (shellEnv, *store.ProjectAccess) {
	t.Helper()
	e, pa := listEnv(t)
	e.St.SetAccessMode(store.AccessMode{Enforce: true})
	return e, pa
}

func packagesOf(t *testing.T, e shellEnv) []requestdomain.PackageSummary {
	t.Helper()
	list, err := e.St.Access(store.Principal{ID: "person:1", Label: "alice", TokenKind: store.WebSessionKind}).PackagesOfProject(shellProject, "")
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestALeadMakesAPackageInTheBrowserAndAGuestSeesTheButtonSwitchedOff(t *testing.T) {
	e, _ := manageEnv(t)
	first := rqMake(t, e.St, rqSpec{Title: "First thing", Criteria: []string{"one"}})
	second := rqMake(t, e.St, rqSpec{Title: "Second thing", Criteria: []string{"two"}})
	base := e.Base + "/ui/x/shell/packages"

	// Nothing yet: the lead gets a way to make the first one instead of a command to copy.
	_, empty := fetchPage(t, e.Lead, base)
	mustContain(t, "lead's empty list", empty, `href="/ui/x/shell/packages?panel=new#new"`, `action="/ui/packages/create"`, `name="branch"`,
		`name="issue" value="`+strconv.FormatInt(first.Request.ID, 10)+`"`, "Second thing")
	mustNotContain(t, "lead's empty list", empty, "ctx package create", "pk-need")
	// A member makes packages as over the API (DEC-51 A); a guest sees the
	// button, off, with the role it needs, and no form.
	_, member := fetchPage(t, e.Member, base)
	mustContain(t, "member's list", member, `action="/ui/packages/create"`)
	_, guest := fetchPage(t, e.Guest, base)
	mustContain(t, "guest's list", guest, `<span class="needrole" id="pk-need">`, "Needs Member", `disabled aria-describedby="pk-need"`)
	mustNotContain(t, "guest's list", guest, `action="/ui/packages/create"`, "ctx package create", "?panel=new")

	form := url.Values{"project": {shellProject}, "branch": {"feat/voice"}, "name": {"Voice answers"}, "pr": {"#31"}, "role": {"optional"},
		"issue": {strconv.FormatInt(first.Request.ID, 10), strconv.FormatInt(second.Request.ID, 10)}}
	resp := postForm(t, e, e.Lead, "/ui/packages/create", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the lead makes a package: %d", resp.StatusCode)
	}
	pkgs := packagesOf(t, e)
	if len(pkgs) != 1 || pkgs[0].Name != "Voice answers" || pkgs[0].Branch != "feat/voice" || pkgs[0].PR != 31 || pkgs[0].Requests != 2 {
		t.Fatalf("the package: %+v", pkgs)
	}
	id := pkgs[0].ID
	if loc := resp.Header.Get("Location"); loc != "/ui/x/shell/packages/"+strconv.FormatInt(id, 10)+"?done=created" {
		t.Fatalf("after making it the page leads to %q", loc)
	}
	detail, err := e.St.PackageByID(id)
	if err != nil || len(detail.Members) != 2 || detail.Members[0].Role != requestdomain.RoleOptional {
		t.Fatalf("the members and their role: %+v %v", detail.Members, err)
	}
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/packages/"+strconv.FormatInt(id, 10)+"?done=created")
	mustContain(t, "lead's package", page, "Package created.", `action="/ui/packages/`+strconv.FormatInt(id, 10)+`/state"`, `name="reason"`, `value="Voice answers"`)
	mustNotContain(t, "lead's package", page, "pk-need")
	_, memberPkg := fetchPage(t, e.Member, e.Base+"/ui/x/shell/packages/"+strconv.FormatInt(id, 10))
	mustContain(t, "member's package", memberPkg, `/state"`)
	_, guestPkg := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/packages/"+strconv.FormatInt(id, 10))
	mustContain(t, "guest's package", guestPkg, `<span class="needrole" id="pk-need">`, `disabled aria-describedby="pk-need"`)
	mustNotContain(t, "guest's package", guestPkg, `/update"`, `/remove"`, `/state"`, `/add"`)
}

func TestAPackageIsChangedFromItsPage(t *testing.T) {
	e, _ := manageEnv(t)
	a := rqMake(t, e.St, rqSpec{Title: "Alpha issue"})
	b := rqMake(t, e.St, rqSpec{Title: "Beta issue"})
	pkg, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/x", Name: "X", Person: "alice", Members: []requestdomain.PackageMember{{RequestID: a.Request.ID, Role: "pflicht"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/packages/" + strconv.FormatInt(pkg.ID, 10)
	_, page := fetchPage(t, e.Lead, e.Base+"/ui/x/shell"+path[3:])
	mustContain(t, "package page", page, "Beta issue", `action="`+path+`/add"`)
	mustNotContain(t, "package page", page, `<option value="`+strconv.FormatInt(a.Request.ID, 10)+`">`)

	if r := postForm(t, e, e.Lead, path+"/add", url.Values{"project": {shellProject}, "issue": {strconv.FormatInt(b.Request.ID, 10)}, "role": {"optional"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); len(d.Members) != 2 {
		t.Fatalf("the issue is not in the package: %+v", d.Members)
	}
	// Taking one out needs the reason, and the log of the issue keeps it.
	resp := postForm(t, e, e.Lead, path+"/remove", url.Values{"project": {shellProject}, "issue": {strconv.FormatInt(b.Request.ID, 10)}, "reason": {" "}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("remove without a reason: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if d, _ := e.St.PackageByID(pkg.ID); len(d.Members) != 2 {
		t.Fatalf("the issue left without a reason: %+v", d.Members)
	}
	if r := postForm(t, e, e.Lead, path+"/remove", url.Values{"project": {shellProject}, "issue": {strconv.FormatInt(b.Request.ID, 10)}, "reason": {"split off"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); len(d.Members) != 1 {
		t.Fatalf("the issue is still in the package: %+v", d.Members)
	}
	// Name and pull request, then close and open again.
	if r := postForm(t, e, e.Lead, path+"/update", url.Values{"project": {shellProject}, "name": {"Renamed"}, "pr": {"44"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("update: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); d.Name != "Renamed" || d.PR != 44 {
		t.Fatalf("the package: %+v", d.Package)
	}
	if r := postForm(t, e, e.Lead, path+"/update", url.Values{"project": {shellProject}, "name": {"Renamed"}, "pr": {""}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("unbind: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); d.PR != 0 {
		t.Fatalf("the pull request stays bound: %d", d.PR)
	}
	if r := postForm(t, e, e.Lead, path+"/state", url.Values{"project": {shellProject}, "state": {"closed"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("close: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); d.State != requestdomain.PackageClosed {
		t.Fatalf("state %q", d.State)
	}
	_, closed := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/packages/"+strconv.FormatInt(pkg.ID, 10))
	mustContain(t, "closed package", closed, "Open again")
	mustNotContain(t, "closed package", closed, `action="`+path+`/add"`)
	if r := postForm(t, e, e.Lead, path+"/state", url.Values{"project": {shellProject}, "state": {"open"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("open: %d", r.StatusCode)
	}
	if d, _ := e.St.PackageByID(pkg.ID); d.State != requestdomain.PackageOpen {
		t.Fatalf("state %q", d.State)
	}
}

// A refused package form lands on the form again, with the refusal in words and what was typed.
func TestARefusedPackageFormSaysWhyAtTheForm(t *testing.T) {
	e, _ := manageEnv(t)
	if _, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/taken", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"project": {shellProject}, "branch": {"feat/taken"}, "name": {"Again"}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, asBrowser(e.Lead), e.Base+"/ui/packages/create", form)
	resp.Body.Close()
	_, back := followRefusal(t, e.Lead, e.Base, resp)
	mustContain(t, "refused form", back, "This branch already has the open package PKG-", `name="branch"`)
	if n := len(packagesOf(t, e)); n != 1 {
		t.Fatalf("%d packages", n)
	}
}

func TestNoPackageFormWorksWithoutTheTokenOrTheRole(t *testing.T) {
	e, _ := manageEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Some issue"})
	pkg, err := e.St.CreatePackage(requestdomain.PackageInput{Project: shellProject, Branch: "feat/y", Name: "Y", Person: "alice", Members: []requestdomain.PackageMember{{RequestID: req.Request.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	pid := strconv.FormatInt(pkg.ID, 10)
	forms := map[string]url.Values{
		"/ui/packages/create":             {"project": {shellProject}, "branch": {"feat/new"}},
		"/ui/packages/" + pid + "/update": {"project": {shellProject}, "name": {"Hijacked"}},
		"/ui/packages/" + pid + "/state":  {"project": {shellProject}, "state": {"closed"}},
		"/ui/packages/" + pid + "/add":    {"project": {shellProject}, "issue": {strconv.FormatInt(req.Request.ID, 10)}},
		"/ui/packages/" + pid + "/remove": {"project": {shellProject}, "issue": {strconv.FormatInt(req.Request.ID, 10)}, "reason": {"no"}},
	}
	for path, form := range forms {
		// Without the token.
		if resp := sameOriginPostForm(t, e.Lead, e.Base+path, cloneValues(form)); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without a token -> %d, want 403", path, resp.StatusCode)
		}
		// A guest.
		if resp := postForm(t, e, e.Guest, path, cloneValues(form)); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as a guest -> %d, want 403 or 404", path, resp.StatusCode)
		}
	}
	if list := packagesOf(t, e); len(list) != 1 || list[0].Name != "Y" || list[0].State != requestdomain.PackageOpen || list[0].Requests != 1 {
		t.Fatalf("a refused form changed something: %+v", list)
	}
	// The guest who asks by hand is told the role.
	form := cloneValues(forms["/ui/packages/create"])
	form.Set("csrf_token", renderedCSRFToken(t, e.Guest, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, e.Guest, e.Base+"/ui/packages/create", form)
	body := readBody(t, resp)
	if !strings.Contains(body, "This needs the role Member in shell. You are Guest there.") {
		t.Errorf("the refusal does not name the role: %s", body)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

func TestALeadMakesAndEditsAFeatureAndLinksIt(t *testing.T) {
	e, pa := manageEnv(t)
	base := "/ui/x/shell/features"
	_, empty := fetchPage(t, e.Lead, e.Base+base)
	mustContain(t, "lead's feature list", empty, `href="`+base+`?panel=new#new"`, `action="/ui/features/create"`, `name="behavior"`, `name="areas"`)
	mustNotContain(t, "lead's feature list", empty, "ctx feature add", "ft-need")
	_, member := fetchPage(t, e.Member, e.Base+base)
	mustContain(t, "member's feature list", member, `action="/ui/features/create"`)
	_, guest := fetchPage(t, e.Guest, e.Base+base)
	mustContain(t, "guest's feature list", guest, `<span class="needrole" id="ft-need">`, "Needs Member", `disabled aria-describedby="ft-need"`)
	mustNotContain(t, "guest's feature list", guest, `action="/ui/features/create"`, "ctx feature add")

	resp := postForm(t, e, e.Lead, "/ui/features/create", url.Values{"project": {shellProject}, "name": {"Voice inbox"}, "summary": {"Answer by voice."},
		"behavior": {"The inbox reads questions aloud."}, "limits": {"German only."}, "cli": {"`ctx inbox --voice`"}, "areas": {"internal/web\ninternal/inbox"}, "message": {"first"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	list, err := pa.SearchFeatures(feature.Filter{Project: shellProject})
	if err != nil || len(list) != 1 {
		t.Fatalf("the features: %+v %v", list, err)
	}
	f := list[0]
	id := strconv.FormatInt(f.ID, 10)
	if f.Name != "Voice inbox" || f.Summary != "Answer by voice." || f.Limits != "German only." || len(f.Areas) != 2 || f.Revision != 1 {
		t.Fatalf("the feature: %+v", f)
	}
	if loc := resp.Header.Get("Location"); loc != base+"/"+id+"?done=created" {
		t.Fatalf("leads to %q", loc)
	}

	// Edit: the form carries the revision, a changed text is a new version.
	_, page := fetchPage(t, e.Lead, e.Base+base+"/"+id+"?panel=edit")
	mustContain(t, "feature page", page, `action="/ui/features/`+id+`/edit"`, `name="base" value="1"`, "Voice inbox", "German only.", `id="link"`)
	if r := postForm(t, e, e.Lead, "/ui/features/"+id+"/edit", url.Values{"project": {shellProject}, "base": {"1"}, "name": {"Voice inbox"}, "summary": {"Answer by voice."},
		"behavior": {"The inbox reads questions aloud."}, "limits": {"German and English."}, "cli": {"`ctx inbox --voice`"}, "areas": {"internal/web\ninternal/inbox"}, "message": {"English too"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit: %d", r.StatusCode)
	}
	got, _ := pa.FeatureView(f.ID)
	if got.Revision != 2 || got.Limits != "German and English." {
		t.Fatalf("after the edit: revision %d, limits %q", got.Revision, got.Limits)
	}
	// A form built on revision 1 does not overwrite revision 2.
	form := url.Values{"project": {shellProject}, "base": {"1"}, "name": {"Voice inbox"}, "summary": {"Stale"}, "message": {"stale"}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/overview"))
	stale := sameOriginPostForm(t, asBrowser(e.Lead), e.Base+"/ui/features/"+id+"/edit", form)
	stale.Body.Close()
	_, back := followRefusal(t, e.Lead, e.Base, stale)
	mustContain(t, "stale form", back, "The feature changed while you were writing")
	if got, _ := pa.FeatureView(f.ID); got.Summary != "Answer by voice." {
		t.Fatalf("the stale form wrote: %q", got.Summary)
	}

	// Links: an issue, an idea, a knowledge entry and a pain, each by its number.
	req := rqMake(t, e.St, rqSpec{Title: "Read aloud"})
	idea1, err := pa.CreateIdea(store.CreateIdeaInput{Project: shellProject, Title: "Speak the answers", Body: "b", Actor: idea.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	kid, err := e.St.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Voice lag", Body: "b", Scope: scope.Axes{Project: shellProject}, Person: "alice", Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	pn, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Nobody hears it", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"REQ-" + strconv.FormatInt(req.Request.ID, 10), "idea " + strconv.FormatInt(idea1.ID, 10), "KN-" + strconv.FormatInt(kid, 10), "PAIN-" + strconv.FormatInt(pn.Pain.ID, 10)} {
		if r := postForm(t, e, e.Lead, "/ui/features/"+id+"/link", url.Values{"project": {shellProject}, "ref": {ref}, "rel": {"affects"}}); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "done=linked") {
			t.Fatalf("link %q: %d %s", ref, r.StatusCode, r.Header.Get("Location"))
		}
	}
	linked, _ := pa.FeatureView(f.ID)
	if len(linked.Affects) != 2 || len(linked.Related) != 2 {
		t.Fatalf("links: affects %+v related %+v", linked.Affects, linked.Related)
	}
	_, withLinks := fetchPage(t, e.Lead, e.Base+base+"/"+id)
	mustContain(t, "linked feature", withLinks, "Read aloud", "Voice lag", "Nobody hears it", `action="/ui/features/`+id+`/unlink"`, `value="knowledge:`+strconv.FormatInt(kid, 10)+`:"`)
	// A wrong spelling is said, and a number of another repository is no link.
	bad := url.Values{"project": {shellProject}, "ref": {"12"}}
	bad.Set("csrf_token", renderedCSRFToken(t, e.Lead, e.Base+"/ui/overview"))
	badResp := sameOriginPostForm(t, asBrowser(e.Lead), e.Base+"/ui/features/"+id+"/link", bad)
	badResp.Body.Close()
	if _, back := followRefusal(t, e.Lead, e.Base, badResp); !strings.Contains(back, "Write the number with its kind") {
		t.Errorf("no word on a bare number: %s", pageSpan(t, back, "<main", "</main>"))
	}
	// Unlink.
	if r := postForm(t, e, e.Lead, "/ui/features/"+id+"/unlink", url.Values{"project": {shellProject}, "link": {"knowledge:" + strconv.FormatInt(kid, 10) + ":"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlink: %d", r.StatusCode)
	}
	if after, _ := pa.FeatureView(f.ID); len(after.Related) != 1 {
		t.Fatalf("related after unlink: %+v", after.Related)
	}
}

func TestNoFeatureFormWorksWithoutTheTokenOrTheRole(t *testing.T) {
	e, pa := manageEnv(t)
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: shellProject, Content: feature.Patch{Name: nStr("Fixed"), Summary: nStr("As is.")}, Actor: feature.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(f.ID, 10)
	forms := map[string]url.Values{
		"/ui/features/create":            {"project": {shellProject}, "name": {"Sneaked in"}},
		"/ui/features/" + id + "/edit":   {"project": {shellProject}, "name": {"Fixed"}, "summary": {"Changed"}},
		"/ui/features/" + id + "/link":   {"project": {shellProject}, "ref": {"REQ-1"}},
		"/ui/features/" + id + "/unlink": {"project": {shellProject}, "link": {"request:1:affects"}},
	}
	for path, form := range forms {
		if resp := sameOriginPostForm(t, e.Lead, e.Base+path, cloneValues(form)); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without a token -> %d, want 403", path, resp.StatusCode)
		}
		if resp := postForm(t, e, e.Guest, path, cloneValues(form)); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as a guest -> %d, want 403 or 404", path, resp.StatusCode)
		}
	}
	list, _ := pa.SearchFeatures(feature.Filter{Project: shellProject})
	if got, _ := pa.FeatureView(f.ID); len(list) != 1 || got.Summary != "As is." || got.Revision != 1 {
		t.Fatalf("a refused form changed something: %d features, %+v", len(list), got)
	}
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/features/"+id)
	mustContain(t, "guest's feature", guest, `<span class="needrole" id="ft-need">`, `disabled aria-describedby="ft-need"`)
	mustNotContain(t, "guest's feature", guest, `/edit"`, `/link"`, `id="link"`)
}

// S19: a label is made where it is wanted, on the issue and in the form of a new one.
func TestALabelIsMadeInPlaceOnTheIssueAndInTheNewIssueForm(t *testing.T) {
	e, pa := manageEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Needs a label"})
	rid := strconv.FormatInt(req.Request.ID, 10)
	_, page := fetchPage(t, e.Member, e.Base+rqPath(req.Request.ID, ""))
	mustContain(t, "issue page", page, `action="/ui/labels/make"`, `name="name"`)
	if r := postForm(t, e, e.Member, "/ui/labels/make", url.Values{"kind": {"request"}, "id": {rid}, "name": {"backend"}}); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "done=label") {
		t.Fatalf("make: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	labels, _ := pa.ListLabels(shellProject)
	if len(labels) != 1 || labels[0].Name != "backend" {
		t.Fatalf("the labels of the project: %+v", labels)
	}
	if on, _ := pa.LabelsOf("request", req.Request.ID); len(on) != 1 || on[0].Name != "backend" {
		t.Fatalf("on the issue: %+v", on)
	}
	// The same name again just puts the one that exists.
	other := rqMake(t, e.St, rqSpec{Title: "Second"})
	if r := postForm(t, e, e.Member, "/ui/labels/make", url.Values{"kind": {"request"}, "id": {strconv.FormatInt(other.Request.ID, 10)}, "name": {"backend"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("again: %d", r.StatusCode)
	}
	if labels, _ := pa.ListLabels(shellProject); len(labels) != 1 {
		t.Fatalf("a second label of the same name: %+v", labels)
	}
	if r := sameOriginPostForm(t, e.Member, e.Base+"/ui/labels/make", url.Values{"kind": {"request"}, "id": {rid}, "name": {"nope"}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("without a token -> %d", r.StatusCode)
	}
	if r := postForm(t, e, e.Guest, "/ui/labels/make", url.Values{"kind": {"request"}, "id": {rid}, "name": {"guest"}}); r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusNotFound {
		t.Errorf("guest -> %d", r.StatusCode)
	}
	if labels, _ := pa.ListLabels(shellProject); len(labels) != 1 {
		t.Fatalf("a refused form made a label: %+v", labels)
	}

	// The form of a new issue: a field for new labels, which are made with the issue.
	_, newPage := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues/new")
	mustContain(t, "new issue form", newPage, `name="new_label"`, "Create a label")
	mustNotContain(t, "new issue form", newPage, `settings#labels`)
	resp := postForm(t, e, e.Member, "/ui/issues/create", url.Values{"project": {shellProject}, "title": {"Brand new"}, "kind": {"issue"}, "type": {"feature"}, "label": {"backend"}, "new_label": {"ui, docs"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create the issue: %d", resp.StatusCode)
	}
	labels, _ = pa.ListLabels(shellProject)
	names := map[string]bool{}
	for _, l := range labels {
		names[l.Name] = true
	}
	if len(labels) != 3 || !names["ui"] || !names["docs"] {
		t.Fatalf("the labels after the new issue: %+v", labels)
	}
	made := resp.Header.Get("Location")
	n, _ := strconv.ParseInt(made[strings.LastIndex(made, "/")+1:], 10, 64)
	if on, _ := pa.LabelsOf("request", n); len(on) != 3 {
		t.Fatalf("on the new issue (%s): %+v", made, on)
	}
	// A guest sees no field to make one.
	_, guest := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/issues/new")
	mustNotContain(t, "guest's form", guest, `name="new_label"`)
}

// R13: an issue that cannot be moved says why, instead of leaving out the button.
func TestAnIssueThatCannotBeMovedSaysWhy(t *testing.T) {
	e, _ := manageEnv(t)
	req := rqMake(t, e.St, rqSpec{Title: "Stays"})
	_, page := fetchPage(t, e.Lead, e.Base+rqPath(req.Request.ID, ""))
	mustContain(t, "issue without a target", page, `id="mv-none"`, "No other project you may write to", `disabled aria-describedby="mv-none"`)
	mustNotContain(t, "issue without a target", page, `?panel=move`)
}
