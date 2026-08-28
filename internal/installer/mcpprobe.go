package installer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var requiredMCPTools = []string{"context_get", "context_remember", "context_search"}

var codexEffectiveProbe = runCodexEffectiveProbe

func mcpRuntimeCheck(harness string, configOK bool) Check {
	c := Check{
		Name:      harness + " mcp runtime",
		Component: ComponentMCP,
		Detail:    "initialize, tools/list, context_get via configured stdio command",
		Fix:       "run 'ctx install " + harness + " --only mcp' and check client/server configuration",
	}
	if !configOK {
		c.Detail = "not started because the configured ghosttree entry is invalid"
		return c
	}
	cmd := exec.Command("ctx", "mcp")
	if err := probeMCPCommand(cmd); err != nil {
		c.Detail = err.Error()
		return c
	}
	c.OK = true
	return c
}

func codexEffectiveMCPCheck(home string, configOK bool) Check {
	c := Check{
		Name:      "codex mcp effective config",
		Component: ComponentMCP,
		Detail:    "codex mcp get ghosttree",
		Fix:       "repair ~/.codex/config.toml or run 'ctx install codex --only mcp'",
	}
	if !configOK {
		c.Detail = "not checked because the focused ghosttree table is invalid"
		return c
	}
	available, err := codexEffectiveProbe(home)
	if !available {
		c.Unverified = true
		c.Detail = "codex CLI is not available on PATH"
		return c
	}
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	c.OK = true
	return c
}

func runCodexEffectiveProbe(home string) (bool, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "mcp", "get", "ghosttree")
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{
		"HOME":       home,
		"CODEX_HOME": filepath.Join(home, ".codex"),
	})
	raw, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return true, fmt.Errorf("codex timed out while parsing its effective MCP config")
		}
		return true, fmt.Errorf("codex rejected its effective MCP config: %v: %s", err, bounded(string(raw), 512))
	}
	text := string(raw)
	for _, want := range []string{"enabled: true", "command: ctx", "args: mcp"} {
		if !strings.Contains(text, want) {
			return true, fmt.Errorf("codex effective MCP entry is missing %q: %s", want, bounded(text, 512))
		}
	}
	return true, nil
}

func replaceEnvironment(current []string, values map[string]string) []string {
	out := make([]string, 0, len(current)+len(values))
	for _, entry := range current {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, replaced := values[key]; !replaced {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func probeMCPCommand(cmd *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "ghosttree-doctor", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
	if err != nil {
		return fmt.Errorf("MCP initialize failed: %v: %s", err, bounded(stderr.String(), 512))
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("MCP tools/list failed: %v: %s", err, bounded(stderr.String(), 512))
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
	}
	var missing []string
	for _, name := range requiredMCPTools {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("MCP tools/list missing %s", strings.Join(missing, ", "))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "context_get", Arguments: map[string]any{}})
	if err != nil {
		return fmt.Errorf("MCP context_get failed: %v: %s", err, bounded(stderr.String(), 512))
	}
	if result.IsError {
		return fmt.Errorf("MCP context_get returned an error result")
	}
	return nil
}
