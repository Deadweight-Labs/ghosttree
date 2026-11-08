package main

import (
	"strings"
	"testing"
)

// REQ-616 am CLI: accepted verlangt eine Begründung, die Liste zeigt den Grund,
// der Rückweg verlangt eine Notiz.
func TestPainAcceptedCLI(t *testing.T) {
	moveCLIFixture(t)
	if code, out := painTaskRun(t, "pain", "add", "Es hakt manchmal", "--project", cliHere); code != 0 {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "pain", "state", "1", "accepted"); code == 0 || !strings.Contains(out, "accepted_reason_required") && !strings.Contains(out, "say why") {
		t.Errorf("accepting without a reason: %d %s", code, out)
	}
	code, out := painTaskRun(t, "pain", "state", "1", "accepted", "--note", "kostet mehr als es bringt")
	if code != 0 || !strings.Contains(out, "PAIN-1 [accepted]") || !strings.Contains(out, "accepted because: kostet mehr als es bringt") || !strings.Contains(out, "state: accepted") {
		t.Fatalf("accept: %d %s", code, out)
	}
	// Die offene Liste zeigt ihn nicht, --state accepted mit dem Grund.
	if code, out := painTaskRun(t, "pain", "list", "--project", cliHere); code != 0 || strings.Contains(out, "PAIN-1") {
		t.Errorf("the open list shows an accepted pain: %d %s", code, out)
	}
	code, out = painTaskRun(t, "pain", "list", "--state", "accepted", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "PAIN-1") || !strings.Contains(out, "accepted because: kostet mehr als es bringt") {
		t.Errorf("list accepted: %d %s", code, out)
	}
	// Als geschlossen in der Suchsprache.
	if code, out := painTaskRun(t, "search", "--project", cliHere, "is:pain", "is:closed"); code != 0 || !strings.Contains(out, "PAIN-1") {
		t.Errorf("search is:closed: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--project", cliHere, "is:pain", "is:open"); code != 0 || strings.Contains(out, "PAIN-1") {
		t.Errorf("search is:open: %d %s", code, out)
	}
	// Rückweg.
	if code, out := painTaskRun(t, "pain", "state", "1", "active"); code == 0 || !strings.Contains(out, "note_required") && !strings.Contains(out, "say why") {
		t.Errorf("back to active without a note: %d %s", code, out)
	}
	code, out = painTaskRun(t, "pain", "state", "1", "active", "--reason", "stört doch")
	if code != 0 || !strings.Contains(out, "PAIN-1 [active]") || strings.Contains(out, "accepted because") {
		t.Errorf("back to active: %d %s", code, out)
	}
	// Der Zustand steht in der Hilfe.
	if _, out := painTaskRun(t, "pain"); !strings.Contains(out, "mitigated|accepted|resolved|obsolete") {
		t.Errorf("usage lacks accepted: %s", out)
	}
}
