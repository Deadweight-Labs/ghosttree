package store

import (
	"errors"
	"testing"
)

// A moved slug keeps its old ids reserved, and a personal namespace never
// reserves hostless remotes of other people (security review of REQ-623).
func TestSlugMoveAndNamespacesDoNotOpenOrBlockIDs(t *testing.T) {
	st := orgStore(t, "robin", "gitserver", "anna")
	o := mustOrg(t, st, "person:1", "Default", "default")
	if _, err := st.CreateNamedProject("person:1", "infra", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameOrg("person:1", o.ID, "Deadweight Labs", "deadweight-labs"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ProjectByRemote("deadweight-labs/infra"); !ok {
		t.Fatal("the named project did not move with the slug")
	}
	if cur, ok := st.OrgSlugTarget("default"); !ok || cur != "deadweight-labs" {
		t.Fatalf("redirect = %q %v", cur, ok)
	}
	if got, err := st.OrgByRef("default"); err != nil || got.ID != o.ID {
		t.Fatalf("old slug resolves to %+v %v", got, err)
	}
	// Nobody occupies the old ids, and nobody founds an org under the old slug.
	if _, err := st.EnsureProject("person:3", "default/infra"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("old named id taken over: %v", err)
	}
	if _, err := st.CreateOrg("person:1", "Other", "default"); !errors.Is(err, ErrOrgSlugTaken) {
		t.Fatalf("old slug reused: %v", err)
	}
	// "gitserver" names a person's namespace: ids under it are that person's
	// alone (finding 8 of the security review), others neither write nor claim.
	if _, err := st.EnsureUserNamespace("person:2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:3", "git@gitserver:repo.git"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("a stranger wrote under a person's namespace: %v", err)
	}
	if _, err := st.ClaimProject("person:3", "git@gitserver:repo.git", ""); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("a stranger claimed under a person's namespace: %v", err)
	}
	if p, err := st.ClaimProject("person:2", "git@gitserver:repo.git", ""); err != nil || p.Org != "gitserver" {
		t.Fatalf("the person claims under the own namespace: %+v %v", p, err)
	}
}

// The rights matrix of REQ-623 in one organization with a shared project:
// owner, member, guest, service and outsider, plus a personal project.
func TestRightsOfPeopleServicesGuestsAndOutsiders(t *testing.T) {
	st := orgStore(t, "robin", "anna", "gus", "bot", "olga")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	for _, who := range []string{"person:2", "person:4"} {
		code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	const shared, other = "github.com/alpha/shared", "github.com/alpha/other"
	for _, p := range []string{shared, other} {
		if _, err := st.ClaimProject("person:1", p, "alpha"); err != nil {
			t.Fatal(err)
		}
		mustShare(t, st, "person:1", p, true)
	}
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, shared, RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountKindAsOperator(4, AccountKindService, o.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"person:1": {shared: RoleOwner, other: RoleOwner},
		"person:2": {shared: RoleMember, other: RoleMember},
		"person:3": {shared: RoleGuest, other: ""},
		"person:4": {shared: "", other: ""}, // a service inherits nothing
		"person:5": {shared: "", other: ""},
	}
	check := func(stage string) {
		t.Helper()
		for who, projects := range want {
			roles, _ := st.AccountRoles(who)
			for p, role := range projects {
				if got := st.ProjectRole(p, who).Role; got != role {
					t.Errorf("%s: %s in %s = %q, want %q", stage, who, p, got, role)
				}
				if got := roles[p].Role; got != role {
					t.Errorf("%s: AccountRoles %s in %s = %q, want %q", stage, who, p, got, role)
				}
			}
		}
		if st.ProjectWriters(shared)["bot"] != (want["person:4"][shared] != "") {
			t.Errorf("%s: writers of shared and the service disagree", stage)
		}
	}
	check("before grants")
	// An explicit role is the only way for a service.
	if err := st.SetProjectRole("person:1", shared, "person:4", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	want["person:4"][shared] = RoleMember
	check("after grant")
	// Services list apart from people, with their explicit roles; guests and
	// outsiders do not see the list.
	services, err := st.OrgServices("person:2", o.ID)
	if err != nil || len(services) != 1 || services[0].Name != "bot" || !services[0].OfOrg || services[0].Roles[shared] != RoleMember || len(services[0].Roles) != 1 {
		t.Fatalf("services = %+v %v", services, err)
	}
	// A restricted project stays unnamed for a member without a role in it.
	if err := st.SetProjectVisibility("person:1", shared, false); err != nil {
		t.Fatal(err)
	}
	if services, _ := st.OrgServices("person:2", o.ID); len(services) != 1 || len(services[0].Roles) != 0 {
		t.Fatalf("restricted project leaks through a service: %+v", services)
	}
	if services, _ := st.OrgServices("person:1", o.ID); len(services) != 1 || services[0].Roles[shared] != RoleMember {
		t.Fatalf("owner view: %+v", services)
	}
	mustShare(t, st, "person:1", shared, true)
	for _, who := range []string{"person:3", "person:5"} {
		if _, err := st.OrgServices(who, o.ID); !errors.Is(err, ErrNotOrgMember) {
			t.Errorf("%s reads the services: %v", who, err)
		}
	}
	members, _ := st.ListOrgMembers(o.ID)
	for _, m := range members {
		if (m.Account == "bot") != (m.Kind == AccountKindService) {
			t.Errorf("member %s kind %q", m.Account, m.Kind)
		}
	}
	// A personal project: only its owner, nobody of the org.
	if err := st.SetAccountGitHub("person:2", "anna", 77); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:2", "github.com/anna/notes", ""); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": RoleOwner, "person:1": "", "person:3": "", "person:4": "", "person:5": ""} {
		if got := st.ProjectRole("github.com/anna/notes", who).Role; got != role {
			t.Errorf("personal project: %s = %q, want %q", who, got, role)
		}
	}
	// Owners cannot share a personal project with "the org", there is none.
	if err := st.SetProjectVisibility("person:2", "github.com/anna/notes", true); !errors.Is(err, ErrUserNamespace) {
		t.Errorf("sharing a personal project: %v", err)
	}
	ns, _ := st.UserNamespace("person:2")
	if _, _, err := st.CreateInvitation("person:2", ns.ID, "", OrgMember, 0); !errors.Is(err, ErrUserNamespace) {
		t.Errorf("inviting into a personal namespace: %v", err)
	}
}

// The org mirrors the connected GitHub organization: the picture always; name
// and address with a redirect from the former slug and the named projects
// moving along only after an owner opted in, until somebody renames it by hand
// (REQ-623, security review 4).
func TestOrgMirrorsTheConnectedGitHubOrganization(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:1", "Default", "default")
	if _, err := st.CreateNamedProject("person:1", "infra", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "Deadweight-Labs", "Deadweight Labs"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("mirror without a source: %v", err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, SourceGitHub, "deadweight-labs", 179, []string{"github.com/deadweight-labs/ghosttree"}); err != nil {
		t.Fatal(err)
	}
	// Without the opt-in only the picture follows.
	if got, err := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "Deadweight-Labs", "Deadweight Labs"); err != nil ||
		got.Slug != "default" || got.Name != "Default" || got.AvatarLogin != "Deadweight-Labs" {
		t.Fatalf("mirrored without opt-in = %+v %v", got, err)
	}
	if err := st.SetOrgFollowGitHub("person:2", o.ID, true); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a stranger opted in: %v", err)
	}
	if err := st.SetOrgFollowGitHub("person:1", o.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "Deadweight-Labs", "Deadweight Labs")
	if err != nil || got.Slug != "deadweight-labs" || got.Name != "Deadweight Labs" || got.AvatarLogin != "Deadweight-Labs" || got.DisplaySource != "github" {
		t.Fatalf("mirrored = %+v %v", got, err)
	}
	if cur, ok := st.OrgSlugTarget("default"); !ok || cur != "deadweight-labs" {
		t.Fatalf("no redirect from the old slug: %q %v", cur, ok)
	}
	if _, ok := st.ProjectByRemote("deadweight-labs/infra"); !ok {
		t.Fatal("named project did not move")
	}
	if login, ok := st.OrgAvatarLogin(o.ID); !ok || login != "Deadweight-Labs" {
		t.Fatalf("avatar login = %q %v", login, ok)
	}
	if id, login, ok := st.SourceByInstallation(179); !ok || id != o.ID || login != "deadweight-labs" {
		t.Fatalf("source by installation = %d %q %v", id, login, ok)
	}
	// A rename by hand stops the name from following GitHub; the picture still does.
	if _, err := st.RenameOrg("person:1", o.ID, "DWL", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "Deadweight-Labs", "Something Else"); got.Name != "DWL" || got.DisplaySource != "user" {
		t.Fatalf("manual name overwritten: %+v", got)
	}
	// A renamed GitHub login is only recorded: remotes still carry the old one.
	if got, _ := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "Dead-Labs", "x"); got.Slug != "deadweight-labs" || got.AvatarLogin != "Deadweight-Labs" {
		t.Fatalf("renamed login changed the org: %+v", got)
	}
	// Control characters and foreign logins are refused.
	if _, err := st.MirrorGitHubOrg(o.ID, "deadweight-labs", "../x", "x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad login: %v", err)
	}
}

