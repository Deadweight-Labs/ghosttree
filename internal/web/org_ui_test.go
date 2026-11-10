package web

import (
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOrgPageHasTheBandAndEveryTabAnswers(t *testing.T) {
	e := ovEnv(t)
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha")
	for _, want := range []string{`class="rband`, `data-tab="repos"`, `data-tab="agents"`, `data-tab="people"`, `data-tab="rooms"`, `data-tab="settings"`, `href="/ui/alpha/agents"`, "Restricted"} {
		if !strings.Contains(page, want) {
			t.Errorf("org page lacks %q", want)
		}
	}
	// Sharing with the org is not public reading; the list must not say so (#4371).
	for _, gone := range []string{"Knowledge public", "Wissen öffentlich"} {
		if strings.Contains(page, gone) {
			t.Errorf("org page still says %q", gone)
		}
	}
	for _, tab := range []string{"agents", "people", "rooms"} {
		if status, p := fetchPage(t, e.Owner, e.Base+"/ui/alpha/"+tab); status != http.StatusOK || !strings.Contains(p, `aria-current="page"`) {
			t.Errorf("tab %s: status %d", tab, status)
		}
	}
	// Settings are the owner's; they lead to the administration page.
	resp, err := e.Owner.Get(e.Base + "/ui/alpha/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/orgs?org=alpha" {
		t.Errorf("settings -> %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/alpha/settings"); status != http.StatusNotFound {
		t.Errorf("a member opens the settings: %d", status)
	}
	// Somebody else's organisation is as unknown as one that does not exist.
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/nothere/people"); status != http.StatusNotFound {
		t.Errorf("an unknown organisation answers %d", status)
	}
}

// REQ-623: services are no people. They have their own tab, which guests do
// not get.
func TestServicesHaveTheirOwnTabAndAreNotPeople(t *testing.T) {
	e := ovEnv(t)
	org, _ := e.St.OrgByRef("alpha")
	if err := e.St.SetAccountKindAsOperator(5, store.AccountKindService, org.ID); err != nil {
		t.Fatal(err)
	}
	_, people := fetchPage(t, e.Owner, e.Base+"/ui/alpha/people")
	if strings.Contains(people, "<b>rita</b>") || !strings.Contains(people, "<b>anna</b>") {
		t.Errorf("people tab mixes in the service or lost a person")
	}
	if !strings.Contains(people, `data-tab="services"`) {
		t.Errorf("owner lacks the services tab")
	}
	status, services := fetchPage(t, e.Member, e.Base+"/ui/alpha/services")
	if status != http.StatusOK || !strings.Contains(services, "<b>rita</b>") || !strings.Contains(services, "Service of Alpha") {
		t.Errorf("services tab: %d", status)
	}
	if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/alpha/services"); status != http.StatusNotFound {
		t.Errorf("guest opens the services tab: %d", status)
	}
	if _, page := fetchPage(t, e.Guest, e.Base+"/ui/alpha"); strings.Contains(page, `data-tab="services"`) {
		t.Errorf("guest sees the services tab")
	}
}

// REQ-623: the org shows the picture of its GitHub organization, and a former
// address leads its members to the current one; strangers learn nothing.
func TestOrgHeadAndMovedAddress(t *testing.T) {
	e := ovEnv(t)
	org, _ := e.St.OrgByRef("alpha")
	if _, err := e.St.ConnectSourceWithInstallation("person:1", org.ID, store.SourceGitHub, "alpha-gh", 31, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetOrgFollowGitHub("person:1", org.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.MirrorGitHubOrg(org.ID, "alpha-gh", "Alpha-GH", "Alpha Inc"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Member, e.Base+"/ui/alpha-gh")
	if !strings.Contains(page, "/ui/avatar/org/") || !strings.Contains(page, "Alpha Inc") || !strings.Contains(page, "https://github.com/Alpha-GH") {
		t.Errorf("org head lacks picture, name or GitHub link")
	}
	noFollow := *e.Member
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Get(e.Base + "/ui/alpha/people")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// 302, not 301: browsers must not remember the move (security review 10).
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/alpha-gh/people" {
		t.Errorf("old address: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, _ := fetchPage(t, e.Member, e.Base+"/ui/avatar/org/999"); status != http.StatusNotFound {
		t.Errorf("picture of an unknown org: %d", status)
	}
}

// REQ-623: a person's own namespace is a page of its own, with their
// repositories (private, or shared with collaborators) and their machines.
func TestUserPageShowsOwnReposAndMachines(t *testing.T) {
	e := ovEnv(t)
	if err := e.St.SetAccountGitHub("person:2", "anna-gh", 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.ClaimProject("person:2", "github.com/anna-gh/notes", ""); err != nil {
		t.Fatal(err)
	}
	ns, ok := e.St.UserNamespace("person:2")
	if !ok {
		t.Fatal("no namespace")
	}
	if err := e.St.ClaimMachine("anna-box", "person:2"); err != nil {
		t.Fatal(err)
	}
	status, page := fetchPage(t, e.Member, e.Base+"/ui/"+ns.Slug)
	if status != http.StatusOK || !strings.Contains(page, ">notes<") || !strings.Contains(page, "Private") {
		t.Fatalf("user page: %d", status)
	}
	for _, gone := range []string{`data-tab="people"`, `data-tab="services"`, "Shared with the org"} {
		if strings.Contains(page, gone) {
			t.Errorf("user page shows %q", gone)
		}
	}
	if _, machines := fetchPage(t, e.Member, e.Base+"/ui/"+ns.Slug+"/machines"); !strings.Contains(machines, "anna-box") || strings.Contains(machines, "Shared with the organization") {
		t.Errorf("user machines tab wrong")
	}
	// Nobody else opens it, not even the owner of the org.
	if status, _ := fetchPage(t, e.Owner, e.Base+"/ui/"+ns.Slug); status != http.StatusNotFound {
		t.Errorf("another person opens the user page: %d", status)
	}
	// The settings of a namespace have no members and no sources.
	settings := getOrgs(t, e.Member, e.Base, "?org="+ns.Slug)
	for _, gone := range []string{`id="members"`, `id="sources"`, `action="/ui/orgs/project/visibility"`} {
		if strings.Contains(settings, gone) {
			t.Errorf("namespace settings contain %q", gone)
		}
	}
}

// Decision 14 A of #4376: guests see their project only, no page of the
// organization and no details of a machine shared with it; owners and members
// see them.
func TestGuestsSeeNoOrganizationPagesAndNoMachineShareDetails(t *testing.T) {
	e := ovEnv(t)
	switchGardener(t, e.St)
	if err := e.St.ClaimMachine("buildbox", "person:1"); err != nil {
		t.Fatal(err)
	}
	// The machine signed in itself once (device login), which proves it.
	if _, err := e.St.DB().Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, machine, created_at) VALUES(1, 'buildbox-proof', 'buildbox', 'device', 'buildbox', '2026-11-09T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.ShareMachineAsOperator("buildbox", "alpha", "read", "svc", "/srv/app"); err != nil {
		t.Fatal(err)
	}
	tabs := []string{"", "/people", "/services", "/agents", "/machines", "/rooms", "/gardener"}
	for _, tab := range tabs {
		if status, _ := fetchPage(t, e.Guest, e.Base+"/ui/alpha"+tab); status != http.StatusNotFound {
			t.Errorf("guest opens /ui/alpha%s: %d", tab, status)
		}
	}
	for name, c := range map[string]*http.Client{"owner": e.Owner, "member": e.Member} {
		for _, tab := range []string{"", "/people", "/services", "/agents", "/machines", "/rooms"} {
			if status, _ := fetchPage(t, c, e.Base+"/ui/alpha"+tab); status != http.StatusOK {
				t.Errorf("%s: /ui/alpha%s = %d", name, tab, status)
			}
		}
		// Every member creates projects (17 A, #4376).
		if _, page := fetchPage(t, c, e.Base+"/ui/alpha"); !strings.Contains(page, `href="/ui/orgs?org=alpha#projects"`) {
			t.Errorf("%s lacks \"New repository\"", name)
		}
		if _, page := fetchPage(t, c, e.Base+"/ui/m/buildbox"); !strings.Contains(page, "/srv/app") {
			t.Errorf("%s lacks the share details", name)
		}
	}
	// The guest reaches the machine through the organization, but without the
	// user and the paths of the share.
	status, page := fetchPage(t, e.Guest, e.Base+"/ui/m/buildbox")
	if status != http.StatusOK || strings.Contains(page, "/srv/app") || strings.Contains(page, ">svc<") {
		t.Error("a guest sees the user and the paths of a shared machine")
	}
	if _, page := fetchPage(t, e.Guest, e.Base+"/ui/inbox"); strings.Contains(page, `href="/ui/alpha"`) {
		t.Error("the guest's menu leads to the organization")
	}
	if strings.Contains(getOrgs(t, e.Guest, e.Base, "?org=alpha"), `id="members"`) {
		t.Error("a guest gets the organization's settings")
	}
}

// Security review, finding 2d, in the browser: the organization lists what
// waits for it, the person hands it over, an owner takes it over.
func TestHandoverOfAProjectUnderALaterPrefix(t *testing.T) {
	e := ovEnv(t)
	// From before the organization connected GitHub: the person held it.
	if _, err := claimOrPlace(e.St, "person:2", "github.com/alpha-gh/x", ""); err != nil {
		t.Fatal(err)
	}
	org, _ := e.St.OrgByRef("alpha")
	if _, err := e.St.ConnectSourceWithInstallation("person:1", org.ID, store.SourceGitHub, "alpha-gh", 8, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.St.ClaimPrefix("person:1", org.ID, "github.com/alpha-gh/*", store.VisibilityShared); err != nil {
		t.Fatal(err)
	}
	if _, page := fetchPage(t, e.Owner, e.Base+"/ui/alpha"); !strings.Contains(page, "Waiting to be taken over") || !strings.Contains(page, "github.com/alpha-gh/x") {
		t.Fatal("the organization does not list the waiting project")
	}
	if _, page := fetchPage(t, e.Member, e.Base+"/ui/alpha"); strings.Contains(page, "github.com/alpha-gh/x") {
		t.Error("a member sees another person's waiting project")
	}
	ns, _ := e.St.UserNamespace("person:2")
	_, mine := fetchPage(t, e.Member, e.Base+"/ui/"+ns.Slug)
	if !strings.Contains(mine, `action="/ui/handover"`) {
		t.Fatal("the person cannot hand it over")
	}
	form := url.Values{"remote": {"github.com/alpha-gh/x"}, "csrf_token": {renderedCSRFToken(t, e.Member, e.Base+"/ui/"+ns.Slug)}}
	if resp := sameOriginPostForm(t, e.Member, e.Base+"/ui/handover", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("hand over = %d", resp.StatusCode)
	}
	form.Set("csrf_token", renderedCSRFToken(t, e.Owner, e.Base+"/ui/alpha"))
	if resp := sameOriginPostForm(t, e.Owner, e.Base+"/ui/orgs/takeover", form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("take over = %d", resp.StatusCode)
	}
	if p, _ := e.St.ProjectByRemote("github.com/alpha-gh/x"); p.Org != "alpha" {
		t.Fatalf("project = %+v", p)
	}
}

// Third review, point 1: the instance admin sets a project's owner in the web
// (a known project is reassigned, an unknown remote decided); nobody else sees
// or uses the form.
func TestAdminAssignsOwnershipInTheWeb(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.DB().Exec(`UPDATE persons SET is_admin=CASE WHEN id=1 THEN 1 ELSE 0 END`); err != nil {
		t.Fatal(err)
	}
	if _, err := claimOrPlace(e.St, "person:2", "github.com/x/squatted", ""); err != nil {
		t.Fatal(err)
	}
	if _, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha"); !strings.Contains(page, `action="/ui/orgs/assign"`) {
		t.Fatal("the admin has no ownership form")
	}
	if _, page := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha"); strings.Contains(page, `action="/ui/orgs/assign"`) {
		t.Fatal("a member sees the ownership form")
	}
	if resp := postOrg(t, e.Member, e.Base, "/ui/orgs/assign", url.Values{"remote": {"github.com/x/squatted"}, "to": {"alpha"}}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a member assigned: %d", resp.StatusCode)
	}
	if p, _ := e.St.ProjectByRemote("github.com/x/squatted"); p.Org == "alpha" {
		t.Fatal("the member's request moved the project")
	}
	postOrg(t, e.Owner, e.Base, "/ui/orgs/assign", url.Values{"remote": {"github.com/x/squatted"}, "to": {"alpha"}})
	if p, _ := e.St.ProjectByRemote("github.com/x/squatted"); p.Org != "alpha" {
		t.Fatalf("reassign: %+v", p)
	}
	postOrg(t, e.Owner, e.Base, "/ui/orgs/assign", url.Values{"remote": {"gitlab.com/x/fresh"}, "to": {"alpha"}})
	if p, ok := e.St.ProjectByRemote("gitlab.com/x/fresh"); !ok || p.Org != "alpha" {
		t.Fatalf("decide: %+v %v", p, ok)
	}
}

// Decision E10 in the browser: the owner of a namespace that yielded its
// address to a verified GitHub owner reads it once on the organizations page.
func TestYieldedAddressIsShownOnce(t *testing.T) {
	e := ovEnv(t)
	if _, err := e.St.CreateOrg("person:1", "Mine", "philipp-gh"); err != nil {
		t.Fatal(err)
	}
	if err := e.St.SetAccountGitHub("person:2", "philipp-gh", 4711); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	if !strings.Contains(page, "philipp-gh-2") {
		t.Fatalf("no notice:\n%s", page)
	}
	if _, page = fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha"); strings.Contains(page, "verified GitHub owner") {
		t.Fatal("the notice shows twice")
	}
}
