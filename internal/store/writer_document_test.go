package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestRuntimeDocumentAndMigrationWritesUseAdmission(t *testing.T) {
	for _, op := range []string{"create", "revision", "patch", "begin", "complete", "document_proof", "import", "migrated"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			body := "# body\r\n\t🌳\n"
			digest := Digest(body)
			doc, err := s.CreateDocument(Document{Project: "p", Slug: "existing", Kind: "spec", Title: "original"}, body, "initial")
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.BeginMigration("p", map[string]string{"docs/import.md": digest})
			if err != nil {
				t.Fatal(err)
			}
			empty, err := s.BeginMigration("empty", nil)
			if err != nil {
				t.Fatal(err)
			}
			write := func() error {
				switch op {
				case "create":
					_, err := s.CreateDocument(Document{Project: "p", Slug: "new", Kind: "spec", Title: "new"}, body, "new")
					return err
				case "revision":
					_, err := s.PushRevision(doc.ID, 1, "next", "revision", "writer")
					return err
				case "patch":
					return s.PatchDocument(doc.ID, map[string]string{"title": "edited"})
				case "begin":
					_, err := s.BeginMigration("other", map[string]string{"README.md": "sha"})
					return err
				case "complete":
					return s.CompleteMigration(empty)
				case "document_proof":
					return s.InsertDocumentMigration(run, "docs/import.md", digest, doc.ID, 1)
				case "import":
					_, err := s.ImportDocument(MigratedDocument{Document: Document{Project: "p", Slug: "imported", Kind: "spec", Title: "imported"}, RunID: run, Source: "docs/import.md", Digest: digest, Body: body})
					return err
				case "migrated":
					_, err := s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{Type: "instruction", Title: "migrated", Body: "body", Scope: scope.Axes{Project: "p"}, SessionRef: "docs/import.md"}, RunID: run, Digest: digest, ItemKey: "one"})
					return err
				}
				panic("unknown operation")
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			if err := write(); !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed admission: %v", op, err)
			}
			release()
			current, err := s.DocumentByID(doc.ID)
			if err != nil || current.Title != "original" || current.HeadRevision != 1 {
				t.Fatalf("document mutated: %+v %v", current, err)
			}
			var count int
			if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM documents)+(SELECT COUNT(*) FROM document_revisions)+(SELECT COUNT(*) FROM migration_runs)+(SELECT COUNT(*) FROM migration_evidence)+(SELECT COUNT(*) FROM knowledge)").Scan(&count); err != nil || count != 4 {
				t.Fatalf("side effects=%d err=%v", count, err)
			}
			var state string
			if err := s.db.QueryRow("SELECT state FROM migration_runs WHERE id=?", empty).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("run=%s err=%v", state, err)
			}
			if err := write(); err != nil {
				t.Fatalf("%s after release: %v", op, err)
			}
			if op == "revision" {
				if _, err := s.PushRevision(doc.ID, 1, "stale", "retry", "writer"); !errors.Is(err, ErrRevisionConflict) {
					t.Fatalf("CAS lost: %v", err)
				}
			}
			if op == "import" || op == "migrated" {
				if err := write(); err != nil {
					t.Fatalf("idempotent import retry: %v", err)
				}
				if err := s.CompleteMigration(run); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
