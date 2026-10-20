package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// `ctx account set-gardener` is how the host's operator says who the gardener is:
// it looks the account up once and stores its NUMBER.
func TestAccountSetGardenerStoresTheNumberAndNothingElse(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ghosttree.db")
	do := func(want int, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := run(append(args, "--db", db), &out); code != want {
			t.Fatalf("%v: exit=%d, want %d\n%s", args, code, want, out.String())
		}
		return out.String()
	}
	do(0, "account", "add", "alice", "--admin")
	do(0, "account", "add", "gardener")
	do(0, "account", "add", "robot")
	gardenerID := func() int64 {
		t.Helper()
		st, err := store.Open(db)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		return st.GardenerAccountID()
	}
	if got := do(0, "account", "list"); strings.Contains(got, "gardener=true") {
		t.Fatalf("an account is marked as the gardener's before one is set:\n%s", got)
	}

	// By name: looked up once, the number is stored.
	out := do(0, "account", "set-gardener", "gardener")
	if !strings.Contains(out, "person:2") || !strings.Contains(out, "(gardener)") || gardenerID() != 2 {
		t.Fatalf("by name: %q, id %d", out, gardenerID())
	}
	listed := do(0, "account", "list")
	for _, line := range strings.Split(strings.TrimSpace(listed), "\n") {
		if strings.Contains(line, "gardener=true") != strings.HasPrefix(line, "person:2\t") {
			t.Errorf("the mark is on the wrong line: %q", line)
		}
	}
	// By number, in both spellings; the earlier account is named as dropped.
	if out := do(0, "account", "set-gardener", "person:3"); !strings.Contains(out, "it was person:2 (gardener)") || gardenerID() != 3 {
		t.Errorf("by principal id: %q, id %d", out, gardenerID())
	}
	if do(0, "account", "set-gardener", "2"); gardenerID() != 2 {
		t.Errorf("by number: id %d", gardenerID())
	}
	// An administrator is accepted with a warning.
	if out := do(0, "account", "set-gardener", "alice"); !strings.Contains(out, "administrator") || gardenerID() != 1 {
		t.Errorf("an administrator: %q, id %d", out, gardenerID())
	}
	do(0, "account", "set-gardener", "gardener")

	// What cannot be an account is refused and leaves the setting as it was.
	for _, bad := range []string{"nobody", "person:99", "99", "person:", "person:0", "0"} {
		if out := do(1, "account", "set-gardener", bad); gardenerID() != 2 || out == "" {
			t.Errorf("%q: %q, id %d", bad, out, gardenerID())
		}
	}
	do(2, "account", "set-gardener")
	do(2, "account", "set-gardener", "--clear", "gardener")

	// The number is what is stored: renaming the account changes nothing.
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetOwnName("person:2", "garden-keeper"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if gardenerID() != 2 {
		t.Error("renaming the account moved the role")
	}
	if got := do(0, "account", "list"); !strings.Contains(got, "garden-keeper") || !strings.Contains(got, "gardener=true") {
		t.Errorf("list after the rename:\n%s", got)
	}
	// A name that is not a number is not looked up after the fact, either: the name
	// "gardener" is free now, and setting it by name finds nobody.
	do(1, "account", "set-gardener", "gardener")
	if gardenerID() != 2 {
		t.Error("a name that no account holds changed the setting")
	}

	// And it can be taken away again.
	if out := do(0, "account", "set-gardener", "--clear"); !strings.Contains(out, "no gardener account is set") || gardenerID() != 0 {
		t.Errorf("clear: %q, id %d", out, gardenerID())
	}
}
