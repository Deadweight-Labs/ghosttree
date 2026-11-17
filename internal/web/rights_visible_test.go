package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-704 (S03): a member sees the buttons for a new workflow switched off,
// with the role they need and whom to ask, in both modes of the server. In
// the log mode of the usability run the buttons used to be live and only the
// save refused, with "Your role may not do this."
func TestWorkflowButtonsOfAMemberAreOffWithTheRoleAndWhomToAsk(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		t.Run(fmt.Sprint("enforce=", enforce), func(t *testing.T) {
			e, _ := workflowEnv(t)
			e.St.SetAccessMode(store.AccessMode{Enforce: enforce})
			text := "---\ndescription: Merges melden\non: pr.merged\nuses: ghosttree/echo\n---\n"
			if r := postForm(t, e, e.Lead, "/ui/workflows/save", url.Values{"project": {shellProject}, "mode": {"new"}, "name": {"melden"}, "text": {text}}); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("the lead creates: %d", r.StatusCode)
			}
			_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows")
			mustContain(t, "member list", list, `<span class="needrole" id="wf-need">`, "Needs Lead · ask alice", `disabled aria-describedby="wf-need"`)
			mustNotContain(t, "member list", list, `href="/ui/x/shell/workflows/new"`, `action="/ui/workflows/template"`)
			_, def := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/melden")
			mustContain(t, "member workflow", def, "Needs Lead · ask alice", `disabled aria-describedby="wf-need"`)
			mustNotContain(t, "member workflow", def, "?edit=1", `action="/ui/workflows/enabled"`)
			_, lead := fetchPage(t, e.Lead, e.Base+"/ui/x/shell/workflows")
			mustContain(t, "lead list", lead, `href="/ui/x/shell/workflows/new"`, `action="/ui/workflows/template"`)
			mustNotContain(t, "lead list", lead, "wf-need")
		})
	}
}

// REQ-704 (S03): what the member reaches anyway (an old tab, a typed address)
// says the role and whom to ask, on the page and on the form it came from.
func TestARefusalForAMissingRoleNamesTheRoleAndWhomToAsk(t *testing.T) {
	e, _ := workflowEnv(t)
	want := "This needs the role Lead in shell. You are Member there. Ask alice for it."
	status, page := fetchPage(t, e.Member, e.Base+"/ui/x/shell/workflows/new")
	if status != http.StatusForbidden || !strings.Contains(page, want) {
		t.Fatalf("the page of a missing role: %d %s", status, pageSpan(t, page, "<main", "</main>"))
	}
	form := url.Values{"project": {shellProject}, "template": {"wissen-nach-merge"}, backField: {"/ui/x/shell/workflows"}}
	form.Set("csrf_token", renderedCSRFToken(t, e.Member, e.Base+"/ui/overview"))
	resp := sameOriginPostForm(t, asBrowser(e.Member), e.Base+"/ui/workflows/template", form)
	resp.Body.Close()
	if _, back := followRefusal(t, e.Member, e.Base, resp); !strings.Contains(back, want) || strings.Contains(back, "Your role may not do this.") {
		t.Fatalf("the refused form does not name the role: %s", pageSpan(t, back, "<main", "</main>"))
	}
	if defs, _ := e.St.WorkflowDefsIn(shellProject); len(defs) != 0 {
		t.Fatalf("a member made a workflow: %+v", defs)
	}
}

// REQ-704 (S04): every role of a person stands in their row of the member
// list, and whoever may change it changes it there: the owner any role, a
// lead member and guest in the project, also for a member who has no role yet
// and whom the enforced list would not show the lead.
func TestTheMemberListShowsAndChangesEveryProjectRole(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		t.Run(fmt.Sprint("enforce=", enforce), func(t *testing.T) {
			e := shellWebAll(t)
			e.St.SetAccessMode(store.AccessMode{Enforce: enforce})
			if _, err := e.St.AddPerson("nora"); err != nil {
				t.Fatal(err)
			}
			orgs, _ := e.St.ListOrgs("person:1")
			code, _, err := e.St.CreateInvitation("person:1", orgs[0].ID, "", store.OrgMember, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.St.AcceptInvitation("person:6", code); err != nil {
				t.Fatal(err)
			}
			_, owner := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
			members := pageSpan(t, owner, `id="members"`, `id="projects"`)
			anna := pageSpan(t, members, `id="m-person_2"`, `class="row mrow"`)
			mustContain(t, "anna's row", anna, `class="mproj" id="pr-github_com_x_shell-person_2"`, `name="remote" value="github.com/x/shell"`, `<option value="lead">Lead</option>`, `<option value="member" selected>Member</option>`)
			lars := pageSpan(t, members, `id="m-person_4"`, `class="row mrow"`)
			mustContain(t, "lars's row", lars, `<option value="lead" selected>Lead</option>`)
			// The projects keep a summary that leads to the person's row.
			projects := pageSpan(t, owner, `id="projects"`, `id="sources"`)
			mustContain(t, "projects", projects, `href="#pr-github_com_x_shell-person_2"`)
			mustNotContain(t, "projects", projects, `action="/ui/orgs/project/role"`)

			_, lead := fetchPage(t, e.Lead, e.Base+"/ui/orgs?org=alpha")
			leadMembers := pageSpan(t, lead, `id="members"`, `id="projects"`)
			mustContain(t, "lead's member list", leadMembers, `name="account" value="person:2"`, `name="account" value="person:6"`)
			mustNotContain(t, "lead's member list", leadMembers, `<option value="owner">`)
			if r := postForm(t, e, e.Lead, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {shellProject}, "account": {"person:6"}, "role": {"member"}}); r.StatusCode != http.StatusSeeOther || !strings.HasSuffix(r.Header.Get("Location"), "#pr-github_com_x_shell-person_6") {
				t.Fatalf("the lead gives a role from the member list: %d %s", r.StatusCode, r.Header.Get("Location"))
			}
			if got := e.St.ProjectRole(shellProject, "person:6").Role; got != store.RoleMember {
				t.Fatalf("nora's role: %q", got)
			}
		})
	}
}
