package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// v01Project makes the project of an organization without git that the usability
// run could not open: alpha/infra, made by its owner, private to start with.
func v01Project(t *testing.T, e shellEnv) string {
	t.Helper()
	if _, err := e.St.CreateNamedProject("person:1", "infra", "alpha"); err != nil {
		t.Fatal(err)
	}
	return "alpha/infra"
}

// AC-675.1: a project without a remote has a repository page with an empty state and
// working tabs, for the owner and for a member.
func TestProjectWithoutARemoteHasARepositoryPage(t *testing.T) {
	e := ovEnv(t)
	id := v01Project(t, e)
	if err := e.St.SetProjectVisibility("person:1", id, true); err != nil {
		t.Fatal(err)
	}
	href := repoHref(id)
	if href != "/ui/-/alpha/infra" {
		t.Fatalf("href of a project without a host = %q", href)
	}
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member} {
		for _, addr := range []string{href, "/ui/alpha/infra"} {
			status, page := fetchPage(t, c, e.Base+addr)
			if status != http.StatusOK {
				t.Fatalf("%s: GET %s = %d", name, addr, status)
			}
			for _, want := range []string{`class="hcrumb"`, `data-tab="issues"><a href="` + href + `/issues"`, `data-tab="knowledge"><a href="` + href + `/knowledge"`} {
				if !strings.Contains(page, want) {
					t.Errorf("%s: %s lacks %q", name, addr, want)
				}
			}
			if strings.Contains(page, `href="https://github.com/alpha`) || strings.Contains(page, "Open on GitHub") {
				t.Errorf("%s: %s links to GitHub for a project that has no remote", name, addr)
			}
		}
		for _, tab := range []string{"/issues", "/knowledge", "/runbooks", "/sessions"} {
			if status, _ := fetchPage(t, c, e.Base+href+tab); status != http.StatusOK {
				t.Errorf("%s: tab %s = %d", name, tab, status)
			}
		}
		if resp, err := c.Get(e.Base + href + "/rooms"); err != nil {
			t.Fatal(err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "project%3Aalpha%2Finfra") {
				t.Errorf("%s: rooms -> %d %q", name, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
	}
	status, page := fetchPage(t, e.Owner, e.Base+href)
	if status != http.StatusOK || !strings.Contains(page, "no-remote") {
		t.Errorf("the code tab of a project without a remote has no empty state: %d", status)
	}
	if status, _ := fetchPage(t, e.Owner, e.Base+href+"/settings"); status != http.StatusOK {
		t.Errorf("owner: settings of a project without a remote = %d", status)
	}
}

// AC-675.4: a member of the organization without a role in the project gets 403 with
// the project, the reason and the owner; an outsider still cannot tell the project
// from one that does not exist.
func TestMemberWithoutAccessToAProjectOfTheirOrgIsToldWhy(t *testing.T) {
	e := ovEnv(t)
	id := v01Project(t, e)
	href := repoHref(id)
	status, page := fetchPage(t, e.Member, e.Base+href)
	if status != http.StatusForbidden {
		t.Fatalf("member of the org without a role: %d, want 403", status)
	}
	text := visibleText(page)
	for _, want := range []string{"alpha/infra", "private", "Alpha", "alice"} {
		if !strings.Contains(text, want) {
			t.Errorf("the 403 page lacks %q: %s", want, text)
		}
	}
	// Every address of the project says the same, whatever it asks for.
	for _, tab := range []string{"/issues", "/issues/1", "/knowledge/2", "/tasks/1", "/ideas/1", "/sessions"} {
		if s, _ := fetchPage(t, e.Member, e.Base+href+tab); s != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", tab, s)
		}
	}
	// The hidden repository of the existing fixtures says it as well.
	if s, p := fetchPage(t, e.Member, e.Base+"/ui/x/hidden"); s != http.StatusForbidden || !strings.Contains(visibleText(p), "x/hidden") {
		t.Errorf("x/hidden for a member: %d", s)
	}
	// A guest of the org and a stranger learn nothing.
	for name, c := range map[string]*http.Client{"guest": e.Guest} {
		hs, hp := fetchPage(t, c, e.Base+href)
		us, up := fetchPage(t, c, e.Base+"/ui/-/alpha/nothere")
		if hs != http.StatusNotFound || us != http.StatusNotFound || hp != up {
			t.Errorf("%s: hidden=%d unknown=%d, answers differ=%v", name, hs, us, hp != up)
		}
	}
	// The owner opens it, and a member with a role does too.
	if s, _ := fetchPage(t, e.Owner, e.Base+href); s != http.StatusOK {
		t.Errorf("owner: %d", s)
	}
	if err := e.St.SetProjectRole("person:1", id, "person:2", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	if s, _ := fetchPage(t, e.Member, e.Base+href); s != http.StatusOK {
		t.Errorf("member with a role: %d", s)
	}
}

// AC-675.3: shared means readable: a member of the org without a role of their own
// reads the project and writes in its room.
func TestSharedProjectIsReadableAndWritableForOrgMembers(t *testing.T) {
	e := ovEnv(t)
	id := v01Project(t, e)
	href := repoHref(id)
	if s, _ := fetchPage(t, e.Member, e.Base+href); s != http.StatusForbidden {
		t.Fatalf("private: %d", s)
	}
	if err := e.St.SetProjectVisibility("person:1", id, true); err != nil {
		t.Fatal(err)
	}
	if s, _ := fetchPage(t, e.Member, e.Base+href+"/issues"); s != http.StatusOK {
		t.Errorf("shared, member without a role reads the issues: %d", s)
	}
	room := "project:" + id
	if s, _ := fetchPage(t, e.Member, e.Base+"/ui/coord?room="+url.QueryEscape(room)); s != http.StatusOK {
		t.Errorf("shared, member opens the room: %d", s)
	}
	if code := postRoom(t, e, e.Member, "/ui/coord/send", room, url.Values{"body": {"hello from a member without a project role"}}); code >= 400 {
		t.Errorf("shared, member writes in the room: %d", code)
	}
	// A guest stays a guest: invited to one project, it gets nothing from sharing
	// another one with the organization.
	org, err := e.St.OrgByRef("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.AddPerson("gast"); err != nil {
		t.Fatal(err)
	}
	code, _, err := e.St.CreateProjectInvitation("person:1", org.ID, shellProject, store.RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.AcceptInvitation("person:6", code); err != nil {
		t.Fatal(err)
	}
	guest := loginInteractive(t, e.Srv, e.St, "gast")
	if role := e.St.ProjectRole(id, "person:6").Role; role != "" {
		t.Errorf("a guest of the org has the role %q in a project shared with it", role)
	}
	if s, _ := fetchPage(t, guest, e.Base+href+"/issues"); s != http.StatusNotFound {
		t.Errorf("a guest of the org reads a shared project: %d", s)
	}
	if s, _ := fetchPage(t, guest, e.Base+"/ui/coord?room="+url.QueryEscape(room)); s != http.StatusNotFound {
		t.Errorf("a guest of the org opens the room of a shared project: %d", s)
	}
}

// AC-675.2: the form takes the address of a repository in every form git knows,
// and the project that comes of it is the one an agent in a clone of that
// repository works in: the address is the identity, so no second project appears.
func TestProjectFormTakesARepositoryAddressAndTheAgentLandsInThatProject(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ input, remote string }{
		{"https://gitlab.com/werkstatt/deploy-hook.git", "gitlab.com/werkstatt/deploy-hook"},
		{"git@gitlab.com:werkstatt/hook-two.git", "gitlab.com/werkstatt/hook-two"},
		{"ssh://git@gitlab.com/werkstatt/hook-three", "gitlab.com/werkstatt/hook-three"},
		{"GitLab.com/Werkstatt/Hook-Four/", "gitlab.com/werkstatt/hook-four"},
	} {
		resp := postOrg(t, anna, base, "/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {c.input}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: %d", c.input, resp.StatusCode)
		}
		p, ok := st.ProjectByRemote(c.remote)
		if !ok || p.Org != "alpha" || p.Named {
			t.Fatalf("%s: project %q = %+v, %v", c.input, c.remote, p, ok)
		}
		if role := st.ProjectRole(c.remote, "person:2").Role; role != store.RoleOwner {
			t.Errorf("%s: the member who made it has the role %q", c.input, role)
		}
		// The agent of another member starts in a clone with this remote (the
		// ssh form and the https form of the same repository): the same project.
		before, _ := st.ListProjects("person:1", org.ID)
		for _, spelling := range []string{c.input, "https://" + c.remote} {
			got, err := st.EnsureProject("person:2", spelling)
			if err != nil || got.ID != p.ID {
				t.Errorf("%s: the agent's project is %+v (%v), not %+v", spelling, got, err, p)
			}
		}
		after, _ := st.ListProjects("person:1", org.ID)
		if len(after) != len(before) {
			t.Errorf("%s: a second project appeared: %d -> %d", c.input, len(before), len(after))
		}
	}
	// A name still makes a project without git; a name that carries the slug of the
	// organization is that name.
	for _, name := range []string{"infra", "alpha/docs"} {
		resp := postOrg(t, alice, base, "/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {name}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	for _, id := range []string{"alpha/infra", "alpha/docs"} {
		if p, ok := st.ProjectByRemote(id); !ok || !p.Named {
			t.Errorf("%s is no named project: %+v %v", id, p, ok)
		}
	}
	// A repository of another organization is refused with a sentence, not a 500.
	if _, err := st.CreateOrg("person:1", "Beta", "beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", "gitlab.com/beta/own", "beta"); err != nil {
		t.Fatal(err)
	}
	resp := postOrg(t, alice, base, "/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {"https://gitlab.com/beta/own"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(page, "another organization") {
		t.Errorf("a repository of another organization: %d", resp.StatusCode)
	}
}

// AC-675.3 (the choice): a person who has the role of a shared project only by
// default is shown as "Default (Member)", never with Owner preselected, so that
// pressing Set cannot make them an owner.
func TestRoleChoiceShowsTheRealDefaultAndNeverPreselectsOwner(t *testing.T) {
	e := ovEnv(t)
	id := v01Project(t, e)
	if err := e.St.SetProjectVisibility("person:1", id, true); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	if !strings.Contains(page, `<option value="" selected>Default (Member)</option>`) {
		t.Errorf("the role choice of a member with the default role does not say so")
	}
	// Whatever select is rendered for a default role, owner is not the selected one.
	for _, sel := range regexp.MustCompile(`(?s)<select class="gfield" name="role".*?</select>`).FindAllString(page, -1) {
		if strings.Contains(sel, "Default (Member)") && regexp.MustCompile(`<option value="owner"[^>]*selected`).MatchString(sel) {
			t.Errorf("owner is preselected: %s", sel)
		}
	}
	// Setting "Default" changes nothing: the member keeps the default role and no
	// row is written.
	form := url.Values{"org": {"alpha"}, "remote": {id}, "account": {"person:2"}, "role": {""}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/orgs"))
	resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/orgs/project/role", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("set default: %d", resp.StatusCode)
	}
	if role := e.St.ProjectRole(id, "person:2"); role.Role != store.RoleMember || !role.Inherited {
		t.Errorf("after Set on the default: %+v", role)
	}
}

// AC-675.5: moving an issue lands on it in the target project and says where it went;
// the old address and the short link lead there too. /ui/requests/new does not 404.
func TestMovedIssueIsFoundAtItsNewPlaceAndTheMoveIsConfirmed(t *testing.T) {
	e := ovEnv(t)
	id := v01Project(t, e)
	d := rqMake(t, e.St, rqSpec{Title: "Misfiled request", Project: shellProject})
	n := strconv.FormatInt(d.Request.ID, 10)
	resp := rqPost(t, e, e.Owner, d.Request.ID, "/move", url.Values{"project": {id}, "reason": {"it is about infra"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("move = %d", resp.StatusCode)
	}
	status, page := fetchPage(t, e.Owner, e.Base+resp.Header.Get("Location"))
	if status != http.StatusOK || !strings.Contains(page, "Moved to alpha/infra") || !strings.Contains(page, "Misfiled request") {
		t.Fatalf("the page after the move: %d, says where it went: %v", status, strings.Contains(page, "Moved to alpha/infra"))
	}
	if !strings.Contains(page, `href="`+repoHref(id)+`/issues"`) {
		t.Errorf("the page is not the one in the target project")
	}
	newAddr := repoHref(id) + "/issues/" + n
	for _, old := range []string{"/ui/x/shell/issues/" + n, "/ui/r/" + n, "/ui/requests/" + n} {
		hop, err := e.Owner.Get(e.Base + old)
		if err != nil {
			t.Fatal(err)
		}
		hop.Body.Close()
		loc := hop.Header.Get("Location")
		if hop.StatusCode != http.StatusFound || !strings.HasPrefix(loc, newAddr) {
			// The short forms go through one more redirect to the repository page.
			if hop.StatusCode != http.StatusFound {
				t.Errorf("%s -> %d, want a redirect to %s", old, hop.StatusCode, newAddr)
				continue
			}
			if s, p := fetchPage(t, e.Owner, e.Base+loc); s != http.StatusOK || !strings.Contains(p, "Misfiled request") {
				t.Errorf("%s -> %s ends at %d", old, loc, s)
			}
		}
	}
	// What the old address would show to someone who cannot open the new project
	// stays a 404.
	if s, _ := fetchPage(t, e.Guest, e.Base+"/ui/x/shell/issues/"+n); s != http.StatusNotFound {
		t.Errorf("the old address for a viewer without access: %d", s)
	}
	// /ui/requests/new leads to the form of a repository the viewer can write to.
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member} {
		hop, err := c.Get(e.Base + "/ui/requests/new")
		if err != nil {
			t.Fatal(err)
		}
		hop.Body.Close()
		if hop.StatusCode != http.StatusFound || !strings.HasSuffix(hop.Header.Get("Location"), "/issues/new") {
			t.Errorf("%s: /ui/requests/new -> %d %q", name, hop.StatusCode, hop.Header.Get("Location"))
		}
		if s, p := fetchPage(t, c, e.Base+hop.Header.Get("Location")); s != http.StatusOK || !strings.Contains(p, "<form") {
			t.Errorf("%s: the form behind it: %d", name, s)
		}
	}
}

// memberStatusFor is what a member of the organization gets for an address in the
// project they have no role in (x/hidden): 403 with the reason. Everything else
// they may not read stays unknown (404).
func memberStatusFor(path string) int {
	if strings.HasPrefix(path, "/ui/x/hidden") {
		return http.StatusForbidden
	}
	return http.StatusNotFound
}

// AC-675.6: two projects with the same name are listed with their owner.
func TestProjectsWithTheSameNameAreListedWithTheirOwner(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.CreateNamedProject("person:1", "deploy-hook", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.ClaimProject("person:1", "github.com/nurrobin/deploy-hook", "alpha"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha")
	for _, want := range []string{">alpha/deploy-hook<", ">nurrobin/deploy-hook<"} {
		if !strings.Contains(page, want) {
			t.Errorf("the organization's list lacks %q", want)
		}
	}
	if strings.Contains(page, ">deploy-hook<") {
		t.Errorf("a project is listed by its bare name although another one has it")
	}
	// The choice in the header and every list that takes its names from it.
	labels := projectLabels([]string{"alpha/deploy-hook", "github.com/nurrobin/deploy-hook", "github.com/nurrobin/other"})
	if labels["alpha/deploy-hook"] != "alpha/deploy-hook" || labels["github.com/nurrobin/deploy-hook"] != "nurrobin/deploy-hook" || labels["github.com/nurrobin/other"] != "other" {
		t.Errorf("labels = %v", labels)
	}
}
