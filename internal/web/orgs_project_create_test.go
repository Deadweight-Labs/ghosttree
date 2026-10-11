package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAMemberCreatesAProjectWithoutGitInTheWeb(t *testing.T) {
	base, st, _, anna, org := orgWeb(t)
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	// A plain member sees the form and uses it.
	resp, _ := anna.Get(base + "/ui/orgs?org=alpha")
	if page := body(t, resp); !strings.Contains(page, `action="/ui/orgs/project/create"`) {
		t.Fatalf("no create-project form for a member: %s", page)
	}
	resp = postOrg(t, anna, base, "/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {"infra"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "org=alpha") || !strings.Contains(loc, "notice=project_created") {
		t.Errorf("redirect %q", loc)
	}
	if role := st.ProjectRole("alpha/infra", "person:2").Role; role != store.RoleOwner {
		t.Errorf("creator role = %q", role)
	}
	next, _ := anna.Get(base + "/ui/orgs?org=alpha&notice=project_created")
	page := body(t, next)
	for _, want := range []string{"Project created", "infra"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q: %s", want, page)
		}
	}
}

func TestCreatingAProjectInTheWebValidatesAndNeedsMembership(t *testing.T) {
	base, st, alice, anna, _ := orgWeb(t)
	for _, c := range []struct {
		name   string
		client *http.Client
		org    string
		pname  string
		want   int
	}{
		{"not a member", anna, "alpha", "infra", http.StatusNotFound},
		{"bad name", alice, "alpha", "Not Valid!", http.StatusBadRequest},
		{"remote-like", alice, "alpha", "github.com/a/b", http.StatusBadRequest},
		{"empty", alice, "alpha", " ", http.StatusBadRequest},
		{"valid", alice, "alpha", "sky-go", http.StatusSeeOther},
		{"taken", alice, "alpha", "sky-go", http.StatusConflict},
		{"unknown org", alice, "nope", "other", http.StatusNotFound},
	} {
		name := c.name
		resp := postOrg(t, c.client, base, "/ui/orgs/project/create", url.Values{"org": {c.org}, "name": {c.pname}})
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, c.want)
		}
	}
	if _, ok := st.ProjectByRemote("alpha/infra"); ok {
		t.Error("a non-member created a project")
	}
	// A form without the CSRF token is refused.
	resp := sameOriginPostForm(t, alice, base+"/ui/orgs/project/create", url.Values{"org": {"alpha"}, "name": {"nocsrf"}})
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("created without a CSRF token")
	}
	if _, ok := st.ProjectByRemote("alpha/nocsrf"); ok {
		t.Error("project exists without a CSRF token")
	}
}
