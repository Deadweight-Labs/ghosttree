package store

import "testing"

func isGuest(st *Store, account int) bool {
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM org_guests WHERE account_id=?`, account).Scan(&n)
	return n > 0
}

func TestProjectInvitationAsMemberKeepsNewcomerOnThatProject(t *testing.T) {
	st := roleFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	mustShare(t, st, "person:1", otherProject, true)
	o, _ := st.OrgByRef("alpha")
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, sharedProject, RoleMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	if got := st.ProjectRole(sharedProject, "person:5"); got.Role != RoleMember {
		t.Fatalf("invited project: %+v", got)
	}
	if got := st.ProjectRole(otherProject, "person:5"); got.Role != "" {
		t.Fatalf("project link must not open other shared projects: %+v", got)
	}
	if st.ProjectWriters(otherProject)["dev"] {
		t.Fatal("writers must follow the invited project only")
	}
}

func TestBackfillMarksProjectOnlyAccountsAsGuests(t *testing.T) {
	st := roleFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	o, _ := st.OrgByRef("alpha")
	// 5 kam nur über eine Projekt-Einladung als member (Altbestand).
	code, _, err := st.CreateProjectInvitation("person:1", o.ID, sharedProject, RoleMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	// 2: volles Mitglied, Gast in A und Member in B. 3: volles Mitglied mit
	// nur Gast-Rolle in A (altes Gast-Konto). 4: volles Mitglied ohne Projektrolle.
	for _, q := range []string{
		`INSERT INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) SELECT id, 2, 'guest', 0, 1, '' FROM projects WHERE remote='` + sharedProject + `'`,
		`INSERT INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) SELECT id, 2, 'member', 0, 1, '' FROM projects WHERE remote='` + otherProject + `'`,
		`INSERT INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) SELECT id, 3, 'guest', 0, 1, '' FROM projects WHERE remote='` + sharedProject + `'`,
		`DELETE FROM org_guests`,
		`DELETE FROM org_state WHERE key='guest_backfill'`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := backfillOrgGuests(st.db); err != nil {
		t.Fatal(err)
	}
	for acc, want := range map[int]bool{5: true, 3: true, 2: false, 4: false} {
		if got := isGuest(st, acc); got != want {
			t.Errorf("account %d guest = %v, want %v", acc, got, want)
		}
	}
	if _, err := st.db.Exec(`DELETE FROM org_guests`); err != nil {
		t.Fatal(err)
	}
	if err := backfillOrgGuests(st.db); err != nil {
		t.Fatal(err)
	}
	if isGuest(st, 5) {
		t.Fatal("backfill must run only once")
	}
}
