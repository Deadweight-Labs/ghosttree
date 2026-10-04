package store

import (
	"errors"
	"testing"
)

func TestRetiredNameStaysReservedForOtherAccounts(t *testing.T) {
	st := orgStore(t, "robin")
	b := inviteIdentity(t, st, "idp", "b", "Ben")
	c := inviteIdentity(t, st, "idp", "c", "Cleo")
	if _, err := st.SetOwnName(b.ID, "Benedikt"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Ben", "BEN", "Ｂｅｎ"} {
		if _, err := st.SetOwnName(c.ID, name); !errors.Is(err, ErrAccountNameTaken) {
			t.Errorf("SetOwnName(%q) took a retired name: %v", name, err)
		}
	}
	// New accounts and the IdP sync get a different name, too.
	d := inviteIdentity(t, st, "idp", "d", "Ben")
	if d.Name != "Ben-2" {
		t.Fatalf("invited account took a retired name: %q", d.Name)
	}
	// The former holder may take it back.
	if a, err := st.SetOwnName(b.ID, "ben"); err != nil || a.Name != "ben" {
		t.Fatalf("former holder: %q, %v", a.Name, err)
	}
}

func TestPlaceholderNamesAreNotReserved(t *testing.T) {
	st := orgStore(t, "robin")
	b := inviteIdentity(t, st, "idp", "b", "") // placeholder "user"
	if _, err := st.SetOwnName(b.ID, "Ben"); err != nil {
		t.Fatal(err)
	}
	c := inviteIdentity(t, st, "idp", "c", "")
	if c.Name != "user" {
		t.Fatalf("placeholder was reserved: %q", c.Name)
	}
}

