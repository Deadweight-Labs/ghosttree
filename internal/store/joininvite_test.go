package store

import (
	"errors"
	"testing"
	"time"
)

const joinRemote = "github.com/alpha/app"

func joinFixture(t *testing.T) (*Store, Org) {
	t.Helper()
	st := orgStore(t, "robin", "anna", "ben")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, err := st.EnsureProject("person:1", joinRemote); err != nil {
		t.Fatal(err)
	}
	return st, o
}

func TestProjectInvitationOnlyGrantsMemberOrGuest(t *testing.T) {
	st, o := joinFixture(t)
	for _, role := range []string{"", "owner", "lead", "boss"} {
		if _, _, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, role, 0); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("role %q accepted: %v", role, err)
		}
	}
	if _, _, err := st.CreateProjectInvitation("person:2", o.ID, joinRemote, RoleMember, 0); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("non-owner: %v", err)
	}
	if _, _, err := st.CreateProjectInvitation("person:1", o.ID, "github.com/none/none", RoleMember, 0); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	other := mustOrg(t, st, "person:2", "Beta", "beta")
	if _, _, err := st.CreateProjectInvitation("person:2", other.ID, joinRemote, RoleMember, 0); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("project of another org: %v", err)
	}
}

func TestProjectInvitationDefaultTTL(t *testing.T) {
	st, o := joinFixture(t)
	for role, want := range map[string]time.Duration{RoleMember: DefaultInvitationTTL, RoleGuest: DefaultGuestInvitationTTL} {
		_, inv, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, role, 0)
		if err != nil {
			t.Fatal(err)
		}
		exp, _ := time.Parse(time.RFC3339, inv.ExpiresAt)
		if d := time.Until(exp) - want; d > time.Minute || d < -time.Minute {
			t.Fatalf("%s expires in %v, want about %v", role, time.Until(exp), want)
		}
	}
}

func TestPreviewShowsOnlyOrgProjectRoleExpiryAndConsumesNothing(t *testing.T) {
	st, o := joinFixture(t)
	code, inv, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p, err := st.PreviewInvitation(code)
		if err != nil || p.Org != "Alpha" || p.Project != joinRemote || p.Role != RoleGuest || p.ExpiresAt != inv.ExpiresAt {
			t.Fatalf("preview %d: %+v %v", i, p, err)
		}
	}
	var accepted string
	st.db.QueryRow(`SELECT accepted_at FROM invitations WHERE id=?`, inv.ID).Scan(&accepted)
	if accepted != "" {
		t.Fatal("a preview must not consume the invitation")
	}
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatalf("accept after previews: %v", err)
	}
}

// Jeder Grund, aus dem ein Code nicht taugt, ist für den Betrachter dasselbe:
// derselbe Fehler, kein Hinweis auf den Grund.
func TestPreviewRejectsEveryInvalidVariantIdentically(t *testing.T) {
	st, o := joinFixture(t)
	mk := func(role string) (string, Invitation) {
		c, i, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, role, 0)
		if err != nil {
			t.Fatal(err)
		}
		return c, i
	}
	expired, expiredInv := mk(RoleMember)
	st.db.Exec(`UPDATE invitations SET expires_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), expiredInv.ID)
	used, _ := mk(RoleMember)
	if _, err := st.AcceptInvitation("person:2", used); err != nil {
		t.Fatal(err)
	}
	revoked, revokedInv := mk(RoleMember)
	if err := st.RevokeInvitation("person:1", o.ID, revokedInv.ID); err != nil {
		t.Fatal(err)
	}
	moved, _ := mk(RoleMember)
	st.db.Exec(`UPDATE projects SET org_id=? WHERE remote=?`, mustOrg(t, st, "person:2", "Beta", "beta").ID, joinRemote)
	ownerLink, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgOwner, 0)
	emailBound, _, _ := st.CreateInvitation("person:1", o.ID, "x@example.test", OrgMember, 0)
	cases := map[string]string{
		"unknown": "0000000000000000000000000000000000000000000000000000000000000000", "empty": "", "garbage": "../../etc/passwd",
		"expired": expired, "used": used, "revoked": revoked, "project moved": moved,
		"org-wide owner invitation": ownerLink, "org-wide email invitation": emailBound,
	}
	for name, code := range cases {
		p, err := st.PreviewInvitation(code)
		if !errors.Is(err, ErrCodeInvalid) || p != (InvitePreview{}) {
			t.Errorf("%s: preview=%+v err=%v", name, p, err)
		}
	}
}

func TestPreviewInviterNoLongerOwner(t *testing.T) {
	st, o := joinFixture(t)
	code, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	if _, err := st.PreviewInvitation(code); err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE org_members SET role='member' WHERE org_id=? AND account_id=1`, o.ID)
	if _, err := st.PreviewInvitation(code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("demoted inviter: %v", err)
	}
}

