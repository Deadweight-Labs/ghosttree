package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// slimToolList shortens what tools/list sends: every output schema becomes just
// its root type, {"type":"object"}. The full schemas were about half of the
// list and no agent reads them. Claude's tool definitions have no field for
// them, Codex does not serialize them into the model request (only its code
// mode types structured results with them) and OpenCode passes them to its
// code mode as well, not to the model API. The answer of a tool is unchanged:
// structured content is still sent and the server still checks it against the
// full schema, which the SDK resolved when the tool was added and which this
// leaves alone. The tools in the registry are not touched; each listed tool is
// a copy.
func slimToolList(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "tools/list" {
			return res, err
		}
		list, ok := res.(*mcp.ListToolsResult)
		if !ok {
			return res, nil
		}
		slim := *list
		slim.Tools = make([]*mcp.Tool, len(list.Tools))
		for i, t := range list.Tools {
			if t == nil || t.OutputSchema == nil {
				slim.Tools[i] = t
				continue
			}
			c := *t
			c.OutputSchema = rootOnly(t.OutputSchema)
			slim.Tools[i] = &c
		}
		return &slim, nil
	}
}

// rootOnly is a schema with only the root type of s, so that a client that
// checks structured content against it still finds an object to be an object.
// A schema without a single root type is kept as it is.
func rootOnly(s any) any {
	var typ string
	switch v := s.(type) {
	case *jsonschema.Schema:
		typ = v.Type
	default:
		raw, err := json.Marshal(s)
		if err != nil {
			return s
		}
		var probe struct {
			Type any `json:"type"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			return s
		}
		typ, _ = probe.Type.(string)
	}
	if typ == "" {
		return s
	}
	return &jsonschema.Schema{Type: typ}
}
