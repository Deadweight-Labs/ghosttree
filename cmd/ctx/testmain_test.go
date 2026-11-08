package main

import (
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
	os.Exit(testtmp.Run(m))
}
