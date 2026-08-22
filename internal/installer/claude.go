package installer

import (
	"os"
	"path/filepath"
	"strings"
)

const hookCommand = "ctx hook session-start"

// ClaudeConfigDir resolves where Claude Code keeps its user config. Verified on
// Claude Code 2.1.234: MCP servers live in <dir>/.claude.json, not in
// settings.json, which ignores an mcpServers key entirely.
func ClaudeConfigDir(home string) string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return home
}

func ClaudeUserConfigPath(home string) string {
	return filepath.Join(ClaudeConfigDir(home), ".claude.json")
}

func InstallClaude(home string) ([]Change, error) {
	var changes []Change

	settings := filepath.Join(home, ".claude", "settings.json")
	c, err := addSessionStartHook(settings)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	c, err = registerClaudeMCP(ClaudeUserConfigPath(home))
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	c, err = writeMarkerFile(filepath.Join(home, ".claude", "CLAUDE.md"), ruleText)
	if err != nil {
		return changes, err
	}
	return append(changes, c), nil
}

func addSessionStartHook(path string) (Change, error) {
	settings, err := readJSONFile(path)
	if err != nil {
		return Change{}, err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["SessionStart"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, "ctx hook") {
				return Change{Path: path, Action: "unchanged"}, nil
			}
		}
	}
	entries = append(entries, map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": hookCommand}},
	})
	hooks["SessionStart"] = entries
	settings["hooks"] = hooks
	return Change{Path: path, Action: "hook added"}, writeJSONFile(path, settings)
}

func registerClaudeMCP(path string) (Change, error) {
	cfg, err := readJSONFile(path)
	if err != nil {
		return Change{}, err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if _, ok := servers["ghosttree"]; ok {
		return Change{Path: path, Action: "unchanged"}, nil
	}
	servers["ghosttree"] = map[string]any{
		"type":    "stdio",
		"command": "ctx",
		"args":    []any{"mcp"},
		"env":     map[string]any{},
	}
	cfg["mcpServers"] = servers
	return Change{Path: path, Action: "mcp server registered"}, writeJSONFile(path, cfg)
}
