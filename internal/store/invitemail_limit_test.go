package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestInviteMailLimitsPerAccountOrgAndAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limit.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// One address gets few mails a day, whoever writes.
	for i := 0; i < maxInviteMailsPerAddress; i++ {
		if err := st.ReserveInviteMail("person:1", 1, "pia@example.test"); err != nil {
			t.Fatalf("mail %d: %v", i, err)
		}
	}
	// Case, a +tag, quotes and a dot at the end of the domain are the same mailbox.
	for _, same := range []string{"Pia@Example.test", "pia+x@example.test", `"pia"@example.test`, "pia@example.test."} {
		if err := st.ReserveInviteMail("person:2", 2, same); !errors.Is(err, ErrInviteMailLimit) {
			t.Fatalf("%s is the same mailbox and over its limit: %v", same, err)
		}
	}
	// An account is limited over all addresses.
	for i := 0; i < maxInviteMailsPerAccount; i++ {
		if err := st.ReserveInviteMail("person:3", 3, "x"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.test"); err != nil {
			t.Fatalf("account mail %d: %v", i, err)
		}
	}
	if err := st.ReserveInviteMail("person:3", 4, "fresh@example.test"); !errors.Is(err, ErrInviteMailLimit) {
		t.Fatalf("the account is over its limit: %v", err)
	}
	// An organization is limited over all accounts.
	for i := 0; i < maxInviteMailsPerOrg; i++ {
		sender := "person:" + string(rune('A'+i%20)) + string(rune('a'+i/20))
		if err := st.ReserveInviteMail(sender, 9, "o"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.test"); err != nil {
			t.Fatalf("org mail %d: %v", i, err)
		}
	}
	if err := st.ReserveInviteMail("person:Zz", 9, "late@example.test"); !errors.Is(err, ErrInviteMailLimit) {
		t.Fatalf("the organization is over its limit: %v", err)
	}
	// A restart does not reset the counts.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ReserveInviteMail("person:1", 1, "pia@example.test"); !errors.Is(err, ErrInviteMailLimit) {
		t.Fatalf("after a restart: %v", err)
	}
}
