package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The names in this test are the ones agents sent in recorded sessions: id
// where the action says request_id, a reference where it wants the number, a
// flag where it wants response_format.
func TestCoreCallsFollowAGuessedFieldName(t *testing.T) {
	session := coreSession(t)
	text, failed := callTool(t, session, "request", map[string]any{
		"action": "create", "type": "feature", "name": "Exports carry the time zone",
		"description": "Every exported row names the zone its times are in.",
		"criteria":    []any{"an export of two rows shows the zone in each"},
	})
	if failed {
		t.Fatalf("create with name for title: %s", text)
	}
	ref := regexp.MustCompile(`REQ-\d+`).FindString(text)
	if ref == "" {
		t.Fatalf("create names no request: %s", text)
	}
	for _, args := range []map[string]any{
		{"action": "get", "id": ref},
		{"action": "get", "request": strings.TrimPrefix(ref, "REQ-")},
		{"action": "get", "request_id": "#" + strings.TrimPrefix(ref, "REQ-"), "detailed": true},
	} {
		text, failed = callTool(t, session, "request", args)
		if failed || !strings.Contains(text, "Exports carry the time zone") {
			t.Fatalf("request %v: %v %s", args, failed, text)
		}
	}
	// A guess is not followed when the field it would land in is given too.
	text, failed = callTool(t, session, "request", map[string]any{"action": "get", "request_id": 1, "id": 2})
	if !failed || !strings.Contains(text, "does not take id") {
		t.Fatalf("both id and request_id: %v %s", failed, text)
	}
	// What is missing is named with what it is for, not as a validator path.
	text, failed = callTool(t, session, "pain", map[string]any{"action": "record"})
	if !failed || !strings.Contains(text, "pain action=record needs title (") {
		t.Fatalf("missing title: %v %s", failed, text)
	}
}

// A harness that is handed text and a structured copy shows its model the
// structured one alone, so the rendered answer has to be the only one.
func TestCoreAnswersCarryTextAndNoStructuredCopy(t *testing.T) {
	session := coreSession(t)
	text, failed := callTool(t, session, "request", map[string]any{
		"action": "create", "type": "feature", "title": "Exports carry the time zone",
		"description": "Every exported row names the zone its times are in.",
		"criteria":    []any{"an export of two rows shows the zone in each"},
	})
	if failed {
		t.Fatal(text)
	}
	for _, call := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"request", map[string]any{"action": "get", "request_id": 1}, "criterion_id"},
		{"request", map[string]any{"action": "search", "query": "time zone"}, "Exports carry"},
		{"pain", map[string]any{"action": "record", "title": "Exports are slow"}, "PAIN-"},
		{"package", map[string]any{"action": "create", "branch": "feat/zone", "requests": []any{map[string]any{"request_id": 1}}}, "PKG-"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatalf("%s %v: %v", call.name, call.args, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		if res.IsError || !strings.Contains(sb.String(), call.want) {
			t.Errorf("%s %v: error %v, text %s", call.name, call.args, res.IsError, sb.String())
		}
		if res.StructuredContent != nil {
			t.Errorf("%s %v carries a structured copy next to its text: %v", call.name, call.args, res.StructuredContent)
		}
	}
}

func TestContextSearchTakesALimit(t *testing.T) {
	session := coreSession(t)
	for i := 0; i < 4; i++ {
		text, failed := callTool(t, session, "context_remember", map[string]any{
			"type": "note", "scope_hint": "project", "title": fmt.Sprintf("Kranich note %d", i),
			"body": "The kranich importer reads one file per run.",
		})
		if failed {
			t.Fatal(text)
		}
	}
	all, failed := callTool(t, session, "context_search", map[string]any{"query": "kranich"})
	if failed || strings.Count(all, "Kranich note") != 4 {
		t.Fatalf("search without limit: %v %s", failed, all)
	}
	two, failed := callTool(t, session, "context_search", map[string]any{"query": "kranich", "limit": 2})
	if failed || strings.Count(two, "Kranich note") != 2 {
		t.Fatalf("search with limit 2: %v %s", failed, two)
	}
}
