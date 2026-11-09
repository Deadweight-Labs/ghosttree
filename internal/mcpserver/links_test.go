package mcpserver

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const linkProject = "github.com/x/y"

// linkWorld is a server with one request, so objects can hang on it.
func linkWorld(t *testing.T) (*Server, *store.Store, string, int64) {
	t.Helper()
	c, st := newTestClient(t)
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Kurzwege", Scope: scope.Axes{Project: linkProject}, Person: "alice"},
		Criteria: []string{"erstes", "zweites"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: linkProject, Branch: "main", Machine: "workstation-a"}}
	return s, st, c.ServerURL() + "/ui/x/y", d.Request.ID
}

// wireText calls a tool through the real MCP transport and returns all text blocks.
func wireText(t *testing.T, s *Server, tool string, args map[string]any) string {
	t.Helper()
	got, failed := callTool(t, connect(t, s), tool, args)
	if failed {
		t.Fatalf("%s failed: %s", tool, got)
	}
	return got
}

func mustHave(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, got)
		}
	}
	if strings.Contains(got, "token") && strings.Contains(got, "/ui/") && regexp.MustCompile(`https?://\S*token`).MatchString(got) {
		t.Errorf("%s carries a token in a link:\n%s", what, got)
	}
}

// REQ-603 AC 1: every answer about one object carries its web address and card.
func TestRequestAnswersCarryLinkAndCard(t *testing.T) {
	s, _, base, rid := linkWorld(t)
	got := wireText(t, s, "request_get", map[string]any{"request_id": rid})
	mustHave(t, "request_get", got, "Issue REQ-1: Kurzwege", "state: open", "next: 2 of 2 criteria open", "with: agent", "link: "+base+"/issues/1")

	got = wireText(t, s, "request_create", map[string]any{"type": "bug", "title": "Neu", "description": "d", "criteria": []string{"a"}})
	mustHave(t, "request_create", got, "Issue REQ-2: Neu", "link: "+base+"/issues/2")

	got = wireText(t, s, "request_record_progress", map[string]any{"request_id": 2, "action": "criterion_add", "description": "noch eins"})
	mustHave(t, "request_record_progress", got, "Issue REQ-2", "next: 2 of 2 criteria open", "link: "+base+"/issues/2")

	got = wireText(t, s, "request_search", map[string]any{"query": ""})
	mustHave(t, "request_search", got, `"url":"`+base+`/issues/1"`, `"url":"`+base+`/issues/2"`)
	if strings.Contains(got, "link: ") || strings.Contains(got, "state: open") {
		t.Errorf("search hits must carry the url only, no card:\n%s", got)
	}
}

func TestHumanTaskAnswersCarryStepsLinkAndCard(t *testing.T) {
	s, st, base, rid := linkWorld(t)
	got := wireText(t, s, "human_task_create", map[string]any{
		"kind": "action", "title": "GitHub verbinden", "request_id": rid,
		"steps":       []map[string]any{{"text": "Einstellungen öffnen", "url": "https://github.com/settings/apps"}, {"text": "App installieren"}},
		"explanation": "Ohne die Verbindung kann der Server keine Pull Requests sehen. Du musst einmal die App freigeben. Ich empfehle, das heute zu tun.",
	})
	mustHave(t, "human_task_create", got, `"steps":[{"text":"Einstellungen öffnen","url":"https://github.com/settings/apps"},{"text":"App installieren"}]`,
		"Task TASK-1: GitHub verbinden", "human does: 1. Einstellungen öffnen", "link: "+base+"/tasks/1")
	if strings.Contains(got, `"hint"`) {
		t.Errorf("a task with steps must not get the hint:\n%s", got)
	}
	// AC 4: steps and explanation come along, so the explanation is the agent's
	// and nothing is left for the later generation run.
	if e, found, err := st.Explanation(explain.KindTask, 1); err != nil || !found || e.Source != explain.SourceAgent || !strings.Contains(e.Text, "Pull Requests") {
		t.Errorf("explanation of TASK-1: %+v found=%v err=%v", e, found, err)
	}

	// Ohne Ablauf: freundlicher Hinweis im Ergebnis.
	got = wireText(t, s, "human_task_create", map[string]any{"kind": "input", "title": "Entscheiden", "request_id": rid})
	mustHave(t, "human_task_create without steps", got, `"hint":"No steps given`, "Task TASK-2")

	// Ungültige Schritte werden abgelehnt: Zeilenumbruch, Formatzeichen, fremdes Schema.
	for name, step := range map[string]map[string]any{
		"line break": {"text": "eins\nzwei"},
		"format":     {"text": "ab\u202ecd"},
		"scheme":     {"text": "x", "url": "javascript:alert(1)"},
		"login":      {"text": "x", "url": "https://u:p@example.org/"},
	} {
		out, failed := callTool(t, connect(t, s), "human_task_create", map[string]any{"kind": "action", "title": "t", "request_id": rid, "steps": []map[string]any{step}})
		if !failed || !strings.Contains(out, "step") {
			t.Errorf("%s: want a refusal naming the step, got failed=%v %s", name, failed, out)
		}
	}

	list := wireText(t, s, "human_task_list", map[string]any{})
	mustHave(t, "human_task_list", list, `"url":"`+base+`/tasks/1"`)

	if _, err := s.client.MarkTasksDone([]int64{1}, "erledigt", 0); err != nil {
		t.Fatal(err)
	}
	got = wireText(t, s, "human_task_resolve", map[string]any{"id": 1, "action": "confirm"})
	mustHave(t, "human_task_resolve", got, "Task TASK-1", "state: confirmed", "link: ")
}

