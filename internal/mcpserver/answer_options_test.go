package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// Antwortoptionen über MCP: an einer Aufgabe vom Typ input, mit Prüfung vor dem
// Senden bei einer Frage, und in den Beschreibungen der Werkzeuge.
func TestAnswerOptionsOverMCP(t *testing.T) {
	s, _, _, rid := linkWorld(t)
	session := connect(t, s)

	got := wireText(t, s, "human_task_create", map[string]any{"kind": "input", "title": "Welche Variante?", "request_id": rid, "options": []string{"Variante A", "Variante B"}})
	mustHave(t, "human_task_create", got, `"title":"Welche Variante?"`)
	for name, args := range map[string]map[string]any{
		"one option":       {"kind": "input", "title": "x", "request_id": rid, "options": []string{"nur eine"}},
		"on a secret":      {"kind": "secret", "title": "x", "request_id": rid, "location": "vault", "options": []string{"a", "b"}},
		"duplicate":        {"kind": "action", "title": "x", "request_id": rid, "options": []string{"a", "A"}},
		"looks like a key": {"kind": "input", "title": "x", "request_id": rid, "options": []string{"ghp_" + strings.Repeat("a", 36), "b"}},
	} {
		if out, failed := callTool(t, session, "human_task_create", args); !failed {
			t.Errorf("%s: accepted: %s", name, out)
		}
	}
	// Eine Frage mit Vorschlägen: Absicht und Zahl werden geprüft, bevor etwas gesendet wird.
	for name, args := range map[string]struct {
		in   map[string]any
		want string
	}{
		"no intent":    {map[string]any{"body": "Welche?", "options": []string{"a", "b"}}, "intent question only"},
		"a blocker":    {map[string]any{"body": "Welche?", "intent": "blocker", "mention": "claude:x", "options": []string{"a", "b"}}, "intent question only"},
		"one option":   {map[string]any{"body": "Welche?", "intent": "question", "mention": "claude:x", "options": []string{"a"}}, "answer options"},
		"empty option": {map[string]any{"body": "Welche?", "intent": "question", "mention": "claude:x", "options": []string{"a", ""}}, "empty"},
	} {
		if out, failed := callTool(t, session, "coord_send", args.in); !failed || !strings.Contains(out, args.want) {
			t.Errorf("%s: %v %s", name, failed, out)
		}
	}
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		data, _ := json.Marshal(tool.InputSchema)
		raw := string(data)
		if (tool.Name == "coord_send" || tool.Name == "human_task_create") && strings.Contains(raw, "options") {
			seen++
		}
		if tool.Name == "decision_open" && !strings.Contains(tool.Description, "quick answers") {
			t.Errorf("decision_open does not say that variants are quick answers: %q", tool.Description)
		}
	}
	if seen != 2 {
		t.Errorf("options in the schema of %d of the 2 tools", seen)
	}
}
