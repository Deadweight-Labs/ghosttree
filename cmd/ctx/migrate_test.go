package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/migrate"
)

func TestValidateDocumentArtifactsRejectsInvalidUTF8BeforeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.md")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateDocumentArtifacts([]migrate.Artifact{{Path: path, Rel: "docs/invalid.md", Kind: "other"}})
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateDocumentArtifactsRejectsSlugCollisions(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one.md")
	two := filepath.Join(root, "two.md")
	if err := os.WriteFile(one, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateDocumentArtifacts([]migrate.Artifact{
		{Path: one, Rel: "docs/specs/design.md", Kind: "spec"},
		{Path: two, Rel: "docs/plans/design.md", Kind: "plan"},
	})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestDocumentOnlyMigrationDoesNotLoadLLMConfig(t *testing.T) {
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	model, err := migrationModel(false)
	if err != nil || model != nil {
		t.Fatalf("migrationModel(false) = %T, %v", model, err)
	}
}
