package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
)

// CommitGetInput is the commit_get tool.
type CommitGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"the project; default: the current one"`
	Commit  string `json:"commit" jsonschema:"the commit hash or its first 7 or more characters"`
}

func (s *Server) handleCommitGet(_ context.Context, _ *mcp.CallToolRequest, in CommitGetInput) (*mcp.CallToolResult, client.CommitDetailOut, error) {
	project, err := s.readProject(in.Project)
	if err != nil {
		return nil, client.CommitDetailOut{}, err
	}
	d, err := s.client.CommitGet(project, in.Commit)
	if err != nil {
		return nil, client.CommitDetailOut{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "commit %s %s\n%d entries were written at this commit:\n", d.Short, d.Title, len(d.Said))
	for _, e := range d.Said {
		fmt.Fprintf(&b, "- %s %s", e.Ref, e.Title)
		if e.Person != "" {
			fmt.Fprintf(&b, " (%s)", e.Person)
		}
		b.WriteString("\n")
	}
	return s.withCard(textResult(b.String()), d.Card), d, nil
}
