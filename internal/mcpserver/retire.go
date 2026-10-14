package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// idList renders entry ids the way the tools talk about them: #12, #15.
func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "#" + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

func (s *Server) handleRetire(_ context.Context, _ *mcp.CallToolRequest, in RetireInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 {
		return nil, nil, fmt.Errorf("id is required")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, nil, fmt.Errorf("reason is required: say why the entry is no longer true or needed")
	}
	saved, err := s.client.RetireKnowledge(in.ID, in.Reason)
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("retired #%d (%s): it no longer appears in search or the briefing; its history stays readable", saved.ID, saved.Status)), nil, nil
}

func (s *Server) handleSupersede(_ context.Context, _ *mcp.CallToolRequest, in SupersedeInput) (*mcp.CallToolResult, any, error) {
	if in.ID == 0 || len(in.Supersedes) == 0 {
		return nil, nil, fmt.Errorf("id and supersedes are required: name the replacement and the entries it replaces")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, nil, fmt.Errorf("reason is required: say what changed")
	}
	saved, err := s.client.SupersedeKnowledge(in.ID, in.Supersedes, in.Reason, "mcp")
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("#%d now supersedes %s: they no longer appear in search or the briefing; their history stays readable", saved.ID, idList(in.Supersedes))), nil, nil
}
