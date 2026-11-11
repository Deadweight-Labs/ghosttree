package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
)

// PullListInput is the pull_list tool.
type PullListInput struct {
	Project string `json:"project,omitempty" jsonschema:"the project; default: the current one"`
	State   string `json:"state,omitempty" jsonschema:"open, merged, closed (closed without merge) or all (default all)"`
	Branch  string `json:"branch,omitempty" jsonschema:"only pull requests whose head is this branch"`
	Author  string `json:"author,omitempty" jsonschema:"only pull requests of this GitHub login"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size, at most 100 (default 30)"`
	Offset  int    `json:"offset,omitempty" jsonschema:"entries to skip"`
}

// PullGetInput is the pull_get tool.
type PullGetInput struct {
	Project string `json:"project,omitempty" jsonschema:"the project; default: the current one"`
	PR      int64  `json:"pr" jsonschema:"the pull request number"`
}

func (s *Server) handlePullList(_ context.Context, _ *mcp.CallToolRequest, in PullListInput) (*mcp.CallToolResult, client.PullListOut, error) {
	project, err := s.readProject(in.Project)
	if err != nil {
		return nil, client.PullListOut{}, err
	}
	out, err := s.client.PullList(project, in.State, in.Branch, in.Author, in.Limit, in.Offset)
	if err != nil {
		return nil, client.PullListOut{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pull requests (open %d, merged %d, closed %d), showing %d from %d\n", out.Total, out.Counts.Open, out.Counts.Merged, out.Counts.Closed, len(out.Items), out.Offset)
	for _, p := range out.Items {
		fmt.Fprintf(&b, "#%d [%s] %s (%s", p.Number, p.State, p.Title, p.Head)
		if p.Base != "" {
			fmt.Fprintf(&b, " → %s", p.Base)
		}
		if p.Author != "" {
			fmt.Fprintf(&b, ", %s", p.Author)
		}
		fmt.Fprintf(&b, ", %d commits)\n", p.Commits)
	}
	return textResult(b.String()), out, nil
}

func (s *Server) handlePullGet(_ context.Context, _ *mcp.CallToolRequest, in PullGetInput) (*mcp.CallToolResult, client.PullDetailOut, error) {
	project, err := s.readProject(in.Project)
	if err != nil {
		return nil, client.PullDetailOut{}, err
	}
	d, err := s.client.PullGet(project, in.PR)
	if err != nil {
		return nil, client.PullDetailOut{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "PR #%d [%s] %s\n%s → %s, %d commits\n", d.Number, d.State, d.Title, d.Head, d.Base, d.Commits)
	c := d.Checklist
	if c.HasPackage {
		fmt.Fprintf(&b, "package: %d of %d required requests still have open criteria, %d unconfirmed human tasks, %d uncovered requirements\n", c.RequiredOpen, c.RequiredTotal, c.HumanTasks, c.Uncovered)
	} else {
		b.WriteString("package: none bound to this pull request\n")
	}
	for _, f := range d.Features {
		fmt.Fprintf(&b, "- [%s] %s\n", f.State, f.Text)
	}
	fmt.Fprintf(&b, "said about it: %d knowledge entries, %d ghost paths\n", len(d.Brief.Knowledge), len(d.Brief.Ghosts))
	return s.withCard(textResult(b.String()), d.Card), d, nil
}
