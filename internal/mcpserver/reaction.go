package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ReactionSetInput is the reaction_set tool.
type ReactionSetInput struct {
	Ref      string `json:"ref" jsonschema:"what to react to: REQ-n (issue), IDEA-n or comment:n (a comment, see comment action=list)"`
	Reaction string `json:"reaction" jsonschema:"one of +1, -1, laugh, hooray, confused, heart, rocket, eyes (or its emoji)"`
	Remove   bool   `json:"remove,omitempty" jsonschema:"take your reaction back instead of setting it"`
}

// ReactionListInput is the reaction_list tool.
type ReactionListInput struct {
	Ref string `json:"ref" jsonschema:"REQ-n, IDEA-n or comment:n"`
}

func (s *Server) handleReactionSet(_ context.Context, _ *mcp.CallToolRequest, in ReactionSetInput) (*mcp.CallToolResult, store.ReactionSummary, error) {
	kind, id, err := client.ReactionTarget(in.Ref)
	if err != nil {
		return nil, store.ReactionSummary{}, err
	}
	var out store.ReactionSummary
	if in.Remove {
		out, err = s.client.Unreact(kind, id, in.Reaction)
	} else {
		out, err = s.client.React(kind, id, in.Reaction)
	}
	if err != nil {
		return nil, store.ReactionSummary{}, err
	}
	return textResult(client.ReactionLine(strings.ToUpper(strings.TrimSpace(in.Ref)), out)), out, nil
}

func (s *Server) handleReactionList(_ context.Context, _ *mcp.CallToolRequest, in ReactionListInput) (*mcp.CallToolResult, client.ReactionsOut, error) {
	kind, id, err := client.ReactionTarget(in.Ref)
	if err != nil {
		return nil, client.ReactionsOut{}, err
	}
	out, err := s.client.Reactions(kind, id)
	if err != nil {
		return nil, client.ReactionsOut{}, err
	}
	return textResult(client.ReactionLine(strings.ToUpper(strings.TrimSpace(in.Ref)), out.ReactionSummary)), out, nil
}
