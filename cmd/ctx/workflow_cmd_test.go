package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// `ctx workflow trigger` is the operator's way to switch a workflow on until
// workflows have pages of their own (REQ-693, review of E0, finding 9).
func TestWorkflowTriggerSwitchesAWorkflowOnAndOff(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ghosttree.db")
	const project = "github.com/alpha/app"
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", project, "alpha"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	do := func(want int, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := run(append([]string{"workflow", "trigger", "--db", db}, args...), &out); code != want {
			t.Fatalf("%v: exit=%d, want %d\n%s", args, code, want, out.String())
		}
		return out.String()
	}
	triggers := func() []store.WorkflowTrigger {
		t.Helper()
		s, err := store.Open(db)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ts, err := s.WorkflowTriggersOf(project)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}

	if out := do(0, "--project", project, "--workflow", "ghosttree/echo", "--on", "pr.merged"); !strings.Contains(out, "ghosttree/echo on for pr.merged") {
		t.Fatalf("switching on: %s", out)
	}
	if ts := triggers(); len(ts) != 1 || !ts[0].Enabled || ts[0].EventKind != store.EventPRMerged || ts[0].CreatedBy != "operator" {
		t.Fatalf("trigger: %+v", ts)
	}
	do(0, "--project", project, "--workflow", "ghosttree/echo", "--on", "idea.created", "--chain")
	do(0, "--project", project, "--workflow", "ghosttree/echo", "--on", "schedule", "--every", "60", "--branch", "*")
	if ts := triggers(); len(ts) != 3 {
		t.Fatalf("three triggers: %+v", ts)
	}
	if out := do(0, "--project", project, "--workflow", "ghosttree/echo", "--on", "pr.merged", "--off"); !strings.Contains(out, "off") {
		t.Fatalf("switching off: %s", out)
	}
	for _, tr := range triggers() {
		if tr.EventKind == store.EventPRMerged && tr.Enabled {
			t.Fatalf("still on: %+v", tr)
		}
		if tr.EventKind == store.EventIdeaCreated && !tr.AllowChain {
			t.Fatalf("--chain lost: %+v", tr)
		}
	}

	// What is refused says why and exits 1; what is missing prints the usage and exits 2.
	if out := do(1, "--project", project, "--workflow", "ghosttree/echo", "--on", "idea.discussion_closed"); !strings.Contains(out, "E3") {
		t.Fatalf("a trigger on a discussion that closes: %s", out)
	}
	if out := do(1, "--project", project, "--workflow", "ghosttree/echo", "--on", "schedule", "--every", "1"); !strings.Contains(out, "5 minutes") {
		t.Fatalf("a schedule every minute: %s", out)
	}
	if out := do(1, "--project", "github.com/nobody/nothing", "--workflow", "ghosttree/echo", "--on", "pr.merged"); !strings.Contains(out, "unknown project") {
		t.Fatalf("an unknown project: %s", out)
	}
	if out := do(1, "--project", project, "--workflow", "Not A Name", "--on", "pr.merged"); !strings.Contains(out, "invalid workflow trigger") {
		t.Fatalf("a bad name: %s", out)
	}
	if out := do(2, "--project", project, "--workflow", "ghosttree/echo"); !strings.Contains(out, "usage: ctx workflow trigger") {
		t.Fatalf("missing --on: %s", out)
	}
	var out bytes.Buffer
	if code := run([]string{"workflow"}, &out); code != 2 || !strings.Contains(out.String(), "usage:") {
		t.Fatalf("without a subcommand: %d %s", code, out.String())
	}
}

// AC-695.6: ctx workflow check names unknown keys, kinds and blocks as errors
// with their line, says what the file would read, write and cost, exits 1 on
// errors and 0 on a good file; --offline calls no server and no model.
func TestWorkflowCheckReportsErrorsAndWhatAWorkflowMayDo(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(bad, []byte("---\non: pr.merged\nuses: ghosttree/echo\nretries: 2\nresults:\n  poems: 1\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := run([]string{"workflow", "check", bad, "--offline"}, &out); code != 1 {
		t.Fatalf("bad file: exit %d\n%s", code, out.String())
	}
	for _, want := range []string{"error: line 4: unknown key retries", "unknown kind of result poems", "aliases: not checked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	good := filepath.Join(dir, "good.md")
	if err := os.WriteFile(good, []byte("---\non: pr.merged\nbranches: [main]\ninputs: [pr.title]\nuses: ghosttree/echo\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"workflow", "check", "--offline", good}, &out); code != 0 {
		t.Fatalf("good file: exit %d\n%s", code, out.String())
	}
	for _, want := range []string{"ok\n", "on: pr.merged on main", "reads: pr.title", "writes: nothing", "costs at most: 0 USD"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}