func TestPainIdeaAndKnowledgeAnswersCarryLinkAndCard(t *testing.T) {
	s, _, base, _ := linkWorld(t)
	got := wireText(t, s, "pain_record", map[string]any{"title": "Es hakt"})
	mustHave(t, "pain_record", got, "Pain PAIN-1: Es hakt", "link: "+base+"/pains/1")
	got = wireText(t, s, "pain_get", map[string]any{"id": 1})
	mustHave(t, "pain_get", got, "Pain PAIN-1", "link: "+base+"/pains/1")
	got = wireText(t, s, "pain_update", map[string]any{"id": 1, "state": "obsolete", "note": "weg"})
	mustHave(t, "pain_update", got, "Pain PAIN-1", "state: obsolete", "link: "+base+"/pains/1")
	got = wireText(t, s, "pain_search", map[string]any{"state": "all"})
	mustHave(t, "pain_search", got, `"url":"`+base+`/pains/1"`)

	got = wireText(t, s, "idea_record", map[string]any{"title": "Eine Idee"})
	mustHave(t, "idea_record", got, "Idea IDEA-1: Eine Idee", "link: "+base+"/ideas/1")
	got = wireText(t, s, "idea_get", map[string]any{"id": 1})
	mustHave(t, "idea_get", got, "Idea IDEA-1", "with: human")
	got = wireText(t, s, "idea_update", map[string]any{"id": 1, "action": "dismiss", "reason": "nein"})
	mustHave(t, "idea_update", got, "Idea IDEA-1", "state: dismissed")
	got = wireText(t, s, "idea_search", map[string]any{"state": "all"})
	mustHave(t, "idea_search", got, `"url":"`+base+`/ideas/1"`)

	got = wireText(t, s, "context_remember", map[string]any{"type": "note", "title": "Merke", "body": "etwas Langes", "scope_hint": "project"})
	mustHave(t, "context_remember", got, "stored #1", "Knowledge #1: Merke", "link: "+base+"/knowledge/1")
	got = wireText(t, s, "context_search", map[string]any{"query": "Merke", "kind": "knowledge"})
	mustHave(t, "context_search hit", got, "#1 [", base+"/knowledge/1")
	if strings.Contains(got, "state: ") {
		t.Errorf("a search hit carries the link only:\n%s", got)
	}
	got = wireText(t, s, "context_search", map[string]any{"knowledge_id": 1})
	mustHave(t, "context_search knowledge_id", got, "Knowledge #1", "link: "+base+"/knowledge/1")
	got = wireText(t, s, "context_search", map[string]any{"query": "is:open is:issue"})
	mustHave(t, "qualified search", got, base+"/issues/1")
}

