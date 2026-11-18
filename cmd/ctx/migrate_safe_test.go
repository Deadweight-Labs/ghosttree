package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/migrate"
)

func TestReadArtifactDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Eine Regeldatei, die auf eine fremde Datei zeigt.
	if err := os.Symlink(secret, filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	// Ein Verzeichnis, das auf ein fremdes Verzeichnis zeigt.
	if err := os.Symlink(outside, filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	art := func(rel string) migrate.Artifact {
		return migrate.Artifact{Path: filepath.Join(repo, filepath.FromSlash(rel)), Rel: rel, Kind: "rules"}
	}
	raw, perm, err := readArtifact(art("CLAUDE.md"))
	if err != nil || string(raw) != "rules" || perm != 0o644 {
		t.Fatalf("a plain file: %q %v %v", raw, perm, err)
	}
	for _, rel := range []string{"AGENTS.md", "linked/id_ed25519"} {
		raw, _, err := readArtifact(art(rel))
		if err == nil || strings.Contains(string(raw), "PRIVATE") {
			t.Fatalf("%s was read through a symlink: %q %v", rel, raw, err)
		}
	}
}
