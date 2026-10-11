package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// targetHint is appended to the descriptions of the tools that write something
// down. Without it an agent files a problem with tool X under whatever project
// it happens to be standing in, and a fact about another machine under its own.
const targetHint = " If it belongs to a different project or machine than the current one, pass project/machine."

// writeTarget resolves where a write goes: the project the caller names (it has
// to be one the account can write to) or the session's own. With neither the
// write is refused and the error lists what could be written to.
func (s *Server) writeTarget(explicit string) (string, error) {
	return s.client.WriteTarget(explicit, s.ctxAxes.Project)
}

// MoveInput is the context_move tool. Exactly one target.
type MoveInput struct {
	ID      int64  `json:"id" jsonschema:"knowledge entry to move"`
	Project string `json:"project,omitempty" jsonschema:"move it to this project: a remote like github.com/owner/repo or a short name that is unique among the projects you can write to"`
	Machine string `json:"machine,omitempty" jsonschema:"move it to this machine; any machine name, including one no agent ever ran on"`
	Global  bool   `json:"global,omitempty" jsonschema:"move it to global, where it applies everywhere"`
}

func (s *Server) handleMove(_ context.Context, _ *mcp.CallToolRequest, in MoveInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 {
		return nil, nil, fmt.Errorf("id is required")
	}
	chosen := 0
	for _, set := range []bool{in.Project != "", in.Machine != "", in.Global} {
		if set {
			chosen++
		}
	}
	if chosen != 1 {
		return nil, nil, fmt.Errorf("name exactly one target: project, machine or global. To move a request, use request_record_progress with action=correct and project")
	}
	project := in.Project
	if project != "" {
		var err error
		if project, err = s.client.WriteTarget(project, ""); err != nil {
			return nil, nil, err
		}
	}
	saved, err := s.client.MoveKnowledge(in.ID, project, in.Machine, in.Global)
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("moved #%d to %s", saved.ID, scopeLabel(saved.Scope))), nil, nil
}
