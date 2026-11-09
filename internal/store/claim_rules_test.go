package store

import (
	"errors"
	"testing"
)

// Security review, finding 2d: when an organization connects a prefix later,
// projects in people's namespaces under it wait for a takeover: an owner of
// the organization with the former owner's agreement, or the instance admin.
func TestProjectsUnderALaterPrefixWaitForATakeover(t *testing.T) {
	st := orgStore(t, "robin", "olga", "carl")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgRole("person:1", o.ID, "person:2", OrgOwner); err != nil {
		t.Fatal(err)
	}
	// Held in carl's namespace from before the organization connected acme.
	for _, r := range []string{"github.com/acme/tool", "github.com/acme/other"} {
		if _, err := claimOrPlace(st, "person:3", r, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, SourceGitHub, "acme", 3, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, "github.com/acme/*", VisibilityShared); err != nil {
		t.Fatal(err)
	}
	list, err := st.PendingTakeovers("person:2", o.ID)
	if err != nil || len(list) != 2 || list[0].Owner != "carl" || list[0].Consented {
		t.Fatalf("waiting = %+v %v", list, err)
	}
	if _, err := st.PendingTakeovers("person:3", o.ID); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a non-owner lists the waiting projects: %v", err)
	}
	// The org owner may not take it without the owner's agreement.
	if _, err := st.TakeOverProject("person:2", "github.com/acme/tool"); !errors.Is(err, ErrNoHandover) {
		t.Fatalf("taken without agreement: %v", err)
	}
	if err := st.ConsentHandover("person:2", "github.com/acme/tool"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("somebody else agreed for carl: %v", err)
	}
	if err := st.ConsentHandover("person:3", "github.com/acme/tool"); err != nil {
		t.Fatal(err)
	}
	p, err := st.TakeOverProject("person:2", "github.com/acme/tool")
	if err != nil || p.Org != "alpha" || !p.Shared {
		t.Fatalf("takeover = %+v %v", p, err)
	}
	if role := st.ProjectRole("github.com/acme/tool", "person:3"); role.Role != RoleOwner {
		t.Fatalf("the former owner = %+v", role)
	}
	// The instance admin decides without an agreement.
	if p, err := st.TakeOverProject("person:1", "github.com/acme/other"); err != nil || p.Org != "alpha" {
		t.Fatalf("admin takeover = %+v %v", p, err)
	}
	if list, _ := st.PendingTakeovers("person:2", o.ID); len(list) != 0 {
		t.Fatalf("still waiting: %+v", list)
	}
}

// Review point 1: a github.com repository is claimed explicitly by the
// verified owner of its login, a member of the organization connected to its
// prefix, or the instance admin; a project somebody else took anyway goes to
// its owner by the admin's reassignment, logged on both sides.
func TestGitHubReposGoToTheirOwnerAndTheAdminReassigns(t *testing.T) {
	st := orgStore(t, "robin", "alice", "carl")
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.ClaimProject("person:3", "github.com/alice/repo", ""); !errors.Is(err, ErrNotGitHubOwner) {
		t.Fatalf("carl claimed alice's repository: %v", err)
	}
	// Carl's from before the rule: it sits in his namespace.
	if _, err := claimOrPlace(st, "person:3", "github.com/alice/repo", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountGitHub("person:2", "alice", 501); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:2", "github.com/alice/repo", ""); !errors.Is(err, ErrProjectClaimed) {
		t.Fatalf("alice's claim of the taken project: %v", err)
	}
	if p, err := st.ClaimProject("person:2", "github.com/alice/other", ""); err != nil || p.Org != "alice" {
		t.Fatalf("alice claims her own: %+v %v", p, err)
	}
	ns, err := st.EnsureUserNamespace("person:2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReassignProject("person:2", "github.com/alice/repo", ns.Slug); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("a non-admin reassigned: %v", err)
	}
	p, err := st.ReassignProject("person:1", "https://github.com/Alice/repo.git", ns.Slug)
	if err != nil || p.Org != ns.Slug {
		t.Fatalf("reassign: %+v %v", p, err)
	}
	if role := st.ProjectRole("github.com/alice/repo", "person:3").Role; role != "" {
		t.Fatalf("carl kept role %q", role)
	}
	if role := st.ProjectRole("github.com/alice/repo", "person:2").Role; role != RoleOwner {
		t.Fatalf("alice = %q", role)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM org_events WHERE action='reassign_project' AND subject='github.com/alice/repo'`).Scan(&n)
	if n != 2 {
		t.Fatalf("logged %d times, want 2", n)
	}
	// The admin may claim a GitHub repository for anybody.
	if p, err := st.ClaimProject("person:1", "github.com/nobody-here/tool", ""); err != nil || p.Org == "" {
		t.Fatalf("admin claim: %+v %v", p, err)
	}
}

// H1 (review of REQ-623): somebody invited into a project as member (a guest
// of the organization with the project role member) gets an own namespace and
// creates projects there under the same rules; somebody who is only a guest
// gets none.
func TestInvitedMembersGetANamespaceGuestsDoNot(t *testing.T) {
	st := orgStore(t, "robin", "ivy", "gus")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, err := st.ClaimProject("person:1", "gitlab.com/alpha/app", "alpha"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []int64{2, 3} {
		if _, err := st.db.Exec(`INSERT INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'member',?)`, o.ID, who, now()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO org_guests(org_id, account_id) VALUES(?,?)`, o.ID, who); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetProjectRole("person:1", "gitlab.com/alpha/app", "person:2", RoleMember, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", "gitlab.com/alpha/app", "person:3", RoleGuest, false, RoleViaAPI); err != nil {
		t.Fatal(err)
	}
	// ivy, invited as member
	if _, err := st.EnsureUserNamespace("person:2"); err != nil {
		t.Fatalf("an invited member gets no namespace: %v", err)
	}
	if p, err := st.ClaimProject("person:2", "gitlab.com/ivy/side", ""); err != nil || p.Org != "ivy" {
		t.Fatalf("ivy's own project: %+v %v", p, err)
	}
	if _, err := st.ClaimProject("person:2", "github.com/someone/thing", ""); !errors.Is(err, ErrNotGitHubOwner) {
		t.Fatalf("ivy claims somebody's GitHub repository: %v", err)
	}
	// gus, only a guest
	if _, err := st.EnsureUserNamespace("person:3"); !errors.Is(err, ErrGuestOnly) {
		t.Fatalf("a guest got a namespace: %v", err)
	}
	if _, err := st.ClaimProject("person:3", "gitlab.com/gus/side", ""); !errors.Is(err, ErrGuestOnly) {
		t.Fatalf("a guest claimed: %v", err)
	}
}
