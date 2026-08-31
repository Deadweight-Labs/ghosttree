package storebench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentSQLiteReadOperationsValidateRetrievedContent(t *testing.T) {
	checks := []struct {
		kind   Kind
		mutate string
	}{
		{GhostRead, `UPDATE ghost_files SET description='corrupt'`},
		{GhostTree, `UPDATE ghost_files SET path='outside/tree.go'`},
		{DocumentRead, `UPDATE document_revisions SET digest='corrupt'`},
		{MigrationRead, `UPDATE migration_artifacts SET digest='corrupt'`},
		{SessionRead, `UPDATE session_chunks SET text='corrupt'`},
	}
	for _, check := range checks {
		t.Run(string(check.kind), func(t *testing.T) {
			workload, _ := Generate(71, Scale{
				Projects: 1, Sessions: 1, ChunksPerSession: 2, GhostFiles: 1,
				GhostBodyBytes: 32, Documents: 1, DocumentRevisions: 1,
				DocumentBodyBytes: 64, MigrationArtifacts: 2, ReadEvery: 1,
			})
			backend, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "ghosttree.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			current := backend.(*currentSQLite)
			ctx := context.Background()
			var read Operation
			for _, operation := range workload.Operations {
				if operation.Kind == check.kind {
					read = operation
					continue
				}
				if readKind(operation.Kind) {
					continue
				}
				if err := backend.Execute(ctx, operation); err != nil {
					t.Fatalf("prepare %s: %v", operation.Kind, err)
				}
			}
			if _, err := current.store.DB().Exec(check.mutate); err != nil {
				t.Fatal(err)
			}
			if err := backend.Execute(ctx, read); err == nil {
				t.Fatalf("%s accepted corrupt read result", check.kind)
			}
		})
	}
}

func TestCurrentSQLiteExecutesAndVerifiesGeneratedWorkload(t *testing.T) {
	workload, expected := Generate(7, Scale{
		Projects: 1, Sessions: 3, ChunksPerSession: 4, GhostFiles: 6,
		GhostBodyBytes: 128, Documents: 2, DocumentRevisions: 3,
		DocumentBodyBytes: 256, MigrationArtifacts: 5, ReadEvery: 2,
	})
	backend, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx := context.Background()
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatalf("%s %s: %v", operation.ID, operation.Kind, err)
		}
	}
	if err := backend.Verify(ctx, expected); err != nil {
		t.Fatal(err)
	}
	if stats := backend.Stats(); stats.MaxOpenConnections != 1 || stats.DatabaseBytes == 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestCurrentSQLiteReportsPreciseVerificationMismatch(t *testing.T) {
	workload, expected := Generate(8, Scale{Projects: 1, GhostFiles: 1, GhostBodyBytes: 32})
	backend, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx := context.Background()
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	var logicalKey string
	for key, ghost := range expected.Ghosts {
		logicalKey = key
		ghost.DescriptionDigest = strings.Repeat("0", 64)
		expected.Ghosts[key] = ghost
		break
	}
	err = backend.Verify(ctx, expected)
	if err == nil || !strings.Contains(err.Error(), logicalKey) || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("error = %v, want key and digest mismatch", err)
	}
}

func TestCurrentSQLiteRetriesRemainIdempotent(t *testing.T) {
	workload, expected := Generate(9, Scale{Projects: 1, Sessions: 2, ChunksPerSession: 3, MigrationArtifacts: 4})
	backend, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "ghosttree.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx := context.Background()
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatal(err)
		}
		if operation.Kind == ChunksAppend || operation.Kind == MigrationBegin {
			if err := backend.Execute(ctx, operation); err != nil {
				t.Fatalf("retry %s: %v", operation.Kind, err)
			}
		}
	}
	if err := backend.Verify(ctx, expected); err != nil {
		t.Fatal(err)
	}
}
