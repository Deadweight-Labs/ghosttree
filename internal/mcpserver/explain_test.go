package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const mcpNote = "Das ist die Einordnung für Menschen, die nicht tief einsteigen wollen. Empfehlung: freigeben."

var explainedTools = []string{"context_remember", "request_create", "pain_record", "human_task_create", "coord_send"}

// Every tool that creates something people read has an optional explanation,
// and its description says what belongs in it.
func TestCreatingToolsOfferAnOptionalExplanation(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		wanted := false
		for _, n := range explainedTools {
			wanted = wanted || n == tool.Name
		}
		if !wanted {
			continue
		}
		seen++
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		prop, ok := schema.Properties["explanation"]
		if !ok {
			t.Errorf("%s has no explanation", tool.Name)
			continue
		}
		for _, r := range schema.Required {
			if r == "explanation" {
				t.Errorf("%s: explanation is required", tool.Name)
			}
		}
		desc, _ := prop["description"].(string)
		for _, part := range []string{"everyday words", "why it matters", "recommendation", "never a secret"} {
			if !strings.Contains(desc, part) {
				t.Errorf("%s: the explanation text does not say %q: %s", tool.Name, part, desc)
			}
		}
		if !strings.Contains(strings.ToLower(tool.Description), "explanation") {
			t.Errorf("%s: the tool description does not mention the explanation", tool.Name)
		}
	}
	if seen != len(explainedTools) {
		t.Fatalf("found %d of %d tools", seen, len(explainedTools))
	}
}

func TestExplanationTravelsFromTheToolToTheStore(t *testing.T) {
	c, st := newTestClient(t)
	project := "github.com/x/y"
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: project}})

	if out, failed := callTool(t, session, "context_remember", map[string]any{"type": "pitfall", "title": "Wächter", "body": "Zahlen zählen nicht.", "scope_hint": "project", "explanation": mcpNote}); failed {
		t.Fatalf("remember: %s", out)
	}
	if out, failed := callTool(t, session, "pain_record", map[string]any{"title": "Es hakt", "explanation": mcpNote}); failed {
		t.Fatalf("pain: %s", out)
	}
	if out, failed := callTool(t, session, "request_create", map[string]any{"type": "feature", "title": "Bauen", "description": "d", "criteria": []string{"c"}, "explanation": mcpNote}); failed {
		t.Fatalf("request: %s", out)
	}
	if out, failed := callTool(t, session, "human_task_create", map[string]any{"kind": "input", "title": "Entscheiden", "request_id": 1, "explanation": mcpNote}); failed {
		t.Fatalf("task: %s", out)
	}
	for _, c := range []struct {
		kind string
		id   int64
	}{{explain.KindKnowledge, 1}, {explain.KindPain, 1}, {explain.KindRequest, 1}, {explain.KindTask, 1}} {
		e, ok, err := st.Explanation(c.kind, c.id)
		if err != nil || !ok || e.Text != mcpNote || e.Source != explain.SourceAgent {
			t.Errorf("%s: %+v %v %v", c.kind, e, ok, err)
		}
	}
	// A secret in it refuses the call and leaves no object behind.
	leak := "Zugang sk-" + strings.Repeat("a1", 12) + " steht hier im Klartext, bitte lesen."
	if out, failed := callTool(t, session, "pain_record", map[string]any{"title": "Zweites", "explanation": leak}); !failed || strings.Contains(out, "sk-a1") {
		t.Errorf("secret explanation: failed=%v %s", failed, out)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM pains`).Scan(&n)
	if n != 1 {
		t.Errorf("%d pains, want 1", n)
	}
}
