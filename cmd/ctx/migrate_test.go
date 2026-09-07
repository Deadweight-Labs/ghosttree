package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/migrate"
)

func TestMigrationDryRunReportsCoverageWithoutConfigOrModel(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	for _, rel := range []string{"CLAUDE.md", "docs/ENGINEERING.md", "docs/qa/25-fixtures-and-gotchas.md", "CONTRIBUTING.md", ".ghosttree/edit/plans/ignored.md"} {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inventory := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		if err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				result[p] = string(raw)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := inventory()
	var out bytes.Buffer
	if code := cmdMigrate([]string{"--dry-run", repo}, &out); code != 0 {
		t.Fatalf("dry-run = %d: %s", code, &out)
	}
	for _, want := range []string{"candidate rules: CLAUDE.md", "candidate document (other): docs/ENGINEERING.md", "candidate document (other): docs/qa/25-fixtures-and-gotchas.md", "skipped CONTRIBUTING.md:", "unscanned .ghosttree:", "local inventory", "No writes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, &out)
		}
	}
	after := inventory()
	if len(after) != len(before) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for p, raw := range before {
		if after[p] != raw {
			t.Errorf("dry-run changed %s", p)
		}
	}
}

func TestMigrationDryRunShowsExclusionsEvenWhenDocumentsFailPreflight(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.Mkdir(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "invalid.md"), []byte{0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CONTRIBUTING.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdMigrate([]string{"--dry-run", repo}, &out); code != 1 || !strings.Contains(out.String(), "not valid UTF-8") || !strings.Contains(out.String(), "skipped CONTRIBUTING.md:") {
		t.Fatalf("dry-run = %d: %s", code, &out)
	}
}

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
