package installer

import (
	"os"
	"testing"
)

// Tests build their fake binaries below the temp directory, which the real
// resolver refuses; the dedicated tests switch the rule back on.
func TestMain(m *testing.M) {
	transientRoots = func() []string { return nil }
	os.Exit(m.Run())
}
