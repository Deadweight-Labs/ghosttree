package storebench

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCompareArtifactsIgnoresMapIterationButNotContent(t *testing.T) {
	want := []Artifact{{Path: "a.md", Digest: "a"}, {Path: "b.md", Digest: "b"}}
	got := map[string]string{"b.md": "b", "a.md": "a"}
	if err := compareArtifacts("migration-1", got, want); err != nil {
		t.Fatal(err)
	}
	got["b.md"] = "changed"
	if err := compareArtifacts("migration-1", got, want); err == nil {
		t.Fatal("digest mismatch passed")
	}
}

func TestVerifySQLiteRejectsHistoricalDocumentBodyCorruption(t *testing.T) {
	workload, expected := Generate(1, Scale{Projects: 1, Documents: 1, DocumentRevisions: 2, DocumentBodyBytes: 16})
	backend, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, op := range workload.Operations {
		if err := backend.Execute(context.Background(), op); err != nil {
			t.Fatal(err)
		}
	}
	if err := backend.Verify(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.(*currentSQLite).store.DB().Exec(`UPDATE document_revisions SET body='CORRUPTED' WHERE revision=1`); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), expected); err == nil {
		t.Fatal("verifier accepted corrupted non-head revision body")
	} else {
		t.Log(err)
	}
}
