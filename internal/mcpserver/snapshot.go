package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const snapshotToolLimit = 100

type SnapshotCreateInput struct {
	Name       string `json:"name" jsonschema:"required"`
	Message    string `json:"message,omitempty"`
	AllowDirty bool   `json:"allow_dirty,omitempty"`
}

type SnapshotListInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type SnapshotGetInput struct {
	Name   string `json:"name" jsonschema:"required"`
	Domain string `json:"domain,omitempty"`
	Key    string `json:"key,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (s *Server) handleSnapshotCreate(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotCreateInput) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if s.ctxAxes.Project == "" || s.repoRoot == "" {
		return nil, nil, fmt.Errorf("context snapshot create requires a bound project and repository")
	}
	git, err := collector.ResolveSnapshotGit(s.repoRoot, in.Name, in.AllowDirty)
	if err != nil {
		return nil, nil, err
	}
	if err := collector.RecheckSnapshotGit(s.repoRoot, in.Name, git); err != nil {
		return nil, nil, err
	}
	input := snapshot.CreateInput{Project: s.ctxAxes.Project, Name: in.Name, Git: git}
	if in.Message != "" {
		input.Message = &in.Message
	}
	if s.sessionRef != "" {
		input.SessionRef = &s.sessionRef
	}
	result, err := s.client.CreateContextSnapshot(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if s.afterSnapshot != nil {
		if err := s.afterSnapshot(ctx, s.ctxAxes.Project); err != nil {
			result.Warnings = append(result.Warnings, snapshot.Warning{Code: "snapshot_mirror_degraded", Message: err.Error()})
		}
	}
	return snapshotJSONResult(result)
}

func (s *Server) handleSnapshotList(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotListInput) (*mcp.CallToolResult, any, error) {
	limit, err := boundedSnapshotLimit(in.Limit)
	if err != nil {
		return nil, nil, err
	}
	page, err := s.client.ContextSnapshots(ctx, snapshot.ListFilter{Project: s.ctxAxes.Project, Cursor: in.Cursor, Limit: limit})
	if err != nil {
		return nil, nil, err
	}
	return snapshotJSONResult(page)
}

func (s *Server) handleSnapshotGet(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotGetInput) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if in.Key != "" && in.Domain == "" {
		return nil, nil, fmt.Errorf("key requires domain")
	}
	if in.Domain == "" {
		head, counts, err := s.client.ContextSnapshot(ctx, s.ctxAxes.Project, in.Name)
		if err != nil {
			return nil, nil, err
		}
		return snapshotJSONResult(struct {
			Snapshot snapshot.Head    `json:"snapshot"`
			Counts   map[string]int64 `json:"counts"`
		}{head, counts})
	}
	limit, err := boundedSnapshotLimit(in.Limit)
	if err != nil {
		return nil, nil, err
	}
	page, err := s.client.ContextSnapshotEntries(ctx, s.ctxAxes.Project, in.Name, snapshot.EntryFilter{Domain: in.Domain, Key: in.Key, Cursor: in.Cursor, Limit: limit})
	if err != nil {
		return nil, nil, err
	}
	return snapshotJSONResult(page)
}

func boundedSnapshotLimit(limit int) (int, error) {
	if limit == 0 {
		return snapshotToolLimit, nil
	}
	if limit < 0 || limit > snapshotToolLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", snapshotToolLimit)
	}
	return limit, nil
}

func snapshotJSONResult(value any) (*mcp.CallToolResult, any, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return textResult(string(raw)), nil, nil
}
