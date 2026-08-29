package privatefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSyncedNoFollowRejectsStaticSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "INDEX.md")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := WriteSyncedNoFollow(path, []byte("replacement"), 0o600); err == nil {
		t.Fatal("symlink destination accepted")
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "target" {
		t.Fatalf("symlink target changed: %q, %v", b, err)
	}
}

func TestWriteSyncedNoFollowRejectsStaticSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkedDir := filepath.Join(root, "linked")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteSyncedNoFollow(filepath.Join(linkedDir, "INDEX.md"), []byte("index"), 0o600); err == nil {
		t.Fatal("symlinked destination directory accepted")
	}
	if _, err := os.Stat(filepath.Join(realDir, "INDEX.md")); !os.IsNotExist(err) {
		t.Fatalf("write escaped through directory symlink: %v", err)
	}
}

func TestWriteSyncedNoFollowFaultsPreserveOldFileAndCleanTemp(t *testing.T) {
	faults := []string{"write", "sync", "close", "replace", "dir-sync"}
	for _, fault := range faults {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "INDEX.md")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			restore := installWriteFaultForTest(fault, errors.New("injected "+fault))
			defer restore()
			if err := WriteSyncedNoFollow(path, []byte("new"), 0o600); err == nil {
				t.Fatal("fault was not reported")
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "old" {
				t.Fatalf("old file not preserved: %q, %v", b, err)
			}
			matches, err := filepath.Glob(filepath.Join(dir, ".INDEX.md.tmp-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary files remain: %v", matches)
			}
		})
	}
}

func TestWriteSyncedNoFollowUsesRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "INDEX.md")
	if err := WriteSyncedNoFollow(path, []byte("index"), fs.FileMode(0o640)); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "index", 0o640)
}

func installWriteFaultForTest(operation string, injected error) func() {
	writeOpsMu.Lock()
	original := writeOps
	switch operation {
	case "write":
		writeOps.write = func(*os.File, []byte) error { return injected }
	case "sync":
		writeOps.sync = func(*os.File) error { return injected }
	case "close":
		writeOps.close = func(f *os.File) error { _ = f.Close(); return injected }
	case "replace":
		writeOps.replace = func(string, string) error { return injected }
	case "dir-sync":
		writeOps.dirSync = func(string) error { return injected }
	default:
		writeOpsMu.Unlock()
		panic("unknown write fault: " + operation)
	}
	writeOpsMu.Unlock()
	return func() {
		writeOpsMu.Lock()
		writeOps = original
		writeOpsMu.Unlock()
	}
}

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

func TestWritePreservesLegacySymlinkedDirectoryBehavior(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkedDir := filepath.Join(root, "linked")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(linkedDir, "config"), []byte("legacy")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(realDir, "config"), "legacy", 0o600)
}

func TestWritePreservesLegacySymlinkDestinationReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	output := filepath.Join(dir, "output")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, output); err != nil {
		t.Fatal(err)
	}
	if err := Write(output, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, output, "replacement", 0o600)
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "target" {
		t.Fatalf("symlink target changed: %q, %v", b, err)
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
