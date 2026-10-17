package store

import (
	"errors"
	"testing"
	"time"
)

const (
	sharedProject  = roleProject
	otherProject   = "github.com/dw/q"
	prefixLogin    = "deadweight-labs"
	prefixRemote   = "github.com/deadweight-labs/ghosttree"
	prefixOwnerOrg = "github.com/deadweight-labs"
)

func mustShare(t *testing.T, st *Store, actor, remote string, shared bool) {
	t.Helper()
	if err := st.SetProjectVisibility(actor, remote, shared); err != nil {
		t.Fatalf("share %s: %v", remote, err)
	}
}

func roomPrincipal(id string) Principal {
	return Principal{ID: id, Label: id, TokenKind: WebSessionKind}
}

func TestSharedProjectGivesOrgMembersTheMemberRole(t *testing.T) {
	st := roleFixture(t)
	if got := st.ProjectRole(sharedProject, "person:2"); got.Role != "" {
		t.Fatalf("private project: member without role has %+v", got)
	}
	mustShare(t, st, "person:1", sharedProject, true)
	for _, who := range []string{"person:2", "person:3", "person:4"} {
		got := st.ProjectRole(sharedProject, who)
		if got.Role != RoleMember || !got.Inherited {
			t.Fatalf("%s: shared project must give the default role, got %+v", who, got)
		}
	}
	if got := st.ProjectRole(sharedProject, "person:5"); got.Role != "" {
		t.Fatalf("outside the org nothing is shared: %+v", got)
	}
	// Die Sammelabfrage und die Schreiberliste folgen derselben Regel.
	roles, _ := st.AccountRoles("person:2")
	if roles[sharedProject].Role != RoleMember {
		t.Fatalf("AccountRoles: %+v", roles[sharedProject])
	}
	if !st.ProjectWriters(sharedProject)["anna"] {
		t.Fatalf("writers: %v", st.ProjectWriters(sharedProject))
	}
	// Der Raum nimmt Nachrichten an: 403 gehört der Vergangenheit an.
	st.SetAccessMode(AccessMode{Enforce: true})
	if err := st.Access(roomPrincipal("person:2")).Check(sharedProject, ResRoom, ActPost, Object{}); err != nil {
		t.Fatalf("post into the room of a shared project: %v", err)
	}
	mustShare(t, st, "person:1", sharedProject, false)
	if err := st.Access(roomPrincipal("person:2")).Check(sharedProject, ResRoom, ActPost, Object{}); err == nil {
		t.Fatal("private again: the member has no access")
	}
}

func TestExplicitRoleOverridesTheSharedDefault(t *testing.T) {
	st := roleFixture(t)
	mustShare(t, st, "person:1", sharedProject, true)
	if err := setRole(st, "person:1", "person:2", RoleGuest, false); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(sharedProject, "person:2"); got.Role != RoleGuest || got.Inherited {
		t.Fatalf("explicit guest must win over the default: %+v", got)
	}
	if err := setRole(st, "person:1", "person:3", RoleLead, false); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(sharedProject, "person:3"); got.Role != RoleLead {
		t.Fatalf("explicit lead: %+v", got)
	}
	members, err := st.ListProjectMembers(sharedProject)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]ProjectMember{}
	for _, m := range members {
		seen[m.Account] = m
	}
	if seen["cleo"].Role != RoleMember || !seen["cleo"].Inherited || seen["anna"].Role != RoleGuest {
		t.Fatalf("member list of a shared project: %+v", members)
	}
}

func TestOnlyProjectOwnersChangeVisibility(t *testing.T) {
	st := roleFixture(t)
	if err := st.SetProjectVisibility("person:2", sharedProject, true); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("a member without a role does not even see the project: %v", err)
	}
	if err := setRole(st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectVisibility("person:2", sharedProject, true); !errors.Is(err, ErrNotProjectOwner) {
		t.Fatalf("member: %v", err)
	}
	if err := st.SetProjectVisibility("person:5", sharedProject, true); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("outsider must not learn the project exists: %v", err)
	}
	if err := st.SetProjectVisibility("person:1", "github.com/dw/nope", true); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	if st.ProjectShared(sharedProject) {
		t.Fatal("refused changes must leave the project private")
	}
	// Ein ausdrücklicher Projekt-Owner darf es auch.
	if err := setRole(st, "person:1", "person:2", RoleOwner, false); err != nil {
		t.Fatal(err)
	}
	mustShare(t, st, "person:2", sharedProject, true)
	if !st.ProjectShared(sharedProject) {
		t.Fatal("project owner shares")
	}
}

