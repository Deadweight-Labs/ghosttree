package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Round 3: who counts as the author of a machine note at delivery time. The
// author is the immutable account of the entry; only legacy entries without one
// are matched by name.
func TestMachineNoteDeliveryFollowsTheAuthorAccount(t *testing.T) {
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
	history := func(person int, old string) {
		t.Helper()
		if _, err := st.db.Exec(`INSERT INTO person_name_history(person_id, old_name, old_key, new_name, source, created_at) VALUES(?,?,?,?,'test','2026-01-01T00:00:00Z')`, person, old, old, "x"); err != nil {
			t.Fatal(err)
		}
	}
	history(2, "annie") // anna used to be called annie
	history(2, "dup")   // two accounts once shared a name: it is ambiguous now
	history(3, "dup")

	entry := func(person string, author int64, machine string) Knowledge {
		t.Helper()
		id, err := st.InsertKnowledge(Knowledge{Type: "instruction", Title: person + machine, Body: "b", Person: person, AuthorID: author, Scope: scope.Axes{Machine: machine}})
		if err != nil {
			t.Fatal(err)
		}
		k, err := st.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	delivers := func(k Knowledge) bool {
		t.Helper()
		// A fresh access per call: the index is loaded once per request.
		return st.Access(Principal{ID: "person:1", Label: "robin"}).CanDeliverKnowledge(k)
	}
	for _, tc := range []struct {
		name string
		k    Knowledge
		want bool
	}{
		{"own note on her machine", entry("anna", 2, "annabox"), true},
		{"the author renamed herself: the account decides, not the label", entry("anna-before-rename", 2, "annabox"), true},
		{"rename attack: a note on a free name stays hers, not the instance owner's", entry("former-name-of-no-account", 2, "newbox"), false},
		{"another account's note on her machine", entry("bea", 3, "annabox"), false},
		{"the instance owner's own note on a free name", entry("robin", 1, "newbox"), true},
		{"a legacy entry, same name in other case", entry("ROBIN", 0, "newbox"), true},
		{"a legacy entry whose label points to no account", entry("claude", 0, "newbox"), true},
		{"the same, on a machine somebody else owns", entry("claude", 0, "beabox"), false},
		{"legacy entry of Anna (name, other case) on her machine", entry("ANNA", 0, "annabox"), true},
		{"legacy entry under her old name", entry("Annie", 0, "annabox"), true},
		{"legacy entry under an ambiguous name", entry("dup", 0, "annabox"), false},
		{"no author at all (distillate)", entry("", 0, "beabox"), true},
	} {
		if got := delivers(tc.k); got != tc.want {
			t.Errorf("%s: delivers = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A client cannot choose its author: the account is not part of the JSON.
	if id, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "x", Body: "b", Person: "robin", Scope: scope.Axes{Machine: "m"}}); err != nil || st.knowledgeAuthorID(id) != 0 {
		t.Errorf("an entry without AuthorID got an author row: %v", err)
	}
}
