// Package mcpserver exposes ghosttree to agents as four MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	client  *client.Client
	ctxAxes scope.Axes
}

func NewServer(c *client.Client, axes scope.Axes) *Server {
	return &Server{client: c, ctxAxes: axes}
}

type SearchInput struct {
	Query       string `json:"query" jsonschema:"what to search for"`
	Kind        string `json:"kind,omitempty" jsonschema:"knowledge, sessions or all (default all)"`
	AllBranches bool   `json:"all_branches,omitempty" jsonschema:"search across all branches of the project"`
	Machine     string `json:"machine,omitempty" jsonschema:"restrict to a machine hostname"`
}

type GetInput struct{}

type RememberInput struct {
	Type      string `json:"type" jsonschema:"pitfall, decision, note or plan"`
	Title     string `json:"title" jsonschema:"one line summary"`
	Body      string `json:"body" jsonschema:"the knowledge itself"`
	ScopeHint string `json:"scope_hint,omitempty" jsonschema:"project, branch, machine or global; omit to use the write defaults"`
}

type SessionsInput struct {
	Query     string `json:"query,omitempty" jsonschema:"full text search across session transcripts"`
	SessionID int64  `json:"session_id,omitempty" jsonschema:"read this session instead of listing"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum results (default 20)"`
}

func (s *Server) Register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_search",
		Description: "Search ghosttree knowledge and past session transcripts. Defaults to the current project, branch and machine context.",
	}, s.handleSearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_get",
		Description: "Get the context package for the current project, branch and machine: what is already known here.",
	}, s.handleGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_remember",
		Description: "Store a pitfall, decision, note or plan in ghosttree instead of writing it into the source tree.",
	}, s.handleRemember)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "context_sessions",
		Description: "List or search past agent sessions, or read one session's transcript.",
	}, s.handleSessions)
}

func Run(ctx context.Context, s *Server, version string) error {
	srv := mcp.NewServer(&mcp.Implementation{Name: "ghosttree", Version: version}, nil)
	s.Register(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func textResult(s string) *mcp.CallToolResult {
	if s == "" {
		s = "(no results)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (s *Server) handleSearch(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
	kind := in.Kind
	if kind == "" {
		kind = "all"
	}
	limit := 20
	var out strings.Builder

	if kind == "knowledge" || kind == "all" {
		ax := s.ctxAxes
		if in.AllBranches {
			ax.Branch = ""
		}
		if in.Machine != "" {
			ax.Machine = in.Machine
		}
		res, err := s.client.SearchUnion(in.Query, "knowledge", ax, limit)
		if err != nil {
			return nil, nil, err
		}
		if len(res.Knowledge) > 0 {
			out.WriteString("## knowledge\n")
			for _, k := range res.Knowledge {
				out.WriteString(renderKnowledge(k))
			}
		}
	}
	if kind == "sessions" || kind == "all" {
		// Sessions carry all three axes, so an exact filter on the full context
		// would only ever match this very session; project is the useful scope.
		filter := scope.Axes{Project: s.ctxAxes.Project, Machine: in.Machine}
		res, err := s.client.Search(in.Query, "sessions", filter, limit)
		if err != nil {
			return nil, nil, err
		}
		if len(res.Sessions) > 0 {
			out.WriteString("\n## sessions\n")
			for _, h := range res.Sessions {
				out.WriteString(renderHit(h))
			}
		}
	}
	return textResult(out.String()), nil, nil
}

func (s *Server) handleGet(ctx context.Context, _ *mcp.CallToolRequest, _ GetInput) (*mcp.CallToolResult, any, error) {
	md, err := s.client.Bootstrap(s.ctxAxes, 0)
	if err != nil {
		return nil, nil, err
	}
	return textResult(md), nil, nil
}

func (s *Server) handleRemember(ctx context.Context, _ *mcp.CallToolRequest, in RememberInput) (*mcp.CallToolResult, any, error) {
	if in.Type == "" || in.Title == "" {
		return nil, nil, fmt.Errorf("type and title are required")
	}
	k := store.Knowledge{Type: in.Type, Title: in.Title, Body: in.Body, Harness: "mcp"}
	autoCtx := s.ctxAxes
	switch in.ScopeHint {
	case "project":
		k.Scope = scope.Axes{Project: s.ctxAxes.Project}
	case "branch":
		k.Scope = scope.Axes{Project: s.ctxAxes.Project, Branch: s.ctxAxes.Branch}
	case "machine":
		k.Scope = scope.Axes{Machine: s.ctxAxes.Machine}
	case "global":
		// Empty scope plus empty context: the server's write defaults resolve
		// to global rather than filling anything in.
		autoCtx = scope.Axes{}
	}
	saved, err := s.client.Remember(k, autoCtx)
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("stored #%d [%s|%s]", saved.ID, saved.Type, scopeLabel(saved.Scope))), nil, nil
}

func (s *Server) handleSessions(ctx context.Context, _ *mcp.CallToolRequest, in SessionsInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if in.SessionID != 0 {
		chunks, err := s.client.ReadSession(in.SessionID, 0, limit)
		if err != nil {
			return nil, nil, err
		}
		var out strings.Builder
		for _, c := range chunks {
			if c.Text == "" {
				continue
			}
			fmt.Fprintf(&out, "### %s (%d)\n%s\n\n", c.Role, c.Seq, c.Text)
		}
		return textResult(out.String()), nil, nil
	}
	filter := scope.Axes{Project: s.ctxAxes.Project}
	if in.Query != "" {
		res, err := s.client.Search(in.Query, "sessions", filter, limit)
		if err != nil {
			return nil, nil, err
		}
		var out strings.Builder
		for _, h := range res.Sessions {
			out.WriteString(renderHit(h))
		}
		return textResult(out.String()), nil, nil
	}
	sessions, err := s.client.Sessions(filter, limit)
	if err != nil {
		return nil, nil, err
	}
	var out strings.Builder
	for _, se := range sessions {
		out.WriteString(renderSession(se))
	}
	return textResult(out.String()), nil, nil
}

func renderKnowledge(k store.Knowledge) string {
	return fmt.Sprintf("- [%s|%s|%s] %s — %s\n", k.Type, scopeLabel(k.Scope), k.Confidence, k.Title, oneLine(k.Body))
}

func renderSession(se store.Session) string {
	return fmt.Sprintf("- #%d %s %s %s (%s)\n", se.ID, se.Harness, se.Scope.Project, se.Scope.Branch, se.LastSeenAt)
}

func renderHit(h store.SessionHit) string {
	return fmt.Sprintf("- #%d %s %s %s (%s) — %s\n", h.Session.ID, h.Session.Harness,
		h.Session.Scope.Project, h.Session.Scope.Branch, h.Session.LastSeenAt, oneLine(h.Snippet))
}

func scopeLabel(ax scope.Axes) string {
	var parts []string
	if ax.Project != "" {
		p := ax.Project
		if ax.Branch != "" {
			p += "@" + ax.Branch
		}
		parts = append(parts, p)
	}
	if ax.Machine != "" {
		parts = append(parts, "machine:"+ax.Machine)
	}
	if len(parts) == 0 {
		return "global"
	}
	return strings.Join(parts, " ")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
