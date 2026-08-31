package storebench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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
