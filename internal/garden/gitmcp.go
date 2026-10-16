package garden

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GitServerName is the MCP server name the session sees; its tools are
// mcp__gardengit__git_log and mcp__gardengit__git_show.
const GitServerName = "gardengit"

// GitToolNames are the tool names the session is allowed to call.
var GitToolNames = []string{"mcp__" + GitServerName + "__git_log", "mcp__" + GitServerName + "__git_show"}

// NewGitServer builds the MCP server with the two fixed, read-only git tools.
func NewGitServer(repo *Repo, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: GitServerName, Version: version}, nil)
	closed := false
	ann := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "git_log",
		Description: "Commit history of the checkout: one line per commit (hash, date, author, subject), newest first. Optional: path (a file or directory of the checkout, relative), max (1 to 100), pickaxe (commits that added or removed exactly this text).",
		Annotations: ann,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GitLogParams) (*mcp.CallToolResult, any, error) {
		out, err := repo.GitLog(ctx, in)
		return toolText(out, err), nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "git_show",
		Description: "One commit of the checkout with message, file statistics and patch. commit is a hash of 7 to 40 hex digits or HEAD; optional path limits the patch to a file or directory of the checkout.",
		Annotations: ann,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GitShowParams) (*mcp.CallToolResult, any, error) {
		out, err := repo.GitShow(ctx, in)
		return toolText(out, err), nil, nil
	})
	return srv
}

func toolText(out string, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
	}
	if out == "" {
		out = "(keine Ausgabe)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}
}

// RunGitServer serves the git tools on stdio until the session ends.
func RunGitServer(ctx context.Context, repo *Repo, version string) error {
	return NewGitServer(repo, version).Run(ctx, &mcp.StdioTransport{})
}
