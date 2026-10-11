package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Round 3: who counts as the author of a machine note at delivery time.
func TestMachineNoteAuthorMatchingIsCaseBlindAndFollowsRenames(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetAccessMode(AccessMode{Enforce: true})
	for i, name := range []string{"robin", "anna", "bea"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ClaimMachine("annabox", "person:2"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimMachine("beabox", "person:3"); err != nil {
		t.Fatal(err)
	}
	// anna was called "annie" before.
	if _, err := st.db.Exec(`INSERT INTO person_name_history(person_id, old_name, old_key, new_name, source, created_at) VALUES(2,'annie','annie','anna','test','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	reader := st.Access(Principal{ID: "person:1", Label: "robin"})
	delivers := func(person string, machine string) bool {
		t.Helper()
		// A fresh access per call: the index is loaded once per request.
		return st.Access(reader.principal).CanDeliverKnowledge(Knowledge{Person: person, Scope: scope.Axes{Machine: machine}})
	}
	for _, tc := range []struct {
		name, person, machine string
		want                  bool
	}{
		{"own note, same case", "anna", "annabox", true},
		{"own note, other case", "ANNA", "annabox", true},
		{"author renamed since: the old name still counts", "Annie", "annabox", true},
		{"another account's note on her machine", "bea", "annabox", false},
		{"unclaimed name belongs to the instance owner", "robin", "newbox", true},
		{"same, other case", "Robin", "newbox", true},
		{"another account's note on an unclaimed name", "anna", "newbox", false},
		{"legacy label that points to no account, unclaimed name", "claude", "newbox", true},
		{"legacy label on the instance owner's machine", "claude", "mainex", true},
		{"legacy label on somebody else's machine", "claude", "beabox", false},
		{"no author", "", "beabox", true},
	} {
		if got := delivers(tc.person, tc.machine); got != tc.want {
			t.Errorf("%s: delivers = %v, want %v", tc.name, got, tc.want)
		}
	}
}
