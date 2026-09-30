package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestRuntimeAdministrativeMutationsUseAdmission(t *testing.T) {
	for _, op := range []string{"canonicalize", "canonicalize_preview", "unbind", "unbind_preview"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			axes := scope.Axes{Project: "github.com/old/repo", Branch: "feature"}
			id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "seed", Body: "body", Scope: axes})
			if err != nil {
				t.Fatal(err)
			}
			write := func() error {
				switch op {
				case "canonicalize":
					_, err := s.CanonicalizeScopes(map[string]string{axes.Project: "github.com/new/repo"})
					return err
				case "canonicalize_preview":
					_, err := s.PreviewCanonicalizeScopes(map[string]string{axes.Project: "github.com/new/repo"})
					return err
				default:
					_, err := s.UnbindBranchScope([]int64{id}, op == "unbind_preview")
					return err
				}
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			if err := write(); !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed admission: %v", op, err)
			}
			release()
			k, err := s.KnowledgeByID(id)
			if err != nil || !reflect.DeepEqual(k.Scope, axes) {
				t.Fatalf("rejected operation changed scope: %+v %v", k, err)
			}
			if err := write(); err != nil {
				t.Fatal(err)
			}
			want := axes
			if op == "canonicalize" {
				want.Project = "github.com/new/repo"
			}
			if op == "unbind" {
				want.Branch = ""
			}
			k, err = s.KnowledgeByID(id)
			if err != nil || !reflect.DeepEqual(k.Scope, want) {
				t.Fatalf("committed/preview scope: %+v, want %+v: %v", k.Scope, want, err)
			}
		})
	}
}

func TestRuntimeBackupRequiresOfflineStore(t *testing.T) {
	s := runtimeDomainStore(t)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(path); err == nil {
		t.Fatal("runtime backup accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime backup created file: %v", err)
	}
	direct, err := OpenWithOptions(filepath.Join(t.TempDir(), "offline.db"), OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	if _, err := direct.InsertKnowledge(Knowledge{Type: "note", Title: "backup proof", Body: "body"}); err != nil {
		t.Fatal(err)
	}
	if err := direct.Backup(path); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenReadOnly(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var count int
	if err := copy.db.QueryRow("SELECT count(*) FROM knowledge WHERE title='backup proof'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("offline backup content: %d %v", count, err)
	}
}