func nameEvents(t *testing.T, st *Store, id int64) [][3]string {
	t.Helper()
	rows, err := st.db.Query(`SELECT old_name, new_name, source FROM person_name_history WHERE person_id=? ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var e [3]string
		if err := rows.Scan(&e[0], &e[1], &e[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestEveryNameChangeIsAudited(t *testing.T) {
	st := orgStore(t, "robin")
	b := inviteIdentity(t, st, "idp", "b", "Ben")
	id, _ := accountNumericID(b.ID)
	if _, err := st.SetOwnName(b.ID, "Benny"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOwnName(b.ID, "Benny"); err != nil { // unchanged: no event
		t.Fatal(err)
	}
	got := nameEvents(t, st, id)
	if len(got) != 1 || got[0] != [3]string{"Ben", "Benny", "user"} {
		t.Fatalf("events: %v", got)
	}
	// An IdP rename is audited as well.
	e := inviteIdentity(t, st, "idp", "e", "Eva")
	eid, _ := accountNumericID(e.ID)
	if _, _, err := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "e", Name: "Eve"}); err != nil {
		t.Fatal(err)
	}
	got = nameEvents(t, st, eid)
	if len(got) != 1 || got[0] != [3]string{"Eva", "Eve", "idp"} {
		t.Fatalf("idp events: %v", got)
	}
	if _, err := st.db.Exec(`DELETE FROM person_name_history`); err == nil {
		t.Fatal("history must be append-only")
	}
}

func TestRenamingIsRateLimited(t *testing.T) {
	st := orgStore(t, "robin")
	b := inviteIdentity(t, st, "idp", "b", "Ben")
	for _, n := range []string{"Ben1", "Ben2", "Ben3"} {
		if _, err := st.SetOwnName(b.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SetOwnName(b.ID, "Ben4"); !errors.Is(err, ErrNameRateLimited) {
		t.Fatalf("fourth change: %v", err)
	}
	// Rejected names do not use up the budget of another account.
	c := inviteIdentity(t, st, "idp", "c", "Cleo")
	if _, err := st.SetOwnName(c.ID, "Cleo1"); err != nil {
		t.Fatal(err)
	}
	// Old changes fall out of the window.
	d := inviteIdentity(t, st, "idp", "d", "Dora")
	did, _ := accountNumericID(d.ID)
	for i := 0; i < 5; i++ {
		if _, err := st.db.Exec(`INSERT INTO person_name_history(person_id, old_name, old_key, new_name, source, created_at) VALUES(?,?,?,?,?,?)`,
			did, "x", "x"+string(rune('a'+i)), "y", "user", "2000-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SetOwnName(d.ID, "Dora1"); err != nil {
		t.Fatalf("old changes counted: %v", err)
	}
}

func TestMixedScriptNamesAreRefused(t *testing.T) {
	st := orgStore(t, "Peter")
	b := inviteIdentity(t, st, "idp", "b", "Ben")
	for _, name := range []string{"Рeter", "Pеter", "Ben Иван", "Ηello", "Peterа"} {
		if _, err := st.SetOwnName(b.ID, name); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("SetOwnName(%q) = %v, want invalid", name, err)
		}
	}
	if _, err := st.SetOwnName(b.ID, "Иван Петров"); err != nil {
		t.Fatalf("single non-Latin script refused: %v", err)
	}
	// An IdP name with mixed scripts is not adopted; a new account falls back.
	c := inviteIdentity(t, st, "idp", "c", "Рeter")
	if c.Name != "user" {
		t.Fatalf("mixed-script IdP name adopted: %q", c.Name)
	}
	if _, _, err := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "c", Name: "Pеter"}); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.AccountByPrincipalID(c.ID); a.Name != "user" {
		t.Fatalf("mixed-script IdP name synced: %q", a.Name)
	}
}

func TestGuestsCannotRename(t *testing.T) {
	st := roleFixture(t) // robin(1) owner, anna(2), ben(3), cleo(4) members of the org, dev(5) outside
	var orgID int64
	if err := st.db.QueryRow(`SELECT MIN(id) FROM orgs`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", orgID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:5", code); err != nil {
		t.Fatal(err)
	}
	if err := setRole(st, "person:1", "person:5", RoleGuest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOwnName("person:5", "Devon"); !errors.Is(err, ErrNameNotAllowed) {
		t.Fatalf("guest rename: %v", err)
	}
	// Once the person is a member somewhere, the name is theirs again.
	if err := setRole(st, "person:1", "person:5", RoleMember, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOwnName("person:5", "Devon"); err != nil {
		t.Fatal(err)
	}
}

func TestOrgInvitationNeverGrantsOwner(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	if _, _, err := st.CreateInvitation("person:1", o.ID, "", OrgOwner, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("owner invitation: %v", err)
	}
	// An owner invitation that predates this rule is dead as well.
	code := legacyOwnerInvitation(t, st, o.ID)
	if _, err := st.AcceptInvitation("person:2", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("legacy owner invitation accepted: %v", err)
	}
	if st.OrgRole(o.ID, "person:2") != "" {
		t.Fatal("anna became a member through an owner link")
	}
}

func TestMixedScriptDetection(t *testing.T) {
	for in, want := range map[string]bool{
		"Peter": false, "Иван Петров": false, "Ελένη": false, "田中たろう": false, "ベン 田中": false, "한국어 田中": false,
		"Jörg 3": false, "a-b_c": false, "Рeter": true, "Ben Иван": true, "Ηello": true, "Pеter": true, "Ωmega": true,
	} {
		if got := MixedScriptName(in); got != want {
			t.Errorf("MixedScriptName(%q) = %v, want %v", in, got, want)
		}
	}
}

// legacyOwnerInvitation plants an org-wide owner invitation the way an older
// version stored it; the API refuses to make one now.
func legacyOwnerInvitation(t *testing.T, st *Store, orgID int64) string {
	t.Helper()
	code, hash, err := newCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO invitations(org_id, role, code_hash, invited_by, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		orgID, OrgOwner, hash, 1, now(), "2999-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return code
}
