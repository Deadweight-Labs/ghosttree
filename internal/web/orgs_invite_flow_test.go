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

func getOrgs(t *testing.T, c *http.Client, base, query string) string {
	t.Helper()
	resp, err := c.Get(base + "/ui/orgs" + query)
	if err != nil {
		t.Fatal(err)
	}
	return body(t, resp)
}

func TestInvitingIsOneFlowWithAProjectAndARoleAndSurvivesAReload(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	if _, err := st.ClaimProject("person:1", "github.com/x/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	for _, want := range []string{`name="project"`, `name="project_role"`, "Create invitation", `<option value="github.com/x/one" selected>`} {
		if !strings.Contains(page, want) {
			t.Errorf("invite form lacks %q", want)
		}
	}
	for _, gone := range []string{"Email (optional)", "Link for anyone", "7 / 3", "Link for one project"} {
		if strings.Contains(page, gone) {
			t.Errorf("the old wording %q is back", gone)
		}
	}
	// A member is the default role; the page offers no lead link.
	if !strings.Contains(page, `<option value="member" selected>`) || strings.Contains(page, `value="lead"`) {
		t.Errorf("roles: %s", page)
	}

	resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "project": {"github.com/x/one"}, "project_role": {"member"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/ui/orgs?org=alpha&new=") {
		t.Fatalf("an invite must redirect, got %d %q", resp.StatusCode, loc)
	}
	ready, _ := alice.Get(base + loc)
	if ready.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the page with the link must not be cached")
	}
	page = body(t, ready)
	code := between(page, base+"/join/", `"`)
	if !strings.Contains(page, "Invitation ready") || len(code) != 64 {
		t.Fatalf("link missing after the redirect: %s", page)
	}
	if !st.OpenProjectInvitation(code, false) {
		t.Fatal("the link does not open an invitation")
	}
	// F5 on that page and a visit later: the link is in the open invitation.
	for _, q := range []string{loc[len("/ui/orgs"):], "?org=alpha"} {
		if again := getOrgs(t, alice, base, q); !strings.Contains(again, "/join/"+code) {
			t.Errorf("the creator no longer sees the link at %q", q)
		}
	}
	// Another owner of the org does not read it.
	if _, err := st.AcceptInvitation("person:2", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:1", org.ID, "person:2", store.OrgOwner); err != nil {
		t.Fatal(err)
	}
	if other := getOrgs(t, anna, base, loc[len("/ui/orgs"):]); strings.Contains(other, code) {
		t.Fatal("a second owner reads the creator's link")
	}
	// Somebody else's invitation number shows nothing, and a revoked one is gone.
	if p := getOrgs(t, alice, base, "?org=alpha&new=99999"); strings.Contains(p, "Invitation ready") {
		t.Fatal("an unknown invitation produced a ready panel")
	}
	invs, _ := st.ListInvitations("person:1", org.ID)
	var id string
	for _, inv := range invs {
		if inv.ProjectRemote != "" {
			id = itoa(inv.ID)
		}
	}
	if r := postOrg(t, alice, base, "/ui/orgs/invite/revoke", url.Values{"org": {"alpha"}, "id": {id}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if after := getOrgs(t, alice, base, loc[len("/ui/orgs"):]); strings.Contains(after, code) || strings.Contains(after, "Invitation ready") {
		t.Fatal("a revoked invitation still shows its link")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestTheInviteSelectOffersUnclaimedProjectsAndClaimsThem(t *testing.T) {
	base, st, alice, _, org := orgWeb(t)
	uploadWebSession(t, st, 1, "a", "github.com/x/mine")
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, `<option value="github.com/x/mine"`) || strings.Contains(page, "Add to organization and invite") {
		t.Fatalf("the unclaimed project is not part of the one select: %s", page)
	}
	// No hidden claim flag is needed: an unclaimed project of the owner is claimed by inviting to it.
	resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "project": {"github.com/x/mine"}, "project_role": {"member"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("claim and invite: %d %s", resp.StatusCode, body(t, resp))
	}
	if p, ok := st.ProjectByRemote("github.com/x/mine"); !ok || p.OrgID != org.ID {
		t.Fatalf("project not in the org: %+v", p)
	}
	// A project that is neither in the org nor the owner's own upload stays refused.
	if resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "project": {"github.com/x/nobody"}, "project_role": {"member"}}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d", resp.StatusCode)
	}
}

