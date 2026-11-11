package main

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestIdeaCLI(t *testing.T) {
	moveCLIFixture(t)
	code, out := painTaskRun(t, "idea", "add", "Dunkles Thema", "--body", "ein Satz", "--project", cliHere)
	if code != 0 || !strings.Contains(out, "IDEA-1 [open] Dunkles Thema") || !strings.Contains(out, "ein Satz") {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "add", "dunkles thema", "--project", cliHere); code == 0 || !strings.Contains(out, "IDEA-1") {
		t.Errorf("duplicate: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "add", "dunkles thema", "--force", "--project", cliHere); code != 0 {
		t.Errorf("forced duplicate: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "IDEA-1") || !strings.Contains(out, "IDEA-2") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "dismiss", "2"); code == 0 {
		t.Errorf("dismiss without reason: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "dismiss", "IDEA-2", "--reason", "doppelt"); code != 0 || !strings.Contains(out, "[dismissed]") || !strings.Contains(out, "reason: doppelt") {
		t.Errorf("dismiss: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "list", "--state", "dismissed", "--project", cliHere); code != 0 || !strings.Contains(out, "IDEA-2") {
		t.Errorf("dismissed ideas stay searchable: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "promote", "1", "--criterion", "Umschalten geht"); code != 0 || !strings.Contains(out, "created REQ-1") || !strings.Contains(out, "[accepted]") || !strings.Contains(out, "became REQ-1") {
		t.Errorf("promote: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "is:idea idea:1", "--project", cliHere); code != 0 || !strings.Contains(out, "IDEA-1") || strings.Contains(out, "IDEA-2") {
		t.Errorf("search is:idea idea:N: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea"); code != 2 {
		t.Errorf("bare idea: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "idea", "show", "x"); code != 2 {
		t.Errorf("bad ref: %d %s", code, out)
	}
}

func TestLabelAndTopicCLI(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "bug", Title: "Kaputt", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	if code, out := painTaskRun(t, "label", "create", "bug", "--color", "#d73a4a", "--description", "kaputt", "--project", cliHere); code != 0 || !strings.Contains(out, "created bug #d73a4a") {
		t.Fatalf("create: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "create", "a,b", "--project", cliHere); code == 0 {
		t.Errorf("bad name: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "add", "request", "REQ-1", "bug"); code != 0 || !strings.Contains(out, "bug") {
		t.Errorf("add: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "bug") || !strings.Contains(out, "kaputt") {
		t.Errorf("list: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "label:bug kind:requests", "--project", cliHere); code != 0 || !strings.Contains(out, "REQ-1") {
		t.Errorf("label: search: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "-label:bug kind:requests", "--project", cliHere); code != 0 || strings.Contains(out, "REQ-1") {
		t.Errorf("-label: search: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "flavor:x", "--project", cliHere); code == 0 || !strings.Contains(out, "unknown qualifier") {
		t.Errorf("unknown qualifier: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "edit", "bug", "--rename", "defekt", "--project", cliHere); code != 0 || !strings.Contains(out, "updated defekt") {
		t.Errorf("edit: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "remove", "request", "1", "defekt"); code != 0 {
		t.Errorf("remove: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "label", "delete", "defekt", "--project", cliHere); code != 0 || !strings.Contains(out, "deleted defekt") {
		t.Errorf("delete: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "topic", "save", "Suche", "internal/embed", "internal/store/embeddings", "--project", cliHere); code != 0 || !strings.Contains(out, "saved Suche") {
		t.Fatalf("topic save: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "topic", "list", "--project", cliHere); code != 0 || !strings.Contains(out, "internal/embed") {
		t.Errorf("topic list: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "topic:suche", "--project", cliHere); code != 0 {
		t.Errorf("topic: search: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "topic", "delete", "Suche", "--project", cliHere); code != 0 {
		t.Errorf("topic delete: %d %s", code, out)
	}
	if code, _ := painTaskRun(t, "label"); code != 2 {
		t.Errorf("bare label: %d", code)
	}
}

func TestSearchPresetCLI(t *testing.T) {
	st := moveCLIFixture(t)
	if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Im PR erledigt", Scope: scope.Axes{Project: cliHere}, Person: "alice"}, Criteria: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`INSERT INTO git_anchors(commit_sha,branch,merge_base,dirty,pr,captured_at) VALUES('abcdef1234567','feat/x','',0,12,'2026-11-05T10:00:00Z')`)
	st.DB().Exec(`UPDATE request_activity SET anchor_id=1 WHERE request_id=1 AND kind='request.created'`)
	code, out := painTaskRun(t, "search", "--presets", "--project", cliHere)
	for _, n := range []string{"pr-ergebnis", "branch-offen", "ordner", "seit", "fuer-mich"} {
		if code != 0 || !strings.Contains(out, n) {
			t.Errorf("presets lack %s: %d %s", n, code, out)
		}
	}
	if code, out := painTaskRun(t, "search", "--preset", "branch-offen", "--branch", "feat/x", "--project", cliHere); code != 0 || !strings.Contains(out, "REQ-1") {
		t.Errorf("branch-offen: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--preset", "pr-ergebnis", "--project", cliHere); code == 0 {
		t.Errorf("missing argument: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--save", "pr-offen", "--line", "is:open pr:{pr}", "--project", cliHere); code != 0 || !strings.Contains(out, "saved pr-offen [user]") {
		t.Errorf("save: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--save", "kaputt", "--line", "flavor:x", "--project", cliHere); code == 0 {
		t.Errorf("broken save: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--preset", "pr-offen", "--pr", "12", "--project", cliHere); code != 0 || !strings.Contains(out, "REQ-1") {
		t.Errorf("run saved: %d %s", code, out)
	}
	if code, out := painTaskRun(t, "search", "--project", cliHere); code != 2 {
		t.Errorf("no query: %d %s", code, out)
	}
}
