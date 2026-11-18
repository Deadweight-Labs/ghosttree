package store

import (
	"path/filepath"
	"testing"
)

// A device's traffic keeps its own machine's last_seen current, and nothing
// else: not a machine that was released, not one that belongs to someone else.
func TestNoteMachineSeenOnlyTouchesTheOwnMachineAndNeverCreatesOne(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "seen.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, n := range []string{"anna", "ben"} {
		if _, err := st.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.CreateDeviceToken("person:1", "build-1"); err != nil {
		t.Fatal(err)
	}
	old := "2026-01-01T00:00:00Z"
	seen := func() string {
		t.Helper()
		var v string
		if err := st.db.QueryRow(`SELECT last_seen FROM machines WHERE hostname='build-1'`).Scan(&v); err != nil {
			return ""
		}
		return v
	}
	reset := func() {
		if _, err := st.db.Exec(`UPDATE machines SET last_seen=? WHERE hostname='build-1'`, old); err != nil {
			t.Fatal(err)
		}
	}

	reset()
	st.NoteMachineSeen("Build-1", "person:2")
	if got := seen(); got != old {
		t.Fatalf("a token of another account moved last_seen to %q", got)
	}
	st.NoteMachineSeen("Build-1", "person:1")
	if got := seen(); got <= old {
		t.Fatalf("the owner's device did not move last_seen: %q", got)
	}

	if err := st.ReleaseMachine("build-1"); err != nil {
		t.Fatal(err)
	}
	st.NoteMachineSeen("build-1", "person:1")
	if got := seen(); got != "" {
		t.Fatalf("a token that outlived its released machine brought the row back: %q", got)
	}
}
