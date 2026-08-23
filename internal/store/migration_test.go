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
