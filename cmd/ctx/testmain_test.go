package main

import (
	"os"
	"testing"
)

// The test binary is not the ctx binary: configuration written by tests keeps
// the bare command unless a test sets the resolver itself, and no test starts a
// real service.
func TestMain(m *testing.M) {
	installCtxExecutable = func(string) (string, error) { return "ctx", nil }
	os.Exit(m.Run())
}
