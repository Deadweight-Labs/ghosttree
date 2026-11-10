package mcpserver

import (
	"github.com/Deadweight-Labs/ghosttree/internal/coretools"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SetUsageHarness names the harness whose tool calls this process counts
// itself (REQ-436). Empty leaves the counting to the hooks: Claude Code's
// PostToolUse hook knows whether a subagent made the call, this process does
// not.
func (s *Server) SetUsageHarness(h string) { s.usageHarness = h }

// noteToolCall reports one call to the server, in the background. A call that
// never reached a tool (a protocol error) is not counted; one the tool refused
// is, because the agent did reach for it.
func (s *Server) noteToolCall(_ *mcp.CallToolRequest, name, action string, _ mcp.Result, err error) {
	if err != nil || s.usageHarness == "" || s.client == nil || s.ctxAxes.Project == "" || !store.ValidExternalID(s.sessionRef) {
		return
	}
	core, act, ok := coretools.Classify(name, action)
	if !ok {
		return
	}
	b := store.ToolUseBatch{Project: s.ctxAxes.Project, SessionRef: s.sessionRef, Harness: s.usageHarness,
		Uses: []store.ToolUse{{Tool: core, Action: act, Calls: 1}}}
	report := s.reportUses
	if report == nil {
		report = func(b store.ToolUseBatch) { _ = s.client.RecordToolUses(b) }
	}
	go report(b)
}
