package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const roleProject = "github.com/dw/p"

// roleFixture: robin (person:1) ist Org-Owner, anna, ben und cleo sind
// einfache Mitglieder ohne Projektrolle, dev hat keine Organisation.
func roleFixture(t *testing.T) *Store {
	t.Helper()
	st := orgStore(t, "robin", "anna", "ben", "cleo", "dev")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	for _, who := range []string{"person:2", "person:3", "person:4"} {
		code, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AcceptInvitation(who, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnsureProject("person:1", roleProject); err != nil {
		t.Fatal(err)
	}
	return st
}

func setRole(st *Store, actor, target, role string, review bool) error {
	return st.SetProjectRole(actor, roleProject, target, role, review, RoleViaAPI)
}

func TestOrgOwnerIsImplicitProjectOwner(t *testing.T) {
	st := roleFixture(t)
	got := st.ProjectRole(roleProject, "person:1")
	if got.Role != RoleOwner || !got.Implicit {
		t.Fatalf("org owner must be implicit project owner: %+v", got)
	}
	if got := st.ProjectRole(roleProject, "person:2"); got.Role != "" {
		t.Fatalf("org membership alone gives no project role: %+v", got)
	}
	if got := st.ProjectRole(roleProject, "person:5"); got.Role != "" {
		t.Fatalf("account outside the org has no role: %+v", got)
	}
	if got := st.ProjectRole("github.com/dw/unknown", "person:1"); got.Role != "" {
		t.Fatalf("unknown project has no roles: %+v", got)
	}
	// Eine gespeicherte Zeile ändert am Org-Owner nichts und lässt sich nicht
	// anlegen.
	if err := setRole(st, "person:1", "person:1", RoleGuest, false); !errors.Is(err, ErrImplicitOwner) {
		t.Fatalf("demoting an org owner through a project row: %v", err)
	}
	members, err := st.ListProjectMembers(roleProject)
	if err != nil || len(members) != 1 || members[0].Account != "robin" || !members[0].Implicit || members[0].Role != RoleOwner {
		t.Fatalf("members = %+v %v", members, err)
	}
	// Stuft die Organisation ihn herab, ist er auch im Projekt nichts mehr.
	if _, err := st.db.Exec(`UPDATE org_members SET role='member' WHERE account_id=1`); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(roleProject, "person:1"); got.Role != "" {
		t.Fatalf("implicit owner must follow the org role: %+v", got)
	}
}

func TestSetProjectRoleGrantRights(t *testing.T) {
	st := roleFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(setRole(st, "person:1", "person:2", RoleLead, true)) // anna: lead + reviewer
	must(setRole(st, "person:1", "person:3", RoleMember, false))
	if got := st.ProjectRole(roleProject, "person:2"); got.Role != RoleLead || !got.CanReview || got.Implicit {
		t.Fatalf("anna = %+v", got)
	}

	// Ein Lead vergibt member und guest an Konten unter lead.
	must(setRole(st, "person:2", "person:4", RoleMember, true))
	must(setRole(st, "person:2", "person:4", RoleGuest, false))
	for _, role := range []string{RoleOwner, RoleLead} {
		if err := setRole(st, "person:2", "person:4", role, false); !errors.Is(err, ErrRoleForbidden) {
			t.Fatalf("lead granting %s: %v", role, err)
		}
	}
	must(setRole(st, "person:1", "person:4", RoleLead, false))
	if err := setRole(st, "person:2", "person:4", RoleMember, false); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("lead changing another lead: %v", err)
	}
	if err := setRole(st, "person:2", "person:1", RoleMember, false); !errors.Is(err, ErrImplicitOwner) {
		t.Fatalf("lead touching the org owner: %v", err)
	}

	// Keine Selbsterhöhung, auch nicht über das Flag oder ins gleiche Level.
	must(setRole(st, "person:1", "person:2", RoleLead, false))
	for _, role := range []string{RoleOwner, RoleLead} {
		if err := setRole(st, "person:2", "person:2", role, true); !errors.Is(err, ErrSelfPromotion) {
			t.Fatalf("lead promoting itself to %s: %v", role, err)
		}
	}
	// Member und guest vergeben nichts, auch nicht an sich selbst.
	if err := setRole(st, "person:3", "person:3", RoleLead, false); !errors.Is(err, ErrNotGrantor) {
		t.Fatalf("member self-promotion: %v", err)
	}
	must(setRole(st, "person:1", "person:4", RoleGuest, false))
	if err := setRole(st, "person:4", "person:3", RoleGuest, false); !errors.Is(err, ErrNotGrantor) {
		t.Fatalf("guest granting: %v", err)
	}
	if err := st.RemoveProjectRole("person:3", roleProject, "person:4", RoleViaAPI); !errors.Is(err, ErrNotGrantor) {
		t.Fatalf("member removing: %v", err)
	}
	// Wer nicht in der Organisation ist, sieht das Projekt nicht; Ziele auch.
	if err := setRole(st, "person:5", "person:3", RoleGuest, false); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("outsider granting: %v", err)
	}
	if err := setRole(st, "person:1", "person:5", RoleGuest, false); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("granting outside the org: %v", err)
	}
	if err := setRole(st, "person:1", "person:3", "reviewer", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reviewer is a flag, not a role: %v", err)
	}
	if err := setRole(st, "person:1", "person:3", "", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty role: %v", err)
	}

	// Ein Lead darf sich selbst senken, ein Owner ebenso.
	must(setRole(st, "person:2", "person:2", RoleMember, false))
	must(setRole(st, "person:1", "person:3", RoleOwner, false))
	must(setRole(st, "person:3", "person:3", RoleMember, false))

	// Entfernen löscht die Zeile; danach gibt es nichts mehr zu entfernen.
	must(st.RemoveProjectRole("person:1", roleProject, "person:4", RoleViaCLI))
	if got := st.ProjectRole(roleProject, "person:4"); got.Role != "" {
		t.Fatalf("removed role still there: %+v", got)
	}
	if err := st.RemoveProjectRole("person:1", roleProject, "person:4", RoleViaCLI); !errors.Is(err, ErrNoProjectRole) {
		t.Fatalf("removing twice: %v", err)
	}

	if got := st.GrantableRoles("person:2", roleProject, "person:4"); len(got) != 0 {
		t.Fatalf("member anna grants nothing: %v", got)
	}
	must(setRole(st, "person:1", "person:2", RoleLead, false))
	if got := st.GrantableRoles("person:2", roleProject, "person:4"); len(got) != 2 || got[0] != RoleMember || got[1] != RoleGuest {
		t.Fatalf("lead grantable = %v", got)
	}
	if got := st.GrantableRoles("person:1", roleProject, "person:4"); len(got) != 4 {
		t.Fatalf("owner grantable = %v", got)
	}

	// Jede Änderung ist protokolliert, und das Protokoll lässt sich nicht ändern.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM role_events WHERE scope=?`, "project:"+roleProject).Scan(&n); err != nil || n < 10 {
		t.Fatalf("role_events = %d %v", n, err)
	}
	var via, actor string
	if err := st.db.QueryRow(`SELECT via, actor FROM role_events WHERE old_role='guest' AND new_role='' ORDER BY id DESC LIMIT 1`).Scan(&via, &actor); err != nil || via != RoleViaCLI || actor != "person:1" {
		t.Fatalf("removal event via=%q actor=%q %v", via, actor, err)
	}
	if _, err := st.db.Exec(`UPDATE role_events SET actor='x'`); err == nil {
		t.Fatal("role_events must be append-only (update)")
	}
	if _, err := st.db.Exec(`DELETE FROM role_events`); err == nil {
		t.Fatal("role_events must be append-only (delete)")
	}
}