// Decisions 14 A and 17 A of #4376: owners and members create projects in
// their organization, guests do not and are guests to the organization's
// pages; a member with only guest roles counts as a guest.
func TestGuestsMembersAndOwnersOfAnOrganization(t *testing.T) {
	st := orgStore(t, "robin", "anna", "gus", "gina", "olga")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	join := func(who string) {
		code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	join("person:2")
	join("person:4")
	if _, err := st.ClaimProject("person:1", "github.com/alpha/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	// gus: a guest invited to one project.
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, "github.com/alpha/one", RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	// gina: a member with nothing but a guest role.
	if err := st.SetProjectRole("person:1", "github.com/alpha/one", "person:4", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	for who, want := range map[string]string{"person:1": OrgViewerOwner, "person:2": OrgViewerMember, "person:3": OrgViewerGuest, "person:4": OrgViewerGuest, "person:5": ""} {
		if got := st.OrgViewerKind(o.ID, who); got != want {
			t.Errorf("%s is %q, want %q", who, got, want)
		}
	}
	for who, ok := range map[string]bool{"person:2": true, "person:3": false, "person:4": false, "person:5": false} {
		// Another host than GitHub: the GitHub rule (only the login's owner)
		// is tested on its own.
		remote := "gitlab.com/alpha/by-" + who[len("person:"):]
		_, err := st.ClaimProject(who, remote, "alpha")
		if (err == nil) != ok {
			t.Errorf("%s claims: %v", who, err)
		}
		_, err = st.CreateNamedProject(who, "named-"+who[len("person:"):], "alpha")
		if (err == nil) != ok {
			t.Errorf("%s creates a named project: %v", who, err)
		}
	}
	orgs, _ := st.ListOrgs("person:3")
	if len(orgs) != 1 || !orgs[0].Guest {
		t.Fatalf("guest's orgs = %+v", orgs)
	}
	if orgs, _ := st.ListOrgs("person:2"); orgs[0].Guest {
		t.Fatal("a member is listed as guest")
	}
	if _, err := st.OrgServices("person:4", o.ID); !errors.Is(err, ErrNotOrgMember) {
		t.Errorf("a guest-role member reads the services: %v", err)
	}
}

// Security review, finding 1: owning the own namespace does not lift the guest
// status; a guest keeps a locked name.
func TestOwnNamespaceDoesNotMakeAGuestAnOwner(t *testing.T) {
	st := orgStore(t, "robin", "gus")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, err := st.ClaimProject("person:1", "github.com/alpha/one", "alpha"); err != nil {
		t.Fatal(err)
	}
	// A namespace from before (for example from a migration), then a guest role.
	if _, err := st.EnsureUserNamespace("person:2"); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, "github.com/alpha/one", RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOwnName("person:2", "Gustav"); !errors.Is(err, ErrNameNotAllowed) {
		t.Fatalf("a guest with an own namespace renamed itself: %v", err)
	}
}

// Upgrade (security review 4): organizations that exist when the owner model
// arrives keep their name and address; the backfill runs once, and later
// organizations are left for their owners to decide.
func TestUpgradeKeepsNamesOfExistingOrganizations(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Default", "default")
	if _, err := st.db.Exec(`UPDATE orgs SET display_source='' WHERE id=?`, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM org_state WHERE key=?`, displaySourceBackfillKey); err != nil {
		t.Fatal(err)
	}
	if err := backfillDisplaySource(st.db); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OrgByRef("default"); got.DisplaySource != DisplayUser {
		t.Fatalf("existing org display_source = %q", got.DisplaySource)
	}
	later := mustOrg(t, st, "person:1", "Later", "later")
	if err := backfillDisplaySource(st.db); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OrgByRef("later"); got.DisplaySource != "" {
		t.Fatalf("the backfill ran twice: %q", got.DisplaySource)
	}
	_ = later
	if ns, _ := st.EnsureUserNamespace("person:1"); ns.DisplaySource != "" {
		t.Fatalf("a personal namespace got a display source: %q", ns.DisplaySource)
	}
}

// Old ids of named projects follow the slug (security review 4).
func TestMovedProjectFollowsTheSlugHistory(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Default", "default")
	if _, err := st.CreateNamedProject("person:1", "infra", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameOrg("person:1", o.ID, "Deadweight Labs", "deadweight-labs"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameOrg("person:1", o.ID, "DWL", "dwl"); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"default/infra", "deadweight-labs/infra"} {
		if cur, ok := st.MovedProject(old); !ok || cur != "dwl/infra" {
			t.Errorf("%s -> %q %v", old, cur, ok)
		}
	}
	for _, id := range []string{"dwl/infra", "default/other", "github.com/x/y", "nobody/infra", "infra"} {
		if cur, ok := st.MovedProject(id); ok {
			t.Errorf("%s resolved to %q", id, cur)
		}
	}
}

// Security review 17: a person founds an organization (in the web), a service
// never does.
func TestServicesFoundNoOrganization(t *testing.T) {
	st := orgStore(t, "robin", "bot")
	if err := st.SetAccountKindAsOperator(2, AccountKindService, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:2", "Bots", "bots"); !errors.Is(err, ErrServiceAccount) {
		t.Fatalf("a service founded an organization: %v", err)
	}
	if _, err := st.CreateOrg("person:1", "People", "people"); err != nil {
		t.Fatalf("a person: %v", err)
	}
}
