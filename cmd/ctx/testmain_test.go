package main

import (
	"errors"
	"os"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/testtmp"
)

// The test binary is not the ctx binary: configuration written by tests keeps
// the bare command unless a test sets the resolver itself, and no test starts a
// real service.
func TestMain(m *testing.M) {
	installCtxExecutable = func(string) (string, error) { return "ctx", nil }
	// A developer's terminal must never be asked a question by a test.
	installTTY = func() (terminal, error) { return nil, os.ErrNotExist }
	dbTTY = func() (terminal, error) { return nil, os.ErrNotExist }
	// The test binary must never start itself as `ctx hook refresh-files`. With
	// no process to hand the files to, a session start writes them itself.
	startProjectionRefresh = func(string) error { return errors.New("no ctx binary in tests") }
	// What hooks remember between calls follows the HOME a test sets, never the
	// state directory of the developer who runs the tests.
	_ = os.Unsetenv("XDG_STATE_HOME")
	os.Exit(testtmp.Run(m))
}
