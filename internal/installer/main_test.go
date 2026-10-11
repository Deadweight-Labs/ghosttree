package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests build their fake binaries below the temp directory, which the real
// resolver refuses; the dedicated tests switch the rule back on. HOME and the
// XDG directories point into a throwaway directory, so no test touches the
// real ~/.config/ghosttree.
func TestMain(m *testing.M) {
	transientRoots = func() []string { return nil }
	dir, err := os.MkdirTemp("", "installer-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(dir, ".local", "share"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(dir, ".local", "state"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
