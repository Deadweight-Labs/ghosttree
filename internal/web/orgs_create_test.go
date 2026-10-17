package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAnyAccountCreatesAnOrganizationInTheWebAndBecomesItsOwner(t *testing.T) {
	base, st, _, anna, _ := orgWeb(t)
	if acct, _ := st.AccountByPrincipalID("person:2"); acct.Admin {
		t.Fatal("fixture: anna must not be an instance admin")
	}
	// The page offers the form to a plain account.
	resp, _ := anna.Get(base + "/ui/orgs")
	if page := body(t, resp); !strings.Contains(page, `action="/ui/orgs/create"`) {
		t.Fatalf("no create form for a plain account: %s", page)
	}
	resp = postOrg(t, anna, base, "/ui/orgs/create", url.Values{"name": {"Team Blau"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "org=team-blau") || !strings.Contains(loc, "notice=created") {
		t.Fatalf("redirect %q does not lead on to inviting", loc)
	}
	if role := st.OrgRole(mustOrgBySlug(t, st, "team-blau").ID, "person:2"); role != store.OrgOwner {
		t.Fatalf("creator role = %q", role)
	}
	next, err := anna.Get(base + loc)
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, next)
	for _, want := range []string{"Organization created", "Team Blau", `action="/ui/orgs/rename"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page after creating lacks %q: %s", want, page)
		}
	}
	// Inviting is one step away: the invitation form, or the way to connect a project first.
	if !strings.Contains(page, `action="/ui/orgs/invite"`) && !strings.Contains(page, "/ui/overview?connect=1") {
		t.Errorf("no way on to inviting or connecting a project: %s", page)
	}
	if !strings.Contains(page, `id="invitations"`) {
		t.Error("the invitation section has no anchor to land on")
	}
}

func TestCreatingAnOrganizationValidatesLikeTheCLI(t *testing.T) {
	base, st, _, anna, _ := orgWeb(t)
	for name, c := range map[string]struct {
		name string
		want int
	}{
		"empty":         {"   ", http.StatusBadRequest},
		"control":       {"Evil\x1b[2Jname", http.StatusBadRequest},
		"too long":      {strings.Repeat("x", 101), http.StatusBadRequest},
		"no slug":       {"日本", http.StatusBadRequest},
		"slug taken":    {"Alpha", http.StatusConflict},
		"valid unicode": {"Büro Süd", http.StatusSeeOther},
	} {
		resp := postOrg(t, anna, base, "/ui/orgs/create", url.Values{"name": {c.name}})
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, c.want)
		}
	}
	orgs, _ := st.ListOrgs("person:2")
	if len(orgs) != 1 {
		t.Errorf("anna owns %d organizations after the checks, want only the valid one", len(orgs))
	}
}

func TestOnlyOwnersAndInstanceAdminsRenameAnOrganization(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	code, _, _ := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	name := func() string { o, _ := st.OrgByRef("alpha"); return o.Name }

	// A member who is no owner: 403, nothing changes, no form on the page.
	resp := postOrg(t, anna, base, "/ui/orgs/rename", url.Values{"org": {"alpha"}, "name": {"Hijacked"}})
	if resp.StatusCode != http.StatusForbidden || name() != "Alpha" {
		t.Fatalf("member rename: %d, name %q", resp.StatusCode, name())
	}
	pageResp, _ := anna.Get(base + "/ui/orgs")
	if strings.Contains(body(t, pageResp), `action="/ui/orgs/rename"`) {
		t.Error("a member is offered the rename form")
	}

	// An owner renames; the address stays.
	resp = postOrgFollow(t, alice, base, "/ui/orgs/rename", url.Values{"org": {"alpha"}, "name": {"Alpha Labs"}})
	if page := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(page, "Organization renamed") || name() != "Alpha Labs" {
		t.Fatalf("owner rename: %d name %q", resp.StatusCode, name())
	}
	if o, err := st.OrgByRef("alpha"); err != nil || o.Slug != "alpha" {
		t.Fatalf("address changed: %+v %v", o, err)
	}

	// A stranger to the organization changes nothing (and learns nothing: 404).
	if _, err := st.AddPerson("ben"); err != nil {
		t.Fatal(err)
	}
	ben := loginInteractive(t, &httptest.Server{URL: base}, st, "ben")
	resp = postOrg(t, ben, base, "/ui/orgs/rename", url.Values{"org": {"alpha"}, "name": {"Hijacked"}})
	if resp.StatusCode != http.StatusNotFound || name() != "Alpha Labs" {
		t.Fatalf("stranger rename: %d name %q", resp.StatusCode, name())
	}

	// The instance admin renames an organization of someone else, member or not.
	resp = postOrg(t, anna, base, "/ui/orgs/create", url.Values{"name": {"Annas Team"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	resp = postOrg(t, alice, base, "/ui/orgs/rename", url.Values{"org": {"annas-team"}, "name": {"Team Anna"}})
	if o, _ := st.OrgByRef("annas-team"); resp.StatusCode != http.StatusSeeOther || o.Name != "Team Anna" {
		t.Fatalf("admin rename: %d name %q", resp.StatusCode, o.Name)
	}
}

func mustOrgBySlug(t *testing.T, st *store.Store, slug string) store.Org {
	t.Helper()
	o, err := st.OrgByRef(slug)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
