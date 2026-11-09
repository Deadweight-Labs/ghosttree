package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxToolListBytes is a ceiling on what tools/list sends. It is not a target:
// it makes growth visible. Raising it should be a decision, not a side effect
// of adding a tool family (PAIN-71, IDEA-12).
const maxToolListBytes = 115_000

// tools/list carries every output schema as its root type only, and the tools
// still answer with structured content, checked by the server against the full
// schema.
func TestToolListSendsOutputSchemasAsTheirRootTypeOnly(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	withOutput := 0
	for _, tool := range res.Tools {
		if tool.OutputSchema == nil {
			continue
		}
		withOutput++
		raw, _ := json.Marshal(tool.OutputSchema)
		if string(raw) != `{"type":"object"}` {
			t.Errorf("%s: output schema %s", tool.Name, raw)
		}
	}
	if withOutput == 0 {
		t.Fatal("no tool lists an output schema; the slim list was meant to keep the root type")
	}
	whole, _ := json.Marshal(res)
	if len(whole) > maxToolListBytes {
		t.Errorf("tools/list is %d bytes, over the ceiling of %d", len(whole), maxToolListBytes)
	}

	// A tool with typed output still sends structured content.
	out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "idea_search", Arguments: map[string]any{}})
	if err != nil || out.IsError {
		t.Fatalf("idea_search: %v %+v", err, out)
	}
	if out.StructuredContent == nil {
		t.Error("idea_search lost its structured content")
	}
}
