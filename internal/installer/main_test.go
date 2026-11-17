package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/testtmp"
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
	// Claude Code's own directory follows CLAUDE_CONFIG_DIR, which a session that
	// runs the tests may have set to the real configuration (PAIN-86, KN-4752).
	// Tests that need it set it themselves.
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("CODEX_HOME")
	code := testtmp.Run(m)
	os.RemoveAll(dir)
	os.Exit(code)
}
