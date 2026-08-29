package store

import (
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesPrivateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	assertPrivateDatabase(t, path)
}

func TestOpenTightensExistingDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	assertPrivateDatabase(t, path)
}

func TestPrepareDatabaseFilesTightensExistingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	assertPrivateDatabase(t, path)
}

func assertPrivateDatabase(t *testing.T, path string) {
	t.Helper()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(file), got)
		}
	}
}

func TestPersonRoundtrip(t *testing.T) {
	s := openTest(t)
	tok, err := s.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}
	name, ok := s.Authenticate(tok)
	if !ok || name != "alice" {
		t.Errorf("Authenticate = %q, %v", name, ok)
	}
	if _, ok := s.Authenticate("deadbeef"); ok {
		t.Error("bogus token must not authenticate")
	}
	if _, err := s.AddPerson("alice"); err == nil {
		t.Error("duplicate person must error")
	}
}

func TestTouchMachine(t *testing.T) {
	s := openTest(t)
	s.TouchMachine("workstation-a")
	s.TouchMachine("workstation-a") // idempotent, updates last_seen
}