func TestPackageAndRunbookAnswersCarryLinkAndCard(t *testing.T) {
	ctx := context.Background()
	s, st, base, rid := linkWorld(t)
	_ = st
	s.SetLive(func() Live { return Live{Branch: "feat/gn", CWD: "/work/y"} })
	got := wireText(t, s, "package_update", map[string]any{"action": "create", "name": "Kurzwege", "branch": "feat/gn", "requests": []map[string]any{{"request_id": rid, "role": "pflicht"}}})
	mustHave(t, "package_update", got, "Checklist PKG-1: Kurzwege", "1 required issue(s) still open", "link: "+base+"/packages/1")
	got = wireText(t, s, "package_get", map[string]any{"branch": "feat/gn"})
	mustHave(t, "package_get", got, "Checklist PKG-1", "link: "+base+"/packages/1")
	got = wireText(t, s, "package_update", map[string]any{"action": "set", "package_id": 1, "pr": 155})
	mustHave(t, "package_update with a PR", got, "link: "+base+"/pulls/155/checkliste")

	res, _, err := s.handleRunbookWrite(ctx, nil, RunbookWriteInput{Name: "rollout", Spec: rbTestSpec()})
	if err != nil {
		t.Fatal(err)
	}
	mustHave(t, "runbook_write", allText(res), "Runbook rollout", "a human approves the newest version", "link: "+base+"/runbooks/rollout")
	res, _, err = s.handleRunbookGet(ctx, nil, RunbookGetInput{Name: "rollout"})
	if err != nil {
		t.Fatal(err)
	}
	mustHave(t, "runbook_get", allText(res), "Runbook rollout", "link: "+base+"/runbooks/rollout")
	res, _, _ = s.handleRunbookGet(ctx, nil, RunbookGetInput{})
	mustHave(t, "runbook list", allText(res), base+"/runbooks/rollout")
}

func allText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text + "\n")
		}
	}
	return b.String()
}

// REQ-603 AC 2: open_link hands out link and card for every short form and
// opens nothing; an unreadable object answers like a missing one.
func TestOpenLinkToolAnswersForEveryShortForm(t *testing.T) {
	s, _, base, rid := linkWorld(t)
	root := strings.TrimSuffix(base, "/x/y")
	wireText(t, s, "human_task_create", map[string]any{"kind": "action", "title": "Handgriff", "request_id": rid})
	wireText(t, s, "pain_record", map[string]any{"title": "Qual"})
	wireText(t, s, "idea_record", map[string]any{"title": "Idee"})
	wireText(t, s, "context_remember", map[string]any{"type": "note", "title": "Merke", "body": "b", "scope_hint": "project"})
	want := map[string][]string{
		"REQ-1":                  {"Issue REQ-1: Kurzwege", base + "/issues/1"},
		"AC-1.2":                 {"Criterion AC-1.2: zweites", base + "/issues/1#criterion-2"},
		"REQ-1#1":                {"Criterion AC-1.1", base + "/issues/1#criterion-1"},
		"TASK-1":                 {"Task TASK-1: Handgriff", base + "/tasks/1"},
		"PAIN-1":                 {"Pain PAIN-1: Qual", base + "/pains/1"},
		"IDEA-1":                 {"Idea IDEA-1: Idee", base + "/ideas/1"},
		"#1":                     {"Knowledge #1: Merke", base + "/knowledge/1"},
		"pr:155":                 {"Pull request #155", base + "/pulls/155"},
		"commit:2d950bf":         {"Commit 2d950bf", base + "/commit/2d950bf"},
		"preset:pr-ergebnis 155": {"Search preset:pr-ergebnis", root + "/presets/repo/pr-ergebnis?pr=155&project=x%2Fy"},
		"internal/store/a.go":    {"File internal/store/a.go", base + "/blob/main/internal/store/a.go"},
		"is:open label:bug":      {"Search", base + "/issues?q=is%3Aopen+label%3Abug"},
		"inbox":                  {"Inbox", root + "/inbox"},
	}
	for ref, wants := range want {
		got := wireText(t, s, "open_link", map[string]any{"ref": ref})
		mustHave(t, "open_link "+ref, got, append(wants, "link: "+wants[1])...)
	}
	for _, ref := range []string{"REQ-999", "TASK-999", "PAIN-999", "IDEA-999", "AC-1.9", "preset:gibtsnicht"} {
		got, failed := callTool(t, connect(t, s), "open_link", map[string]any{"ref": ref})
		if !failed || !strings.Contains(got, "not found, or you may not read it") || strings.Contains(got, "http") {
			t.Errorf("%s: want the one answer for unknown, got failed=%v %s", ref, failed, got)
		}
	}
	if got, failed := callTool(t, connect(t, s), "open_link", map[string]any{"ref": "banane"}); !failed || strings.Contains(got, "http") {
		t.Errorf("garbage ref: %v %s", failed, got)
	}
}
