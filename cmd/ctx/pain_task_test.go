package main

import (
	"bytes"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func painTaskRun(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, &out)
	return code, out.String()
}

func TestPainCLI(t *testing.T) {
	st := moveCLIFixture(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Lesen in Teilen", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	// Nur ein Titel; --project, weil das Arbeitsverzeichnis des Tests kein Repo ist.
	code, out := painTaskRun(t, "pain", "add", "Lesen geht auch nur ganz", "--project", cliHere, "--motivates", "REQ-1", "--motivates", "https://example.invalid/issue/3")
	if code != 0 || !strings.Contains(out, "PAIN-1 [active] Lesen geht auch nur ganz") || !strings.Contains(out, "REQ-1 Lesen in Teilen") {
		t.Fatalf("add: %d %s", code, out)
	}
	var requests int
	st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requests)
	if requests != int(d.Request.ID) {
		t.Errorf("a pain created a request")
	}
	if code, out := painTaskRun(t, "pain", "state", "1", "mitigated", "--evidence", "commit:abc123"); code == 0 || !strings.Contains(out, "what remains") {
		t.Errorf("mitigated without remaining: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "state", "PAIN-1", "mitigated", "--remaining", "Lesen in Teilen fehlt", "--evidence", "request:1"); code != 0 || !strings.Contains(out, "[mitigated]") || !strings.Contains(out, "remaining: Lesen in Teilen fehlt") {
		t.Errorf("mitigated: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "remaining: Lesen in Teilen fehlt") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "edit", "1", "--title", "Neu", "--reason", "Tippfehler"); code != 0 || !strings.Contains(out, "Neu") {
		t.Errorf("edit: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "unlink", "1"); code == 0 {
		t.Errorf("unlink without reason: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "unlink", "1", "--reason", "falsch"); code != 0 {
		t.Errorf("unlink: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "show", "1", "--json"); code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Errorf("show json: %d %s", code, out)
	}
	if code, _ := painTaskRun(t, "pain"); code != 2 {
		t.Errorf("bare pain: %d", code)
	}
}

func TestTaskCLI(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Zahlung", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	add := func(extra ...string) (int, string) {
		return painTaskRun(t, append([]string{"task", "add", "--project", cliHere, "--request", "REQ-1"}, extra...)...)
	}
	if code, out := add("--kind", "secret", "--title", "Key anlegen", "--where", "Agent Vault: stripe/prod", "--check", "Eintrag existiert"); code != 0 || !strings.Contains(out, "TASK-1") || !strings.Contains(out, "-> Agent Vault: stripe/prod") {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := add("--kind", "secret", "--title", "ohne Ort"); code == 0 || !strings.Contains(out, "location") {
		t.Errorf("secret without a place: %d %s", code, out)
	}
	if code, out := add("--kind", "input", "--title", "Key sk-"+strings.Repeat("a1", 12)); code == 0 || !strings.Contains(out, "secret") {
		t.Errorf("secret text: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "task", "add", "--project", cliHere, "--kind", "input", "--title", "ohne Bindung"); code == 0 || !strings.Contains(out, "request_start_work") {
		t.Errorf("no binding: %d %s", code, out)
	}
	if code, _ := add("--kind", "action", "--title", "Handgriff"); code != 0 {
		t.Fatal("second task")
	}
	if code, out := painTaskRun(t, "task", "done", "1", "2", "--note", "erledigt"); code != 0 || strings.Count(out, "done") < 2 {
		t.Errorf("done: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "task", "reopen", "2"); code == 0 || !strings.Contains(out, "reason") {
		t.Errorf("reopen without reason: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "task", "confirm", "TASK-1"); code != 0 || !strings.Contains(out, "confirmed") {
		t.Errorf("confirm: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "task", "list", "--project", cliHere, "--state", "all"); code != 0 || !strings.Contains(out, "TASK-2") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "task", "drop", "2", "--reason", "nicht nötig"); code != 0 || !strings.Contains(out, "dropped") {
		t.Errorf("drop: %d %s", code, out)
	}
}
