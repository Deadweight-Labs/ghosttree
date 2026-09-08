package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func runtimeDomainStore(t *testing.T) *Store {
	t.Helper()
	cfg := DefaultWriterConfig()
	cfg.MaxOperations = 1
	s, err := OpenRuntime(filepath.Join(t.TempDir(), "domains.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestRuntimePersonAndAccessWritesUseAdmission(t *testing.T) {
	s := runtimeDomainStore(t)
	if _, err := s.AddPerson("existing"); err != nil {
		t.Fatal(err)
	}
	release := holdRuntimeWriter(t, s.writer, 0)
	if _, err := s.AddPerson("rejected"); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("person bypassed queue: %v", err)
	}
	if err := s.SetContextSnapshotAccess("existing", "github.com/example/repo", true, true, true); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("access bypassed queue: %v", err)
	}
	release()
	if _, ok := s.PrincipalByName("rejected"); ok {
		t.Fatal("rejected person persisted")
	}
	p, ok := s.PrincipalByName("existing")
	if !ok {
		t.Fatal("seed person lost")
	}
	access, err := s.ContextSnapshotAccess(p.ID, "github.com/example/repo")
	if err != nil || access.ReleaseBind {
		t.Fatalf("rejected grant persisted: %+v %v", access, err)
	}
	if err := s.SetContextSnapshotAccess("existing", "github.com/example/repo", true, true, true); err != nil {
		t.Fatal(err)
	}
	access, err = s.ContextSnapshotAccess(p.ID, "github.com/example/repo")
	if err != nil || !access.ReleaseBind {
		t.Fatalf("grant absent after ACK: %+v %v", access, err)
	}
}