func TestAcceptProjectInvitationGrantsOnlyTheProjectRoleOnce(t *testing.T) {
	st, o := joinFixture(t)
	if _, err := st.EnsureProject("person:1", "github.com/alpha/other"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	org, err := st.AcceptInvitation("person:2", code)
	if err != nil || org.ID != o.ID {
		t.Fatalf("accept: %+v %v", org, err)
	}
	if st.OrgRole(o.ID, "person:2") != OrgMember {
		t.Fatal("invitee must be an org member")
	}
	if got := st.ProjectRole(joinRemote, "person:2").Role; got != RoleGuest {
		t.Fatalf("project role %q, want guest", got)
	}
	if got := st.ProjectRole("github.com/alpha/other", "person:2").Role; got == RoleGuest || got == RoleMember {
		t.Fatalf("invitation leaked a role into another project: %q", got)
	}
	var via string
	st.db.QueryRow(`SELECT via FROM role_events WHERE subject='person:2'`).Scan(&via)
	if via != RoleViaInvitation {
		t.Fatalf("role event via=%q", via)
	}
	if _, err := st.AcceptInvitation("person:3", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second use: %v", err)
	}
}

func TestAcceptProjectInvitationIsAtomicUnderConcurrency(t *testing.T) {
	st, o := joinFixture(t)
	code, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	results := make(chan error, 2)
	for _, who := range []string{"person:2", "person:3"} {
		go func() { _, err := st.AcceptInvitation(who, code); results <- err }()
	}
	ok := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d accounts consumed one invitation", ok)
	}
	var members int
	st.db.QueryRow(`SELECT COUNT(*) FROM project_members WHERE role='member'`).Scan(&members)
	if members != 1 {
		t.Fatalf("%d project grants for one invitation", members)
	}
}

func TestAcceptProjectInvitationForExistingOrgMemberAddsRoleNeverLowers(t *testing.T) {
	st, o := joinFixture(t)
	first, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", first); err != nil {
		t.Fatal(err)
	}
	guest, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	if _, err := st.AcceptInvitation("person:2", guest); err != nil {
		t.Fatalf("org member takes a project role: %v", err)
	}
	if st.ProjectRole(joinRemote, "person:2").Role != RoleGuest {
		t.Fatal("guest role missing")
	}
	again, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	if _, err := st.AcceptInvitation("person:2", again); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("same role again: %v", err)
	}
	member, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	if _, err := st.AcceptInvitation("person:2", member); err != nil {
		t.Fatal(err)
	}
	downgrade, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	if _, err := st.AcceptInvitation("person:2", downgrade); !errors.Is(err, ErrAlreadyMember) {
		t.Fatalf("downgrade: %v", err)
	}
	if st.ProjectRole(joinRemote, "person:2").Role != RoleMember {
		t.Fatal("role was lowered")
	}
}

func TestGuestLinkNeedsEnforcement(t *testing.T) {
	st, o := joinFixture(t)
	st.SetAccessMode(AccessMode{Enforce: false})
	if _, _, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0); !errors.Is(err, ErrGuestLinkNeedsEnforcement) {
		t.Fatalf("guest link without enforcement: %v", err)
	}
	if _, _, err := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0); err != nil {
		t.Fatalf("member link without enforcement: %v", err)
	}
}

func TestProjectInvitationNormalizesTheRemoteAndIsListedWithProjectAndRole(t *testing.T) {
	st, o := joinFixture(t)
	if _, _, err := st.CreateProjectInvitation("person:1", o.ID, "https://github.com/Alpha/App.git", RoleGuest, 0); err != nil {
		t.Fatalf("url form of the remote: %v", err)
	}
	if _, _, err := st.CreateProjectInvitation("person:1", o.ID, "has space", RoleGuest, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad remote: %v", err)
	}
	list, err := st.ListInvitations("person:1", o.ID)
	if err != nil || len(list) != 1 || list[0].ProjectRemote != joinRemote || list[0].ProjectRole != RoleGuest {
		t.Fatalf("list = %+v %v", list, err)
	}
}

func TestProjectInvitationKeepsCanReviewOfAnExistingRole(t *testing.T) {
	st, o := joinFixture(t)
	if _, err := st.AcceptInvitation("person:2", mustInvite(t, st, o)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", joinRemote, "person:2", RoleGuest, true, RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if info := st.ProjectRole(joinRemote, "person:2"); info.Role != RoleMember || !info.CanReview {
		t.Fatalf("role after upgrade: %+v", info)
	}
}

func mustInvite(t *testing.T, st *Store, o Org) string {
	t.Helper()
	code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// Die Datenbank selbst lässt keine andere Projektrolle zu, und eine Einladung,
// die sie doch trüge, ließe sich nicht einlösen.
func TestProjectInvitationRoleIsConstrained(t *testing.T) {
	st, o := joinFixture(t)
	if _, err := st.db.Exec(`INSERT INTO invitations(org_id, project_id, project_role, role, code_hash, invited_by, created_at, expires_at) VALUES(?,1,'lead','member','x',1,?,?)`,
		o.ID, now(), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err == nil {
		t.Fatal("a lead invitation row was accepted by the database")
	}
}

func TestOrgMemberListIsReducedForGuestsAndPlainMembers(t *testing.T) {
	st, o := joinFixture(t)
	guest, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleGuest, 0)
	member, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	if _, err := st.AcceptInvitation("person:2", guest); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", member); err != nil {
		t.Fatal(err)
	}
	names := func(viewer string) []string {
		list, err := st.ListOrgMembersFor(o.ID, viewer)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range list {
			out = append(out, m.Account)
		}
		return out
	}
	if got := names("person:2"); len(got) != 2 || got[0] == got[1] {
		t.Fatalf("guest sees %v, want only himself and the owner", got)
	}
	for _, got := range names("person:2") {
		if got == "ben" {
			t.Fatal("the guest sees another member")
		}
	}
	if got := names("person:3"); len(got) != 3 {
		t.Fatalf("member sees %v", got)
	}
	if got := names("person:1"); len(got) != 3 {
		t.Fatalf("owner sees %v", got)
	}
}

func TestIsProjectInvitation(t *testing.T) {
	st, o := joinFixture(t)
	project, _, _ := st.CreateProjectInvitation("person:1", o.ID, joinRemote, RoleMember, 0)
	if !st.IsProjectInvitation(project) || st.IsProjectInvitation(mustInvite(t, st, o)) || st.IsProjectInvitation("nope") {
		t.Fatal("IsProjectInvitation is wrong")
	}
}
