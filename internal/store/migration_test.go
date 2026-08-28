package store

import (
	"path/filepath"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"testing"
)

func TestMigratedActivationPersistsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := s.BeginMigration("p", map[string]string{"core/AGENTS.md": "digest"})
	saved, err := s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{
		Type: "instruction", Title: "core", Body: "b", Scope: scope.Axes{Project: "p"}, SessionRef: "core/AGENTS.md",
		Activation: activation.Rule{Paths: []string{"core/**"}},
	}, RunID: run, Digest: "digest", ItemKey: "core"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.KnowledgeByID(saved.ID)
	if err != nil || len(got.Activation.Paths) != 1 || got.Activation.Paths[0] != "core/**" {
		t.Fatalf("reloaded=%+v err=%v", got, err)
	}
	proof, err := s.MigrationEvidenceForKnowledge(saved.ID)
	if err != nil || proof.Source != "core/AGENTS.md" || proof.Digest != "digest" || proof.ItemKey != "core" {
		t.Fatalf("migration proof=%+v err=%v", proof, err)
	}
	var projected int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM search_documents WHERE kind='knowledge' AND domain_id=?`, saved.ID).Scan(&projected); err != nil || projected != 1 {
		t.Fatalf("migration projection count=%d err=%v", projected, err)
	}

	if _, err := s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{
		Type: "note", Title: "bad", Body: "b", Scope: scope.Axes{Project: "p"}, SessionRef: "core/AGENTS.md",
		Activation: activation.Rule{Paths: []string{"core/**"}},
	}, RunID: run, Digest: "digest", ItemKey: "bad"}); err == nil {
		t.Fatal("non-instruction activation must fail")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE title='bad'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed transaction left knowledge row: count=%d err=%v", count, err)
	}
}

func TestMigratedRequestUsesRequestDomain(t *testing.T) {
	s := openTest(t)
	run, err := s.BeginMigration("p", map[string]string{"PLAN.md": "digest"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{
		Type: "request", Title: "ship ledger", Body: "Make requests usable", Scope: scope.Axes{Project: "p"}, SessionRef: "PLAN.md",
	}, RunID: run, Digest: "digest", ItemKey: "request-1", RequestState: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 || saved.Kind != "request" {
		t.Fatalf("saved = %+v", saved)
	}
	detail, err := s.RequestByID(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Request.Title != "ship ledger" || detail.Request.State != "open" || detail.Request.Origin != "distilled" {
		t.Fatalf("request = %+v", detail.Request)
	}
	var knowledgeCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE title='ship ledger'`).Scan(&knowledgeCount); err != nil || knowledgeCount != 0 {
		t.Fatalf("request leaked into knowledge: count=%d err=%v", knowledgeCount, err)
	}
	var evidenceRequestID int64
	if err := s.db.QueryRow(`SELECT request_id FROM migration_evidence WHERE item_key='request-1'`).Scan(&evidenceRequestID); err != nil || evidenceRequestID != saved.ID {
		t.Fatalf("migration evidence request=%d err=%v", evidenceRequestID, err)
	}
	if err := s.CompleteMigration(run); err != nil {
		t.Fatal(err)
	}
	retried, err := s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{
		Type: "request", Title: "ship ledger", Body: "Make requests usable", Scope: scope.Axes{Project: "p"}, SessionRef: "PLAN.md",
	}, RunID: run, Digest: "digest", ItemKey: "request-1", RequestState: "open"})
	if err != nil || retried.ID != saved.ID {
		t.Fatalf("idempotent retry = %+v, %v", retried, err)
	}
}