func TestOrgInvitationNeedsNoProjectAndSeesSharedProjects(t *testing.T) {
	st := orgStore(t, "robin", "philipp")
	o := mustOrg(t, st, "person:1", "Deadweight", "dw")
	if _, err := st.EnsureProject("person:1", sharedProject); err != nil {
		t.Fatal(err)
	}
	mustShare(t, st, "person:1", sharedProject, true)
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	code, inv, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil || inv.ProjectRemote != "" {
		t.Fatalf("org invitation: %+v %v", inv, err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	st.SetAccessMode(AccessMode{Enforce: true})
	a := st.Access(roomPrincipal("person:2"))
	if err := a.Check(sharedProject, ResRoom, ActPost, Object{}); err != nil {
		t.Fatalf("invited person writes in the shared room: %v", err)
	}
	if err := a.Check(otherProject, ResRoom, ActRead, Object{}); err == nil {
		t.Fatal("a private project stays invisible")
	}
	visible := a.VisibleProjects([]Project{{Remote: sharedProject}, {Remote: otherProject}})
	if len(visible) != 1 || visible[0].Remote != sharedProject {
		t.Fatalf("visible projects: %+v", visible)
	}
}

func TestGuestsStayOnTheirProjects(t *testing.T) {
	st := roleFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	mustShare(t, st, "person:1", sharedProject, true)
	mustShare(t, st, "person:1", otherProject, true)
	o, _ := st.OrgByRef("alpha")
	// Gast: eine Projekt-Einladung in das erste Projekt.
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, sharedProject, RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(sharedProject, "person:5"); got.Role != RoleGuest {
		t.Fatalf("invited project: %+v", got)
	}
	if got := st.ProjectRole(otherProject, "person:5"); got.Role != "" {
		t.Fatalf("a guest must not inherit the default of other shared projects: %+v", got)
	}
	roles, _ := st.AccountRoles("person:5")
	if _, ok := roles[otherProject]; ok && roles[otherProject].Role != "" {
		t.Fatalf("AccountRoles leaks the shared default to a guest: %+v", roles[otherProject])
	}
	if st.ProjectWriters(otherProject)["dev"] {
		t.Fatal("guest counted as writer of a shared project")
	}
	members, _ := st.ListOrgMembers(o.ID)
	guest := false
	for _, m := range members {
		if m.Account == "dev" {
			guest = m.Guest
		}
	}
	if !guest {
		t.Fatalf("the member list must name the guest: %+v", members)
	}
	// Wer als Gast kam und in die Org eingeladen wird, ist danach Mitglied.
	code, _, err = st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatalf("a guest accepts an org invitation: %v", err)
	}
	if got := st.ProjectRole(otherProject, "person:5"); got.Role != RoleMember {
		t.Fatalf("upgraded guest sees shared projects: %+v", got)
	}
	if got := st.ProjectRole(sharedProject, "person:5"); got.Role != RoleGuest {
		t.Fatalf("the explicit guest role stays until someone changes it: %+v", got)
	}
}

// ---------------------------------------------------------------- Präfix und Quellen

func prefixFixture(t *testing.T) (*Store, Org) {
	t.Helper()
	st := orgStore(t, "robin", "anna", "ben", "mallory")
	o := mustOrg(t, st, "person:1", "Deadweight", "dw")
	code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	return st, o
}

func TestPrefixClaimNeedsProof(t *testing.T) {
	st, o := prefixFixture(t)
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); !errors.Is(err, ErrPrefixUnproven) {
		t.Fatalf("without a connected source: %v", err)
	}
	if _, err := st.ConnectSource("person:2", o.ID, "github", prefixLogin, nil); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("only an org owner connects: %v", err)
	}
	if _, err := st.ConnectSource("person:1", o.ID, "github", "not a login!", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("login shape: %v", err)
	}
	if _, err := st.ConnectSource("person:1", o.ID, "github", prefixLogin, nil); err != nil {
		t.Fatal(err)
	}
	// Der Nachweis gilt nur für das eigene Präfix.
	if _, err := st.ClaimPrefix("person:1", o.ID, "github.com/someone-else/*", "shared"); !errors.Is(err, ErrPrefixUnproven) {
		t.Fatalf("foreign prefix: %v", err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, "gitlab.com/"+prefixLogin+"/*", "shared"); !errors.Is(err, ErrPrefixUnproven) {
		t.Fatalf("other host: %v", err)
	}
	if _, err := st.ClaimPrefix("person:2", o.ID, prefixOwnerOrg+"/*", "shared"); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member: %v", err)
	}
	p, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared")
	if err != nil || p.Prefix != prefixOwnerOrg || p.Visibility != "shared" {
		t.Fatalf("claim: %+v %v", p, err)
	}
	// Doppeltes Beanspruchen ändert nur die Sichtbarkeit.
	if p, err = st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg, "private"); err != nil || p.Visibility != "private" {
		t.Fatalf("again: %+v %v", p, err)
	}
	list, _ := st.ListOrgPrefixes("person:2", o.ID)
	if len(list) != 1 {
		t.Fatalf("prefixes: %+v", list)
	}
}

