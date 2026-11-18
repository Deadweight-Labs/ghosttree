package plainfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMakesPrivateFilesAndDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "edit", "specs")
	path := filepath.Join(dir, "a.md")
	if err := Write(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want %v", p, fi, err, want)
		}
	}
}
