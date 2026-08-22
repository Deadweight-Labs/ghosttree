package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "ctx ") {
		t.Errorf("output %q should contain version string", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"nope"}, &out); code == 0 {
		t.Error("unknown command should return non-zero")
	}
}
