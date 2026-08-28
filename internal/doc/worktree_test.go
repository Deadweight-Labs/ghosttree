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
