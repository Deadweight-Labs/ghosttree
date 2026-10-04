package store

import (
	"errors"
	"strings"
	"testing"
)

func inviteIdentity(t *testing.T, st *Store, issuer, subject, name string) Account {
	t.Helper()
	org, err := st.CreateOrg("person:1", "Alpha-"+subject, "alpha-"+subject)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateInvitation("person:1", org.ID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := st.LoginIdentity(IdentityLogin{Issuer: issuer, Subject: subject, Name: name, Code: code})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoginRefreshesAnIdpNameUntilThePersonChoosesOne(t *testing.T) {
	st := orgStore(t, "robin")
	a := inviteIdentity(t, st, "idp", "sub-1", "") // the IdP gave no name at first
	if a.Name != "user" {
		t.Fatalf("fallback name %q", a.Name)
	}
	got, outcome, err := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "sub-1", Name: "Anna Berger"})
	if err != nil || outcome != LoginExisting || got.Name != "Anna Berger" {
		t.Fatalf("login: %+v %v %v", got, outcome, err)
	}
	if again, _ := st.AccountByPrincipalID(a.ID); again.Name != "Anna Berger" {
		t.Fatalf("stored name %q", again.Name)
	}
	// A later IdP change follows while the name is still the IdP's.
	got, _, _ = st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "sub-1", Name: "Anna Meier"})
	if got.Name != "Anna Meier" {
		t.Fatalf("idp rename: %q", got.Name)
	}
	// An empty IdP name never erases the name.
	got, _, _ = st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "sub-1"})
	if got.Name != "Anna Meier" {
		t.Fatalf("empty name: %q", got.Name)
	}
	if _, err := st.SetOwnName(a.ID, "Anni"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "sub-1", Name: "Anna Berger"})
	if got.Name != "Anni" {
		t.Fatalf("a chosen name must survive the login, got %q", got.Name)
	}
}

func TestLoginNameSyncNeverCollides(t *testing.T) {
	st := orgStore(t, "robin")
	first := inviteIdentity(t, st, "idp", "a", "Anna")
	second := inviteIdentity(t, st, "idp", "b", "") // "user"
	if first.Name != "Anna" || second.Name != "user" {
		t.Fatalf("%q %q", first.Name, second.Name)
	}
	got, _, err := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "b", Name: "ANNA"})
	if err != nil || got.Name != "ANNA-2" {
		t.Fatalf("collision: %q %v", got.Name, err)
	}
	// The same name again is not a change and must not produce ANNA-3.
	got, _, _ = st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "b", Name: "ANNA"})
	if got.Name != "ANNA-2" {
		t.Fatalf("stable: %q", got.Name)
	}
}

func TestLoginDoesNotOverwriteANameAnAdminChose(t *testing.T) {
	st := orgStore(t)
	if _, err := st.AddAccount("Robin", "", true); err != nil {
		t.Fatal(err)
	}
	code, _, err := st.CreateAccountCode(CodeClaim, "Robin")
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "r", Name: "robin-login", Code: code})
	if err != nil || a.Name != "Robin" {
		t.Fatalf("claim: %+v %v", a, err)
	}
	got, _, _ := st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "r", Name: "Somebody Else"})
	if got.Name != "Robin" {
		t.Fatalf("admin-chosen name replaced by %q", got.Name)
	}
	// A leftover fallback name from before the sync existed is fair game.
	if _, err := st.db.Exec(`UPDATE persons SET name='admin' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.LoginIdentity(IdentityLogin{Issuer: "idp", Subject: "r", Name: "Robin G"})
	if got.Name != "Robin G" {
		t.Fatalf("legacy fallback not replaced: %q", got.Name)
	}
}

func TestSetOwnName(t *testing.T) {
	st := orgStore(t, "robin")
	b := inviteIdentity(t, st, "idp", "b", "Ben")
	for in, want := range map[string]string{"  Ben   K  ": "Ben K", "ben": "ben", "Jörg_2": "Jörg_2"} {
		a, err := st.SetOwnName(b.ID, in)
		if err != nil || a.Name != want {
			t.Fatalf("SetOwnName(%q) = %q, %v; want %q", in, a.Name, err, want)
		}
	}
	for _, name := range []string{"Robin", "ROBIN", "Ｒｏｂｉｎ"} {
		if _, err := st.SetOwnName(b.ID, name); !errors.Is(err, ErrAccountNameTaken) {
			t.Errorf("SetOwnName(%q) = %v, want taken", name, err)
		}
	}
	for _, name := range []string{"", "   ", "...", "-_-", strings.Repeat("a", MaxDisplayNameRunes+1)} {
		if _, err := st.SetOwnName(b.ID, name); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("SetOwnName(%q) = %v, want invalid", name, err)
		}
	}
	if _, err := st.SetOwnName("person:999", "Zed"); err == nil {
		t.Fatal("unknown account")
	}
}

func TestIdpDisplayNameHelpers(t *testing.T) {
	for in, want := range map[string]bool{"admin": true, "user": true, "user-2": true, "Admin": true, "Anna": false, "users": false, "": true} {
		if got := isFallbackAccountName(in); got != want {
			t.Errorf("isFallbackAccountName(%q) = %v", in, got)
		}
	}
}
