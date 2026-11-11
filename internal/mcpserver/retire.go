package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/knref"
)

// idList renders entry ids the way the tools talk about them: KN-12, KN-15.
func idList(ids []int64) string { return knref.List(ids) }

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
	s.knowledgeChanged()
	return s.withCard(textResult(fmt.Sprintf("retired %s (%s): it no longer appears in search or the briefing; its history stays readable", knref.Format(saved.ID), saved.Status)), s.builder().KnowledgeCard(saved)), nil, nil
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
	s.knowledgeChanged()
	return s.withCard(textResult(fmt.Sprintf("%s now supersedes %s: they no longer appear in search or the briefing; their history stays readable", knref.Format(saved.ID), idList(in.Supersedes))), s.builder().KnowledgeCard(saved)), nil, nil
}
