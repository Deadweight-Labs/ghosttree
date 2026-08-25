package installer

import (
	"os"
	"path/filepath"
)

const (
	hookCommand        = "ctx hook session-start"
	promptHookCommand  = "ctx hook user-prompt-submit"
	preToolHookCommand = "ctx hook pre-tool-use"
)

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
	h := harnessNamed("claude")

	// Both event channels at once. They are separate on purpose: session-start
	// says what holds for the whole session, user-prompt-submit says what the
	// last sentence gave a reason to mention.
	changes, err := installHooks(h, home)
	if err != nil {
		return changes, err
	}

	c, err := registerClaudeMCP(ClaudeUserConfigPath(home))
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	c, err = writeMarkerFile(h.RulePath(home), ruleFor(h))
	if err != nil {
		return changes, err
	}
	return append(changes, c), nil
}

// addHook appends to whatever is already registered for an event. Other tools
// keep hooks here too — a lease daemon, an approval bridge — and replacing the
// list would silently disarm them.
func addHook(path, event, command, matcher string) (Change, error) {
	settings, err := readJSONFile(path)
	if err != nil {
		return Change{}, err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			// Matched on the exact command, not on "ctx hook": the two events
			// run different subcommands and one must not be mistaken for the
			// other, or installing the second would look like a no-op.
			if cmd, _ := hm["command"].(string); cmd == command {
				return Change{Path: path, Action: "unchanged"}, nil
			}
		}
	}
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	}
	// Der Matcher hält ctx aus jedem Bash-Aufruf heraus. Ohne ihn feuert
	// PreToolUse auf jedem Werkzeug, und ein Prozessstart je Aufruf ist ein
	// spürbarer Preis für eine Antwort, die es meistens nicht gibt.
	if matcher != "" {
		entry["matcher"] = matcher
	}
	entries = append(entries, entry)
	hooks[event] = entries
	settings["hooks"] = hooks
	return Change{Path: path, Action: event + " hook added"}, writeJSONFile(path, settings)
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
