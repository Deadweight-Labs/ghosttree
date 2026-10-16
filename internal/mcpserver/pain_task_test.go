package mcpserver

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var painTaskTools = []string{"pain_record", "pain_get", "pain_search", "pain_update", "human_task_create", "human_task_list", "human_task_resolve"}

// The seven tools are discoverable with input and output schemas and
// annotations, and what the descriptions invite to omit is optional at the wire.
func TestPainAndTaskToolsHaveSchemasAndOptionalFields(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string][]string{
		"pain_record":        {"title"},
		"pain_get":           {"id"},
		"pain_search":        nil,
		"pain_update":        {"id"},
		"human_task_create":  {"kind", "title"},
		"human_task_list":    nil,
		"human_task_resolve": {"id", "action"},
	}
	optional := map[string][]string{
		"pain_record":        {"project", "body", "motivates"},
		"pain_update":        {"state", "remaining", "evidence_kind", "evidence_ref", "note", "title", "body", "reason", "link_add", "link_remove"},
		"human_task_create":  {"project", "description", "location", "check_hint", "request_id", "work_id", "assignee", "everyone"},
		"human_task_list":    {"project", "state", "request_id", "mine"},
		"human_task_resolve": {"note"},
		"pain_search":        {"query", "state", "request_id", "limit"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		if !slices.Contains(painTaskTools, tool.Name) {
			continue
		}
		seen++
		if tool.InputSchema == nil || tool.OutputSchema == nil || tool.Annotations == nil {
			t.Errorf("%s lacks schema or annotations", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, f := range optional[tool.Name] {
			if _, ok := schema.Properties[f]; !ok {
				t.Errorf("%s has no %s", tool.Name, f)
			}
			if slices.Contains(schema.Required, f) {
				t.Errorf("%s: %s is required at the wire", tool.Name, f)
			}
		}
		for _, f := range required[tool.Name] {
			if !slices.Contains(schema.Required, f) {
				t.Errorf("%s: %s should be required", tool.Name, f)
			}
		}
	}
	if seen != len(painTaskTools) {
		t.Fatalf("found %d of %d tools", seen, len(painTaskTools))
	}
}

func TestPainToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Maßnahme", Scope: scope.Axes{Project: project}}, Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})
	// Nur ein Titel genügt.
	out, failed := callTool(t, session, "pain_record", map[string]any{"title": "Kein Diff und keine Fassungen"})
	if failed {
		t.Fatalf("pain_record with a title only: %s", out)
	}
	if !strings.Contains(out, "PAIN-1") || !strings.Contains(out, `"state":"active"`) {
		t.Errorf("record answer: %s", out)
	}
	var created int
	st.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&created)
	if created != 1 {
		t.Errorf("pain_record created a request: %d", created)
	}
	// Kante über pain_update, Belegpflicht, mitigated ohne remaining.
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "link_add": []map[string]any{{"request_id": detail.Request.ID}}}); failed || !strings.Contains(got, "Maßnahme") {
		t.Errorf("link_add: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "resolved"}); !failed || !strings.Contains(got, "evidence") {
		t.Errorf("resolved without evidence: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "mitigated", "evidence_kind": "commit", "evidence_ref": "abc"}); !failed || !strings.Contains(got, "what remains") {
		t.Errorf("mitigated without remaining: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "mitigated", "remaining": "Lesen fehlt", "evidence_kind": "request", "evidence_ref": "REQ-1"}); failed || !strings.Contains(got, "Lesen fehlt") {
		t.Errorf("mitigated: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_search", map[string]any{}); failed || !strings.Contains(got, "Kein Diff") {
		t.Errorf("search: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_get", map[string]any{"id": 1}); failed || !strings.Contains(got, "history") {
		t.Errorf("get: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1}); !failed {
		t.Errorf("an update without anything: %s", got)
	}
	// A title change without a reason is refused before the state moves.
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "resolved", "evidence_kind": "commit", "evidence_ref": "abc", "title": "Neu"}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("title without reason: %v %s", failed, got)
	}
	if got, _ := callTool(t, session, "pain_get", map[string]any{"id": 1}); strings.Contains(got, "resolved") {
		t.Errorf("the state moved although the call was refused: %s", got)
	}
}

func TestHumanTaskToolsOverTheWire(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Zahlung", Scope: scope.Axes{Project: project}}, Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	rid := detail.Request.ID
	axes := scope.Axes{Project: project}
	session := connect(t, &Server{client: c, ctxAxes: axes})
	// Minimal call: kind, title, location, request_id.
	out, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "secret", "title": "Key anlegen", "location": "Agent Vault: stripe/prod", "request_id": rid})
	if failed || !strings.Contains(out, "TASK-1") {
		t.Fatalf("create: %v %s", failed, out)
	}
	if got, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "secret", "title": "Key anlegen", "request_id": rid}); !failed || !strings.Contains(got, "location") {
		t.Errorf("missing location: %v %s", failed, got)
	}
	leak := "sk-" + strings.Repeat("a1", 12)
	if got, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "input", "title": "Key " + leak, "request_id": rid}); !failed || strings.Contains(got, leak) || !strings.Contains(got, "secret") {
		t.Errorf("secret over MCP: %v %s", failed, got)
	}
	// Ohne request_id und ohne laufende Arbeit: eine Auskunft, die den Weg nennt.
	if got, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "action", "title": "Handgriff"}); !failed || !strings.Contains(got, "request_start_work") {
		t.Errorf("no request: %v %s", failed, got)
	}
	// Mit laufender Arbeit der Session ergibt sich der Request von selbst.
	sid, _ := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "mcp-task", Scope: axes})
	if _, _, err := st.StartRequestWork(rid, sid, "primary", "alice"); err != nil {
		t.Fatal(err)
	}
	got, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "action", "title": "Handgriff"})
	if failed || !strings.Contains(got, `"request_id":1`) {
		t.Errorf("from active work: %v %s", failed, got)
	}
	// Der Mensch erledigt über die Store-Schicht (kein done im MCP); der Agent prüft.
	if _, failed := callTool(t, session, "human_task_resolve", map[string]any{"id": 1, "action": "done"}); !failed {
		t.Error("an agent marked a task done")
	}
	if _, err := c.MarkTasksDone([]int64{1}, "gesetzt", 0); err != nil {
		t.Fatal(err)
	}
	if got, failed := callTool(t, session, "human_task_list", map[string]any{}); failed || !strings.Contains(got, `"state":"done"`) {
		t.Errorf("list: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "human_task_resolve", map[string]any{"id": 1, "action": "reopen"}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("reopen without a note: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "human_task_resolve", map[string]any{"id": 1, "action": "confirm"}); failed || !strings.Contains(got, `"state":"confirmed"`) {
		t.Errorf("confirm: %v %s", failed, got)
	}
}
