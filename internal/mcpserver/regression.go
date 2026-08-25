package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RegressionCoverInput struct {
	KnowledgeID int64  `json:"knowledge_id,omitempty" jsonschema:"the pitfall to judge; omit to list the fixes nothing guards"`
	State       string `json:"state,omitempty" jsonschema:"covered, uncovered, or not_applicable"`
	Test        string `json:"test,omitempty" jsonschema:"with covered: the test that would catch the defect's return, e.g. internal/store/regression_test.go:TestTheGapQueryFindsFixesNothingGuards"`
}

type RegressionCoverOutput struct {
	KnowledgeID int64  `json:"knowledge_id,omitempty"`
	State       string `json:"state,omitempty"`
	Test        string `json:"test,omitempty"`
	Gaps        []Gap  `json:"gaps,omitempty"`
	Unreviewed  int    `json:"unreviewed,omitempty"`
	Note        string `json:"note,omitempty"`
}

type Gap struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// handleRegressionCover does both halves of the same question, because they are
// asked in the same breath: "what guards this one" and "what is guarded by
// nothing".
func (s *Server) handleRegressionCover(_ context.Context, _ *mcp.CallToolRequest, in RegressionCoverInput) (*mcp.CallToolResult, RegressionCoverOutput, error) {
	ax := scope.Axes{Project: s.ctxAxes.Project}
	if in.KnowledgeID == 0 {
		gaps, unreviewed, err := s.client.RegressionGaps(ax)
		if err != nil {
			return nil, RegressionCoverOutput{}, err
		}
		out := RegressionCoverOutput{Unreviewed: unreviewed}
		for _, k := range gaps {
			out.Gaps = append(out.Gaps, Gap{ID: k.ID, Title: k.Title})
		}
		var b strings.Builder
		for _, gap := range out.Gaps {
			fmt.Fprintf(&b, "- #%d %s\n", gap.ID, gap.Title)
		}
		if len(out.Gaps) == 0 {
			b.WriteString("No fix is recorded as unguarded.\n")
		}
		// Die Zahl reist immer mit. Eine leere Lückenliste neben hundert
		// unbeurteilten Einträgen ist keine Entwarnung, sondern eine
		// ungestellte Frage — und für ein Modell ist der Unterschied nur
		// sichtbar, wenn er dasteht.
		fmt.Fprintf(&b, "\n%d gaps; %d pitfalls nobody has judged yet.\n", len(out.Gaps), unreviewed)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, out, nil
	}
	if err := s.client.SetRegressionCover(in.KnowledgeID, in.State, in.Test); err != nil {
		return nil, RegressionCoverOutput{}, err
	}
	out := RegressionCoverOutput{KnowledgeID: in.KnowledgeID, State: in.State, Test: in.Test}
	text := fmt.Sprintf("#%d: %s", in.KnowledgeID, in.State)
	if in.Test != "" {
		text += " by " + in.Test
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}
