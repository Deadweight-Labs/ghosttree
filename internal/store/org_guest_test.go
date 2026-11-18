package store

import (
	"errors"
	"testing"
)

// Ein Gast gründet keine Organisation: als Owner wäre er kein Gast mehr, und
// jede Sperre für Gäste (Projekte anlegen, Namen ändern, Einladungen) fiele weg.
func TestAGuestDoesNotFoundAnOrganization(t *testing.T) {
	st := accessFixture(t)
	if _, err := st.CreateOrg("person:5", "Guest Corp", "guest-corp"); !errors.Is(err, ErrGuestOnly) {
		t.Fatalf("guest founds an organization: %v", err)
	}
	if !guestOnlyTx(st.db, 5) {
		t.Fatal("the refused attempt must leave the guest a guest")
	}
	// A member of the organization founds one.
	if _, err := st.CreateOrg("person:3", "Mia Corp", "mia-corp"); err != nil {
		t.Fatalf("member founds an organization: %v", err)
	}
}
