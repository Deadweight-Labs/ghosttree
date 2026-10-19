package projectlink

import (
	"os"
	"path/filepath"
	"sync"
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

// Every Set reads, changes and rewrites the whole registry; without the lock
// concurrent links (several terminals, a script) lose each other's entries.
func TestParallelSetsLoseNoLink(t *testing.T) {
	root := isolate(t)
	const n = 20
	dirs := make([]string, n)
	for i := range dirs {
		dirs[i] = filepath.Join(root, "f", string(rune('a'+i)))
		if err := os.MkdirAll(dirs[i], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, d := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Set(d, "acme/infra"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := len(List()); got != n {
		t.Errorf("%d of %d links survived", got, n)
	}
	// Removing in parallel loses nothing either.
	for _, d := range dirs[:10] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Remove(d); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := len(List()); got != n-10 {
		t.Errorf("%d links left, want %d", got, n-10)
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