func TestOnlyCompletedMigrationAuthorizesExactDigest(t *testing.T) {
	s := openTest(t)
	pending, err := s.BeginMigration("p", map[string]string{"CLAUDE.md": "old"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.CompletedMigrationArtifacts("p")
	if len(got) != 0 {
		t.Fatalf("pending run visible: %v", got)
	}
	if err := s.CompleteMigration(pending); err == nil {
		t.Fatal("run without artifact evidence completed")
	}
	_, err = s.InsertMigrated(MigratedEntry{Knowledge: Knowledge{Type: "instruction", Title: "build", Body: "make test", Scope: scope.Axes{Project: "p"}, SessionRef: "CLAUDE.md"}, RunID: pending, Digest: "old", Quote: "make test", ItemKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteMigration(pending); err != nil {
		t.Fatal(err)
	}
	got, err = s.CompletedMigrationArtifacts("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(got["CLAUDE.md"]) != 1 || got["CLAUDE.md"][0] != "old" {
		t.Fatalf("completed artifacts=%v", got)
	}
	for _, digest := range got["CLAUDE.md"] {
		if digest == "changed" {
			t.Error("changed content was authorized")
		}
	}
}

func TestDocumentMigrationEvidenceRequiresExactStoredRevision(t *testing.T) {
	s := openTest(t)
	body := "# Design\r\n\ttab 🌳\n"
	document, err := s.CreateDocument(Document{Project: "p", Slug: "design", Kind: "spec", Title: "Design"}, body, "import")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.BeginMigration("p", map[string]string{"docs/design.md": Digest(body)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDocumentMigration(run, "docs/design.md", Digest(body), document.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteMigration(run); err != nil {
		t.Fatal(err)
	}
	proven, err := s.CompletedDocumentArtifacts("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(proven["docs/design.md"]) != 1 || proven["docs/design.md"][0] != Digest(body) {
		t.Fatalf("proof = %+v", proven)
	}
	if proven["docs/design.md"][0] == Digest("changed") {
		t.Fatal("a different file digest was authorized")
	}
	if _, err := s.db.Exec(`DELETE FROM document_revisions WHERE document_id=? AND revision=1`, document.ID); err != nil {
		t.Fatal(err)
	}
	proven, err = s.CompletedDocumentArtifacts("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(proven) != 0 {
		t.Fatalf("proof survived missing revision: %+v", proven)
	}
}

func TestDocumentMigrationEvidenceRejectsMismatchedRevision(t *testing.T) {
	s := openTest(t)
	document, err := s.CreateDocument(Document{Project: "p", Slug: "design", Kind: "spec", Title: "Design"}, "stored", "import")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.BeginMigration("p", map[string]string{"docs/design.md": Digest("source")})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDocumentMigration(run, "docs/design.md", Digest("source"), document.ID, 1); err == nil {
		t.Fatal("evidence accepted a revision with different bytes")
	}
	if err := s.CompleteMigration(run); err == nil {
		t.Fatal("run completed without valid revision evidence")
	}
}

func TestImportDocumentIsAtomicAndIdempotent(t *testing.T) {
	s := openTest(t)
	body := "# Design\r\n\ttab 🌳\n"
	run, err := s.BeginMigration("p", map[string]string{"docs/design.md": Digest(body)})
	if err != nil {
		t.Fatal(err)
	}
	in := MigratedDocument{
		Document: Document{Project: "p", Slug: "design", Kind: "spec", Title: "Design", Person: "alice"},
		RunID:    run, Source: "docs/design.md", Digest: Digest(body), Body: body, Message: "import",
	}
	first, err := s.ImportDocument(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ImportDocument(in)
	if err != nil || second.ID != first.ID {
		t.Fatalf("retry = %+v, %v; first = %+v", second, err, first)
	}
	var documents, revisions, evidence int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM document_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM migration_evidence`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || revisions != 1 || evidence != 1 {
		t.Fatalf("rows after retry: documents=%d revisions=%d evidence=%d", documents, revisions, evidence)
	}
	if err := s.CompleteMigration(run); err != nil {
		t.Fatal(err)
	}

	badRun, err := s.BeginMigration("p", map[string]string{"docs/bad.md": Digest("expected")})
	if err != nil {
		t.Fatal(err)
	}
	bad := MigratedDocument{
		Document: Document{Project: "p", Slug: "bad", Kind: "spec", Title: "Bad"},
		RunID:    badRun, Source: "docs/bad.md", Digest: Digest("expected"), Body: "different",
	}
	if _, err := s.ImportDocument(bad); err == nil {
		t.Fatal("import accepted body whose digest differs from the artifact")
	}
	if _, err := s.DocumentBySlug("p", "bad"); err == nil {
		t.Fatal("failed import left a document behind")
	}
}

func TestBeginMigrationReusesMatchingPendingRun(t *testing.T) {
	s := openTest(t)
	artifacts := map[string]string{"docs/a.md": "one", "docs/b.md": "two"}
	first, err := s.BeginMigration("p", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginMigration("p", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("matching retry created run %d; want pending run %d", second, first)
	}
	different, err := s.BeginMigration("p", map[string]string{"docs/a.md": "changed"})
	if err != nil {
		t.Fatal(err)
	}
	if different == first {
		t.Fatal("different artifacts reused the pending run")
	}
}
