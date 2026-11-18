package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/mirror"
)

// A repository that brings .ghosttree/INDEX.md or .ghosttree itself as a link
// must not make the mirror write or delete anything outside the checkout.
func TestWriteMirrorDocsDoesNotFollowLinksTheRepoBrought(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "bashrc")
	keep := filepath.Join(outside, "docs", "keep.md")
	for p, body := range map[string]string{victim: "export A=1\n", keep: "mine"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs := []mirror.Doc{{Path: "INDEX.md", Body: "index"}, {Path: "docs/specs/s.md", Body: "d"}, {Path: "knowledge/note/1.md", Body: "k"}}

	t.Run("INDEX.md is a link to a file", func(t *testing.T) {
		repo := gitRepo(t)
		root := filepath.Join(repo, ".ghosttree")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(root, "INDEX.md")); err != nil {
			t.Fatal(err)
		}
		if err := writeMirrorDocs(root, repo, docs); err == nil {
			t.Fatal("a link as INDEX.md must be refused")
		}
		if b, _ := os.ReadFile(victim); string(b) != "export A=1\n" {
			t.Fatalf("the file behind the link was written: %q", b)
		}
	})

	t.Run(".ghosttree is a link to a directory", func(t *testing.T) {
		repo := gitRepo(t)
		root := filepath.Join(repo, ".ghosttree")
		if err := os.Symlink(outside, root); err != nil {
			t.Fatal(err)
		}
		if err := writeMirrorDocs(root, repo, docs); err == nil {
			t.Fatal(".ghosttree as a link must be refused")
		}
		if b, _ := os.ReadFile(keep); string(b) != "mine" {
			t.Fatalf("the directory behind the link was touched: %q", b)
		}
		if _, err := os.Stat(filepath.Join(outside, "INDEX.md")); !os.IsNotExist(err) {
			t.Fatalf("INDEX.md was written behind the link: %v", err)
		}
	})
}
