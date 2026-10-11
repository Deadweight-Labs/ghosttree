package projectlink

import (
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "home", ".config"))
	return dir
}

func TestSetFindRemoveAndPermissions(t *testing.T) {
	root := isolate(t)
	folder := filepath.Join(root, "infra")
	if err := os.MkdirAll(filepath.Join(folder, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(folder, "acme/infra"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("registry = %v, %v; want mode 0600", st, err)
	}
	if dir, id := Find(filepath.Join(folder, "a", "b")); dir != folder || id != "acme/infra" {
		t.Errorf("Find = %q, %q", dir, id)
	}
	if dir, _ := Find(root); dir != "" {
		t.Errorf("parent of a link resolved: %q", dir)
	}
	if list := List(); len(list) != 1 || list[0].Path != folder || list[0].Project != "acme/infra" {
		t.Errorf("List = %+v", list)
	}
	if ok, err := Remove(filepath.Join(folder, "a")); ok || err != nil {
		t.Errorf("Remove of a subfolder = %v, %v; only the linked folder itself can be unlinked", ok, err)
	}
	if ok, err := Remove(folder); !ok || err != nil {
		t.Errorf("Remove = %v, %v", ok, err)
	}
	if dir, _ := Find(folder); dir != "" {
		t.Errorf("still linked: %q", dir)
	}
}

func TestSetRefusesWhatCanNeverBeLinked(t *testing.T) {
	root := isolate(t)
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, "/"} {
		if _, err := Set(dir, "acme/infra"); err == nil {
			t.Errorf("Set(%q) succeeded", dir)
		}
	}
	if _, err := Set(filepath.Join(home, "x"), "github.com/a/b"); err == nil {
		t.Error("a git remote was linked as a named project")
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("a refused link wrote the registry: %v", err)
	}
}
