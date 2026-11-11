package main

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestParsePackageRequests(t *testing.T) {
	got, err := parsePackageRequests([]string{"596", "REQ-597:optional", "req-598:PFLICHT"})
	if err != nil || len(got) != 3 || got[0].RequestID != 596 || got[1].Role != "optional" || got[1].RequestID != 597 || got[2].Role != "pflicht" {
		t.Fatalf("got %+v %v", got, err)
	}
	for _, bad := range []string{"abc", "0", "5:wichtig", ""} {
		if _, err := parsePackageRequests([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// ctx package create|add|remove|set|show|import-relations gegen einen echten
// Server. Das Arbeitsverzeichnis des Tests ist kein Repo des Projekts, deshalb
// stehen --project und --package überall dort, wo sonst der Branch gilt.
func TestPackageCLI(t *testing.T) {
	st := moveCLIFixture(t)
	for _, title := range []string{"Eins", "Zwei", "Drei"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Flags hinter den Requests, und --request-Nummern als bloße Argumente.
	code, out := painTaskRun(t, "package", "create", "--project", cliHere, "--branch", "feat/cli", "--name", "CLI-Paket", "1", "2:optional")
	if code != 0 || !strings.Contains(out, `PKG-1 "CLI-Paket" [open]`) || !strings.Contains(out, "REQ-1 [open] Eins") || !strings.Contains(out, "REQ-2 [open] Zwei") {
		t.Fatalf("create: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "create", "--project", cliHere, "--branch", "feat/cli"); code == 0 || !strings.Contains(out, "already has the open work package PKG-1") {
		t.Errorf("second package on a branch: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "add", "--package", "1", "3", "--reason", "gehört dazu"); code != 0 || !strings.Contains(out, "REQ-3 [open] Drei") {
		t.Errorf("add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "remove", "--package", "1", "3"); code == 0 || !strings.Contains(out, "--reason") {
		t.Errorf("remove without a reason: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "remove", "3", "--package", "1", "--reason", "anderer Branch"); code != 0 || strings.Contains(out, "REQ-3") {
		t.Errorf("remove: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "set", "--package", "1", "--pr", "42"); code != 0 || !strings.Contains(out, "PR #42") {
		t.Errorf("set: %d %s", code, out)
	}
	for _, args := range [][]string{{"--package", "1"}, {"--branch", "feat/cli", "--project", cliHere}, {"--pr", "42", "--project", cliHere}} {
		code, out := painTaskRun(t, append([]string{"package", "show"}, args...)...)
		if code != 0 || !strings.Contains(out, "PKG-1") || !strings.Contains(out, "REQ-2 [open] Zwei") {
			t.Errorf("show %v: %d %s", args, code, out)
		}
	}
	if code, out := painTaskRun(t, "package", "show", "--package", "1", "--review"); code != 0 || !strings.Contains(out, "Requirements list") || !strings.Contains(out, "NOT READY") {
		t.Errorf("show --review: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "show", "--package", "1", "--json"); code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Errorf("show json: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "show", "--package", "99"); code == 0 || !strings.Contains(out, "PKG-99") {
		t.Errorf("show unknown: %d %s", code, out)
	}

	// Die Übernahme mit Probelauf.
	if _, err := st.AddRequestRelation(3, requestdomain.Relation{Kind: "external", ExternalRef: "branch:" + cliHere + "@feat/alt"}, "alice"); err != nil {
		t.Fatal(err)
	}
	if code, out := painTaskRun(t, "package", "import-relations", "--project", cliHere, "--dry-run"); code != 0 || !strings.Contains(out, "REQ-3  added") || !strings.Contains(out, "dry run, nothing written") {
		t.Errorf("dry run: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "show", "--branch", "feat/alt", "--project", cliHere); code == 0 {
		t.Errorf("the dry run created a package: %s", out)
	}
	if code, out := painTaskRun(t, "package", "import-relations", "--project", cliHere); code != 0 || !strings.Contains(out, "1 added") {
		t.Errorf("import: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "package", "import-relations", "--project", cliHere); code != 0 || !strings.Contains(out, "no branch relations to import") {
		t.Errorf("second import: %d %s", code, out)
	}
	if code, _ := painTaskRun(t, "package"); code != 2 {
		t.Errorf("bare package: %d", code)
	}
}