func TestTwoOrgsCannotHoldTheSamePrefix(t *testing.T) {
	st, o := prefixFixture(t)
	other := mustOrg(t, st, "person:3", "Other", "other")
	if _, err := st.ConnectSource("person:1", o.ID, "github", prefixLogin, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSource("person:3", other.ID, "github", prefixLogin, nil); !errors.Is(err, ErrSourceTaken) {
		t.Fatalf("same GitHub org twice: %v", err)
	}
	if _, err := st.ClaimPrefix("person:3", other.ID, prefixOwnerOrg+"/*", "shared"); !errors.Is(err, ErrPrefixUnproven) {
		t.Fatalf("claim without own proof: %v", err)
	}
	// Das Aufheben gibt den Namensraum frei; danach darf die andere Org ihn
	// belegen.
	if err := st.ReleasePrefix("person:1", o.ID, prefixOwnerOrg); err != nil {
		t.Fatal(err)
	}
	if err := st.DisconnectSource("person:1", o.ID, "github", prefixLogin); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSource("person:3", other.ID, "github", prefixLogin, nil); err != nil {
		t.Fatalf("freed: %v", err)
	}
}

func TestClonedRepoJoinsTheClaimingOrgAtFirstContact(t *testing.T) {
	st, o := prefixFixture(t)
	if _, err := st.ConnectSource("person:1", o.ID, "github", prefixLogin, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); err != nil {
		t.Fatal(err)
	}
	// Anna ist einfaches Mitglied: kein Claim, trotzdem landet das Projekt in der Org.
	p, err := st.EnsureProject("person:2", prefixRemote)
	if err != nil || p.OrgID != o.ID {
		t.Fatalf("first contact: %+v %v", p, err)
	}
	if !st.ProjectShared(prefixRemote) {
		t.Fatal("the prefix rule carries its visibility")
	}
	if got := st.ProjectRole(prefixRemote, "person:2"); got.Role != RoleMember {
		t.Fatalf("anna works in it: %+v", got)
	}
	// Ein Fremder kann es weder zuordnen noch abgreifen.
	if _, err := st.EnsureProject("person:4", "github.com/deadweight-labs/secret"); !errors.Is(err, ErrProjectClaimed) {
		t.Fatalf("outsider at the prefix: %v", err)
	}
	if _, known := st.ProjectByRemote("github.com/deadweight-labs/secret"); known {
		t.Fatal("an outsider must not create projects under a claimed prefix")
	}
	// Auch ein eigener Claim in einer anderen Org geht nicht.
	other := mustOrg(t, st, "person:4", "Mallory", "mal")
	if _, err := st.ClaimProject("person:4", "github.com/deadweight-labs/other", other.Slug); !errors.Is(err, ErrProjectClaimed) {
		t.Fatalf("claim under foreign prefix: %v", err)
	}
	// Eine fremde Remote bleibt wie bisher.
	p, err = st.EnsureProject("person:2", "github.com/deadweight-labs-fake/x")
	if err != nil || p.Shared {
		t.Fatalf("a similar prefix must not match: %+v %v", p, err)
	}
	if st.ProjectShared("github.com/deadweight-labs-fake/x") {
		t.Fatal("a similar prefix must not match")
	}
}

func TestImportSourceReposAsProjects(t *testing.T) {
	st, o := prefixFixture(t)
	repos := []string{"github.com/deadweight-labs/a", "github.com/deadweight-labs/b", "bogus remote"}
	src, err := st.ConnectSource("person:1", o.ID, "github", prefixLogin, repos)
	if err != nil || src.Repos != 2 {
		t.Fatalf("connect: %+v %v", src, err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, prefixOwnerOrg+"/*", "shared"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ImportSourceRepos("person:2", o.ID, src.ID); !errors.Is(err, ErrNotOrgOwner) || n != 0 {
		t.Fatalf("member imports: %d %v", n, err)
	}
	n, err := st.ImportSourceRepos("person:1", o.ID, src.ID)
	if err != nil || n != 2 {
		t.Fatalf("import: %d %v", n, err)
	}
	if !st.ProjectShared("github.com/deadweight-labs/a") {
		t.Fatal("imported projects take the visibility of the rule")
	}
	if n, _ := st.ImportSourceRepos("person:1", o.ID, src.ID); n != 0 {
		t.Fatalf("second import adds nothing, got %d", n)
	}
	list, _ := st.ListSourceRepos("person:1", o.ID, src.ID)
	if len(list) != 2 || !list[0].Imported {
		t.Fatalf("repo list: %+v", list)
	}
}

func TestMovingAProjectMakesItPrivate(t *testing.T) {
	st := roleFixture(t)
	other := mustOrg(t, st, "person:1", "Beta", "beta")
	mustShare(t, st, "person:1", sharedProject, true)
	if _, err := st.MoveProject("person:1", sharedProject, other.Slug); err != nil {
		t.Fatal(err)
	}
	if st.ProjectShared(sharedProject) {
		t.Fatal("a moved project starts private in its new org")
	}
}

func TestLeavingOrgDropsTheGuestMarker(t *testing.T) {
	st := roleFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	o, _ := st.OrgByRef("alpha")
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, sharedProject, RoleGuest, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveOrgMember("person:1", o.ID, "person:5"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM org_guests WHERE account_id=5`).Scan(&n)
	if n != 0 {
		t.Fatal("guest marker must go with the membership")
	}
}