func TestLastProjectOwnerIsProtected(t *testing.T) {
	st := roleFixture(t)
	if err := setRole(st, "person:1", "person:3", RoleOwner, false); err != nil {
		t.Fatal(err)
	}
	// Mit einem Org-Owner als zweitem Owner darf ben gehen.
	if err := setRole(st, "person:3", "person:3", RoleLead, false); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:1", "person:3", RoleOwner, false); err != nil {
		t.Fatal(err)
	}
	// Ohne Org-Owner ist ben der einzige Owner.
	if _, err := st.db.Exec(`UPDATE org_members SET role='member' WHERE account_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:3", "person:3", RoleLead, false); !errors.Is(err, ErrLastProjectOwner) {
		t.Fatalf("demoting the last owner: %v", err)
	}
	if err := st.RemoveProjectRole("person:3", roleProject, "person:3", RoleViaWeb); !errors.Is(err, ErrLastProjectOwner) {
		t.Fatalf("removing the last owner: %v", err)
	}
	if got := st.ProjectRole(roleProject, "person:3"); got.Role != RoleOwner {
		t.Fatalf("last owner lost the role: %+v", got)
	}
}

func TestLeavingOrMovingDropsProjectRoles(t *testing.T) {
	st := roleFixture(t)
	if err := setRole(st, "person:1", "person:2", RoleLead, false); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveOrgMember("person:2", 1, "person:2"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := st.CreateInvitation("person:1", 1, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(roleProject, "person:2"); got.Role != "" {
		t.Fatalf("a rejoining member starts without a role: %+v", got)
	}
}

func TestCapAgentRole(t *testing.T) {
	cases := []struct{ requested, account, want string }{
		{"lead", RoleOwner, RoleLead}, // höchstens lead
		{"lead", RoleLead, RoleLead},
		{"lead", RoleMember, RoleMember}, // Anforderung über dem Konto
		{"lead", RoleGuest, RoleGuest},
		{"lead", "", RoleGuest}, // keine Rolle = guest
		{"member", RoleOwner, RoleMember},
		{"guest", RoleOwner, RoleGuest},
		{"", RoleOwner, RoleMember}, // nicht angegeben = member
		{"owner", RoleOwner, RoleLead},
		{"owner", RoleMember, RoleMember},
		{"nonsense", RoleOwner, RoleMember},
	}
	for _, c := range cases {
		if got := CapAgentRole(c.requested, c.account); got != c.want {
			t.Errorf("CapAgentRole(%q,%q) = %q, want %q", c.requested, c.account, got, c.want)
		}
	}
}

func registerRoleAgent(t *testing.T, st *Store, id, principal, room, role string) {
	t.Helper()
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: id, Provider: "claude", RoomKey: room, DisplayName: id, PrincipalID: principal, Role: role}); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveAgentRoleIsLive(t *testing.T) {
	st := roleFixture(t)
	room := RoomKeyForProject(roleProject)
	if err := setRole(st, "person:1", "person:2", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	registerRoleAgent(t, st, "claude:h:owner", "person:1", room, "lead")
	registerRoleAgent(t, st, "claude:h:owner-guest", "person:1", room, "guest")
	registerRoleAgent(t, st, "claude:h:member", "person:2", room, "lead") // über dem Konto
	registerRoleAgent(t, st, "claude:h:norole", "person:3", room, "lead")
	registerRoleAgent(t, st, "claude:h:default", "person:1", room, "")
	want := map[string]string{
		"claude:h:owner": RoleLead, "claude:h:owner-guest": RoleGuest, "claude:h:member": RoleMember,
		"claude:h:norole": RoleGuest, "claude:h:default": RoleMember,
	}
	for id, role := range want {
		if got := st.EffectiveAgentRole(roleProject, id); got.Role != role {
			t.Errorf("%s = %q, want %q", id, got.Role, role)
		}
	}
	if got := st.EffectiveAgentRole(roleProject, "claude:h:unknown"); got.Role != RoleGuest {
		t.Errorf("unknown agent = %+v", got)
	}

	// Live: das Konto steigt, der Agent folgt bis lead, aber nicht höher.
	for _, role := range []string{RoleLead, RoleOwner} {
		if err := setRole(st, "person:1", "person:2", role, role == RoleLead); err != nil {
			t.Fatal(err)
		}
		got := st.EffectiveAgentRole(roleProject, "claude:h:member")
		if got.Role != RoleLead || !got.CanReview == (role == RoleLead) {
			t.Errorf("account %s: agent = %+v", role, got)
		}
	}
	// Und es folgt nach unten, ohne dass der Agent neu startet.
	if err := setRole(st, "person:1", "person:2", RoleGuest, false); err != nil {
		t.Fatal(err)
	}
	if got := st.EffectiveAgentRole(roleProject, "claude:h:member"); got.Role != RoleGuest {
		t.Errorf("demoted account: agent = %+v", got)
	}
	// Erneutes Anmelden ohne Rolle lässt die Anforderung stehen, mit Rolle senkt sie.
	registerRoleAgent(t, st, "claude:h:owner", "person:1", room, "")
	if got := st.EffectiveAgentRole(roleProject, "claude:h:owner"); got.Role != RoleLead {
		t.Errorf("re-register without role changed it: %+v", got)
	}
	registerRoleAgent(t, st, "claude:h:owner", "person:1", room, "member")
	if got := st.EffectiveAgentRole(roleProject, "claude:h:owner"); got.Role != RoleMember {
		t.Errorf("re-register lowering: %+v", got)
	}
	if _, err := st.RegisterCoordAgent(CoordAgent{ExternalID: "claude:h:x", Provider: "claude", RoomKey: room, DisplayName: "x", PrincipalID: "person:1", Role: "owner"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("agents are never owner: %v", err)
	}
}

func TestPeersShowRoles(t *testing.T) {
	st := roleFixture(t)
	room := RoomKeyForProject(roleProject)
	if err := setRole(st, "person:1", "person:2", RoleMember, true); err != nil {
		t.Fatal(err)
	}
	registerRoleAgent(t, st, "claude:h:a", "person:1", room, "lead")
	registerRoleAgent(t, st, "claude:h:b", "person:2", room, "lead")
	registerRoleAgent(t, st, "claude:h:c", "person:3", room, "member")
	peers, err := st.CoordPeers(room, "")
	if err != nil || len(peers) != 3 {
		t.Fatalf("peers = %+v %v", peers, err)
	}
	got := map[string]CoordAgent{}
	for _, p := range peers {
		got[p.ExternalID] = p
	}
	if p := got["claude:h:a"]; p.Role != RoleLead || p.Owner != "robin" || p.CanReview {
		t.Errorf("a = %+v", p)
	}
	if p := got["claude:h:b"]; p.Role != RoleMember || !p.CanReview || p.RequestedRole != RoleLead {
		t.Errorf("b = %+v", p)
	}
	if p := got["claude:h:c"]; p.Role != RoleGuest {
		t.Errorf("c = %+v", p)
	}
	// Maschinenräume kennen keine Rollen.
	registerRoleAgent(t, st, "claude:h:m", "person:1", RoomKeyForMachine("host"), "lead")
	peers, err = st.CoordPeers(RoomKeyForMachine("host"), "")
	if err != nil || len(peers) != 1 || peers[0].Role != "" || peers[0].RequestedRole != "" {
		t.Errorf("machine room peers = %+v %v", peers, err)
	}
}

func TestProjectRolesThroughRuntimeWriter(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, n := range []string{"robin", "anna"} {
		st.AddPerson(n)
	}
	o, _ := st.CreateOrg("person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", roleProject); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectRole("person:1", roleProject, "person:2", RoleLead, true, RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(roleProject, "person:2"); got.Role != RoleLead || !got.CanReview {
		t.Fatalf("role through the writer: %+v", got)
	}
}

// Eine Datenbank aus der Zeit vor Paket 6 hat weder die Rollentabellen noch
// coord_agents.role. Beim Öffnen entstehen sie; vorhandene Org-Mitglieder
// bekommen keine gespeicherte Rolle (Default laut Spec: Org-Owner implizit
// owner, einfache Mitglieder nichts), vorhandene Agenten member.
func TestRolesMigrationOnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"robin", "anna"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:1", roleProject); err != nil {
		t.Fatal(err)
	}
	room := RoomKeyForProject(roleProject)
	registerRoleAgent(t, st, "claude:h:old", "person:2", room, "")
	st.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE role_events`, `DROP TABLE project_members`, `ALTER TABLE coord_agents DROP COLUMN role`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	for i := 0; i < 2; i++ { // zweites Öffnen ist idempotent
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if got := st.ProjectRole(roleProject, "person:1"); got.Role != RoleOwner || !got.Implicit {
			t.Fatalf("open %d: org owner = %+v", i, got)
		}
		if got := st.ProjectRole(roleProject, "person:2"); (got.Role != "") != (i == 1) {
			t.Fatalf("open %d: existing member must start without a stored role: %+v", i, got)
		}
		var stored string
		if err := st.db.QueryRow(`SELECT role FROM coord_agents WHERE external_id='claude:h:old'`).Scan(&stored); err != nil || stored != "member" {
			t.Fatalf("open %d: existing agent role = %q %v", i, stored, err)
		}
		if got := st.EffectiveAgentRole(roleProject, "claude:h:old"); (got.Role != RoleGuest) != (i == 1) {
			t.Fatalf("open %d: agent of a role-less account = %+v", i, got)
		}
		if err := st.SetProjectRole("person:1", roleProject, "person:2", RoleMember, false, RoleViaCLI); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if got := st.EffectiveAgentRole(roleProject, "claude:h:old"); got.Role != RoleMember {
			t.Fatalf("open %d: agent after grant = %+v", i, got)
		}
		st.Close()
	}
}

func TestLeadCannotRemoveOrDemoteAnotherLead(t *testing.T) {
	st := roleFixture(t)
	for _, who := range []string{"person:2", "person:3"} {
		if err := setRole(st, "person:1", who, RoleLead, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RemoveProjectRole("person:2", roleProject, "person:3", RoleViaWeb); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("lead removing a lead: %v", err)
	}
	if err := setRole(st, "person:2", "person:3", RoleGuest, false); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("lead demoting a lead: %v", err)
	}
	if got := st.ProjectRole(roleProject, "person:3"); got.Role != RoleLead {
		t.Fatalf("the other lead lost the role: %+v", got)
	}
	// Die eigene Stufe bleibt wählbar (Flag umschalten), höher nicht.
	got := st.GrantableRoles("person:2", roleProject, "person:2")
	if strings.Join(got, ",") != "lead,member,guest" {
		t.Fatalf("self grantable = %v", got)
	}
}
