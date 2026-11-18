package ghost

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// outside legt ein Verzeichnis außerhalb des Repos an, das wie die Opferdaten
// aussieht, und gibt Pfad und Inhalt zurück.
func outside(t *testing.T) (dir string, victim string) {
	t.Helper()
	dir = t.TempDir()
	for _, d := range []string{"tree", "knowledge", "docs", "requests"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, d, "keep.md"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	victim = filepath.Join(dir, "bashrc")
	if err := os.WriteFile(victim, []byte("export A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, victim
}

func assertUntouched(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"tree", "knowledge", "docs", "requests"} {
		b, err := os.ReadFile(filepath.Join(dir, d, "keep.md"))
		if err != nil || string(b) != "mine" {
			t.Fatalf("%s/keep.md changed or gone: %q %v", d, b, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bashrc")); string(b) != "export A=1\n" {
		t.Fatalf("bashrc changed: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Fatalf("something was added outside: %v", entries)
	}
}

func TestMaterializeRefusesGhosttreeSymlink(t *testing.T) {
	repo := t.TempDir()
	dir, _ := outside(t)
	if err := os.Symlink(dir, filepath.Join(repo, ".ghosttree")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".ghosttree/tree", ".ghosttree/knowledge"} {
		err := Materialize(repo, rel, []Doc{{Path: "a.md", Body: "x"}})
		if !errors.Is(err, ErrUnsafe) {
			t.Fatalf("%s: want ErrUnsafe, got %v", rel, err)
		}
	}
	if err := WriteFile(repo, ".ghosttree/INDEX.md", []byte("x")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("INDEX.md: want ErrUnsafe, got %v", err)
	}
	assertUntouched(t, dir)
}

func TestMaterializeRefusesSymlinkedTargetAndBuildDir(t *testing.T) {
	repo := t.TempDir()
	dir, _ := outside(t)
	gt := filepath.Join(repo, ".ghosttree")
	if err := os.MkdirAll(gt, 0o700); err != nil {
		t.Fatal(err)
	}
	// Die Zielebene selbst und das Bauverzeichnis daneben sind Symlinks.
	if err := os.Symlink(filepath.Join(dir, "knowledge"), filepath.Join(gt, "knowledge")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "docs"), filepath.Join(gt, "docs.tmp")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".ghosttree/knowledge", ".ghosttree/docs"} {
		if err := Materialize(repo, rel, []Doc{{Path: "a.md", Body: "x"}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("%s: want ErrUnsafe, got %v", rel, err)
		}
	}
	assertUntouched(t, dir)
}

func TestWriteFileDoesNotWriteThroughASymlink(t *testing.T) {
	repo := t.TempDir()
	dir, victim := outside(t)
	gt := filepath.Join(repo, ".ghosttree")
	if err := os.MkdirAll(gt, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(gt, "INDEX.md")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(repo, ".ghosttree/INDEX.md", []byte("index")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}
	assertUntouched(t, dir)
}

func TestMaterializeKeepsOutsideIntactWhenSymlinkPointsToParent(t *testing.T) {
	// .ghosttree -> .. : die Ebene darüber, in der Nachbarn des Klons liegen.
	parent := t.TempDir()
	repo := filepath.Join(parent, "clone")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "docs", "keep.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, ".ghosttree")); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(repo, ".ghosttree/docs", []Doc{{Path: "a.md", Body: "x"}}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(parent, "docs", "keep.md")); err != nil || string(b) != "mine" {
		t.Fatalf("the neighbour directory was touched: %q %v", b, err)
	}
}

func TestMaterializeRefusesPathsThatLeaveTheMirror(t *testing.T) {
	repo := t.TempDir()
	for _, p := range []string{
		"../../../../.claude/commands/PAIN-1-x.md", "pains/../../x.md", "/etc/x.md",
		"pains//x.md", "a/./b.md", "", "a\x00b.md", "C:/x.md", "pains/c:/x.md",
	} {
		if err := Materialize(repo, ".ghosttree/pains", []Doc{{Path: p, Body: "x"}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("%q: want ErrUnsafe, got %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".ghosttree", "pains")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written for a refused set: %v", err)
	}
	if err := Materialize(repo, "../out", []Doc{{Path: "a.md"}}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("target outside the base: %v", err)
	}
}

func TestMaterializeWritesPrivateAndTightensAnOldMirror(t *testing.T) {
	repo := t.TempDir()
	gt := filepath.Join(repo, ".ghosttree")
	old := filepath.Join(gt, "knowledge")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "1.md"), []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Materialize(repo, ".ghosttree/knowledge", []Doc{{Path: "note/1-a.md", Body: "neu"}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(repo, ".ghosttree/INDEX.md", []byte("index")); err != nil {
		t.Fatal(err)
	}
	want := map[string]os.FileMode{
		gt:                                     0o700,
		filepath.Join(gt, "knowledge"):         0o700,
		filepath.Join(gt, "knowledge", "note"): 0o700,
		filepath.Join(gt, "knowledge", "note", "1-a.md"): 0o600,
		filepath.Join(gt, "INDEX.md"):                    0o600,
	}
	for p, mode := range want {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Fatalf("%s: mode %v, want %v", p, fi.Mode().Perm(), mode)
		}
	}
	if _, err := os.Stat(filepath.Join(gt, "knowledge.tmp")); !os.IsNotExist(err) {
		t.Fatalf("the build directory stayed: %v", err)
	}
}
