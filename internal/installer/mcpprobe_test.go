package installer

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPProbeCompletesHandshakeListsToolsAndCallsContextGet(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPProbeHelperProcess$")
	cmd.Env = append(os.Environ(), "GHOSTTREE_MCP_PROBE_HELPER=complete")
	if err := probeMCPCommand(cmd); err != nil {
		t.Fatal(err)
	}
}

func TestMCPProbeRejectsMissingRequiredTool(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPProbeHelperProcess$")
	cmd.Env = append(os.Environ(), "GHOSTTREE_MCP_PROBE_HELPER=missing")
	err := probeMCPCommand(cmd)
	if err == nil || !strings.Contains(err.Error(), "context_remember") {
		t.Fatalf("err = %v", err)
	}
}

func TestMCPProbeHelperProcess(t *testing.T) {
	mode := os.Getenv("GHOSTTREE_MCP_PROBE_HELPER")
	if mode == "" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "probe-helper", Version: "1"}, nil)
	names := []string{"context_get", "context_search", "context_remember"}
	if mode == "missing" {
		names = names[:2]
	}
	for _, name := range names {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		})
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
