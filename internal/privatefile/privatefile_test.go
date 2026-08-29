package privatefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := Write(path, []byte("new contents")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "new contents", 0o600)
}

func TestWriteReplacesLaxFileAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old contents that are longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "new", 0o600)
}

func TestWriteFailureLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("secret")); err == nil {
		t.Fatal("Write replaced a directory")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".secret.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain after failure: %v", matches)
	}
}

func assertFile(t *testing.T, path, want string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("mode = %04o, want %04o", got, mode)
	}
}
