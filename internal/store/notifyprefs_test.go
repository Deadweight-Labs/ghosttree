package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestNotifySwitchesAreStoredPerAccountAndDefaultToOn(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("gus"); err != nil {
		t.Fatal(err)
	}
	if off, err := st.NotifyOff("person:1"); err != nil || len(off) != 0 {
		t.Fatalf("a new account has nothing off: %v %v", off, err)
	}
	for _, k := range []string{NotifyTask, NotifyGardener, NotifyTask} {
		if err := st.SetNotify("person:1", k, false); err != nil {
			t.Fatal(err)
		}
	}
	off, _ := st.NotifyOff("person:1")
	if len(off) != 2 || !off[NotifyTask] || !off[NotifyGardener] {
		t.Fatalf("off: %v", off)
	}
	if other, _ := st.NotifyOff("person:2"); len(other) != 0 {
		t.Fatalf("another account's switches moved: %v", other)
	}
	if err := st.SetNotify("person:1", NotifyTask, true); err != nil {
		t.Fatal(err)
	}
	if off, _ := st.NotifyOff("person:1"); len(off) != 1 || off[NotifyTask] {
		t.Fatalf("on again: %v", off)
	}
	for name, c := range map[string][2]string{"unknown kind": {"person:1", "weather"}, "no account": {"person:99", NotifyTask}, "not a person": {"agent:7", NotifyTask}} {
		if err := st.SetNotify(c[0], c[1], false); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		}
	}
	if off, err := st.NotifyOff("agent:7"); err != nil || len(off) != 0 {
		t.Errorf("a session without an account has nothing off: %v %v", off, err)
	}
}

func TestNotifySchemaIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNotify("person:1", NotifyTask, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DROP TABLE account_notify_off`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		if off, err := st.NotifyOff("person:1"); err != nil || len(off) != 0 {
			t.Fatalf("open %d: %v %v", round, off, err)
		}
		st.Close()
	}
}
