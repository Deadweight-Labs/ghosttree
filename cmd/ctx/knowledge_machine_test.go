package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Decision #4349 on the command line: knowledge about a repository on a host
// carries project AND machine (--scope project --machine M); only what concerns
// the host alone is --scope machine, without a project.
func TestKnowledgeAddWithProjectAndMachineCLI(t *testing.T) {
	st := moveCLIFixture(t)
	add := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"knowledge", "add", "--type", "note", "--body", "b"}, args...), &out)
		return code, out.String()
	}
	stored := func(title string) scope.Axes {
		t.Helper()
		rows, err := st.SearchKnowledge(title, scope.Axes{}, 10)
		if err != nil || len(rows) == 0 {
			t.Fatalf("no entry %q: %v", title, err)
		}
		return rows[0].Scope
	}

	code, out := add("--title", "deploy on build-1", "--scope", "project", "--project", cliHere, "--machine", "Build-1")
	if code != 0 || !strings.Contains(out, "machine:build-1") {
		t.Fatalf("project plus machine: %d %s", code, out)
	}
	if got := stored("deploy on build-1"); got.Project != cliHere || got.Machine != "build-1" {
		t.Errorf("scope = %+v, want project and machine", got)
	}
	// "here" is this machine (mainex in the fixture).
	if code, out := add("--title", "deploy here", "--scope", "project", "--project", cliHere, "--machine", "here"); code != 0 {
		t.Fatalf("machine here: %d %s", code, out)
	}
	if got := stored("deploy here"); got.Project != cliHere || got.Machine != "mainex" {
		t.Errorf(`"here" = %+v`, got)
	}
	// Without --machine a project entry stays a plain project entry.
	if code, out := add("--title", "plain project", "--scope", "project", "--project", cliHere); code != 0 {
		t.Fatalf("project: %d %s", code, out)
	}
	if got := stored("plain project"); got.Machine != "" {
		t.Errorf("a project entry gained a machine: %+v", got)
	}
	// The machine alone: this machine by default, another with --machine.
	if code, _ := add("--title", "host users", "--scope", "machine"); code != 0 {
		t.Fatal("machine entry refused")
	}
	if got := stored("host users"); got.Project != "" || got.Machine != "mainex" {
		t.Errorf("machine entry = %+v", got)
	}
	if code, _ := add("--title", "other host users", "--scope", "machine", "--machine", "build-2"); code != 0 {
		t.Fatal("machine entry for another machine refused")
	}
	if got := stored("other host users"); got.Project != "" || got.Machine != "build-2" {
		t.Errorf("machine entry for another machine = %+v", got)
	}
	// Combinations that make no sense are refused, and nothing is written.
	for name, args := range map[string][]string{
		"machine entry with a project": {"--title", "refused one", "--scope", "machine", "--project", cliHere},
		"branch with a machine":        {"--title", "refused two", "--scope", "branch", "--machine", "build-1"},
	} {
		if code, out := add(args...); code == 0 {
			t.Errorf("%s was accepted: %s", name, out)
		}
	}
	for _, title := range []string{"refused one", "refused two"} {
		if rows, _ := st.SearchKnowledge(title, scope.Axes{}, 10); len(rows) != 0 {
			t.Errorf("a refused call stored %q", title)
		}
	}
	// The usage text tells the rule.
	var usage bytes.Buffer
	run([]string{"knowledge", "--help"}, &usage)
	if !strings.Contains(usage.String(), "project AND") {
		t.Errorf("the usage does not state the rule: %s", usage.String())
	}
}