func TestAnOrgOnlyInvitationIsASecondaryOption(t *testing.T) {
	base, st, alice, _, _ := orgWeb(t)
	st.ClaimProject("person:1", "github.com/x/one", "alpha")
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, "<details") || !strings.Contains(page, "Invite to the organization only") {
		t.Fatalf("the org-only invitation is not offered as a side option: %s", page)
	}
	resp := postOrg(t, alice, base, "/ui/orgs/invite", url.Values{"org": {"alpha"}, "role": {"member"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("org-only: %d", resp.StatusCode)
	}
	list := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(list, "Single-use link") || strings.Contains(list, "Link for anyone") {
		t.Fatalf("an open invitation is not named for what it is: %s", list)
	}
	if !strings.Contains(list, "/ui/login/code?code=") {
		t.Fatal("an org-only link must stay copyable too")
	}
}

func TestOrgMembersWithoutAProjectGetAnAddButton(t *testing.T) {
	base, st, alice, _, org := orgWeb(t)
	st.ClaimProject("person:1", "github.com/x/one", "alpha")
	if _, err := st.AcceptInvitation("person:2", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	page := getOrgs(t, alice, base, "?org=alpha")
	if !strings.Contains(page, "No project yet") || !strings.Contains(page, "Add to project") {
		t.Fatalf("a member without a project is not flagged: %s", page)
	}
	resp := postOrg(t, alice, base, "/ui/orgs/project/role", url.Values{"org": {"alpha"}, "remote": {"github.com/x/one"}, "account": {"person:2"}, "role": {"member"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add: %d", resp.StatusCode)
	}
	if got := st.ProjectRole("github.com/x/one", "person:2").Role; got != store.RoleMember {
		t.Fatalf("role %q", got)
	}
	if after := getOrgs(t, alice, base, "?org=alpha"); strings.Contains(after, "No project yet") {
		t.Fatalf("the hint stays after the person got a project: %s", after)
	}
}

func TestTheLastOwnerIsOfferedNeitherDemotionNorLeaving(t *testing.T) {
	base, st, alice, anna, org := orgWeb(t)
	page := getOrgs(t, alice, base, "?org=alpha")
	for _, gone := range []string{"Make member", "Leave", "/ui/orgs/member/remove"} {
		if strings.Contains(page, gone) {
			t.Errorf("the sole owner is offered %q", gone)
		}
	}
	if _, err := st.AcceptInvitation("person:2", mustOrgInvite(t, st, org)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:1", org.ID, "person:2", store.OrgOwner); err != nil {
		t.Fatal(err)
	}
	for who, c := range map[string]*http.Client{"alice": alice, "anna": anna} {
		page = getOrgs(t, c, base, "?org=alpha")
		if !strings.Contains(page, "Make member") || !strings.Contains(page, "Leave") {
			t.Errorf("with two owners %s should get both actions", who)
		}
	}
}

func TestTheDefaultOrganisationIsMarkedByWhatItDoes(t *testing.T) {
	base, _, alice, _, _ := orgWeb(t)
	page := getOrgs(t, alice, base, "?org=alpha")
	if !regexp.MustCompile(`<span class="ad-pill is-soft">For new projects</span>`).MatchString(page) {
		t.Fatalf("the default badge is not explained: %s", page)
	}
	if regexp.MustCompile(`ad-pill is-soft">Default<`).MatchString(page) {
		t.Fatal("a bare \"Default\" badge is back")
	}
}
