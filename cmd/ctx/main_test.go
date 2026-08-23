package main

import (
	"bytes"
	"path/filepath"
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

func TestExportRejectsBadArguments(t *testing.T) {
	cases := map[string][]string{
		"no session id":    {"export"},
		"not a number":     {"export", "abc"},
		"too many session": {"export", "1", "2"},
	}
	for name, args := range cases {
		var out bytes.Buffer
		code := run(args, &out)
		if code == 0 {
			t.Errorf("%s: exit code = 0, want non-zero", name)
		}
		// Without this the test would pass purely because "export" is an
		// unknown command, proving nothing about argument handling.
		if got := out.String(); strings.Contains(got, "unknown command") {
			t.Errorf("%s: export is not wired up: %s", name, got)
		} else if !strings.Contains(got, "usage: ctx export") {
			t.Errorf("%s: want export usage, got %q", name, got)
		}
	}
}

// The usage line promises "ctx export <session-id> [-o file]", so flags have
// to be accepted after the id even though flag.Parse stops at the first
// positional argument.
func TestExportAcceptsFlagAfterSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	run([]string{"export", "1137", "-o", filepath.Join(home, "x.jsonl")}, &out)
	if strings.Contains(out.String(), "usage: ctx export") {
		t.Errorf("flag after session id must parse, got:\n%s", out.String())
	}
}

func TestDoctorReportsUnwiredHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if code := run([]string{"doctor"}, &out); code == 0 {
		t.Errorf("nothing is wired up, doctor must exit non-zero:\n%s", out.String())
	}
	got := out.String()
	for _, want := range []string{"claude mcp registration", "codex mcp registration", "fix:"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output missing %q:\n%s", want, got)
		}
	}
}

func TestDoctorFixInstallsHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var fixed bytes.Buffer
	run([]string{"doctor", "--fix"}, &fixed)

	var after bytes.Buffer
	run([]string{"doctor"}, &after)
	for _, name := range []string{"claude mcp registration", "codex rule section"} {
		if failed(after.String(), name) {
			t.Errorf("after --fix, %q should pass:\n%s", name, after.String())
		}
	}
}

// failed reports whether the doctor line for a check is marked FAIL.
func failed(output, checkName string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, checkName) {
			return strings.Contains(line, "FAIL")
		}
	}
	return true // check never appeared at all
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"nope"}, &out); code == 0 {
		t.Error("unknown command should return non-zero")
	}
}
