package installer

import (
	"os"
	"path/filepath"
	"strings"
)

// Check is one inspected piece of harness wiring. Fix is filled in even when
// OK, so callers can show what a passing check is guarding.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// VerifyClaude reports whether Claude Code is still wired to ghosttree.
// Installing is idempotent, but nothing re-checks afterwards that the harness
// still reads what we wrote: config files get rewritten by other tools, homes
// get migrated, CLAUDE_CONFIG_DIR appears.
func VerifyClaude(home string) []Check {
	userCfg := ClaudeUserConfigPath(home)
	checks := []Check{
		fileCheck("claude mcp registration", userCfg, `"ghosttree"`,
			"run 'ctx install claude'"),
		fileCheck("claude session-start hook", filepath.Join(home, ".claude", "settings.json"), hookCommand,
			"run 'ctx install claude'"),
		fileCheck("claude rule section", filepath.Join(home, ".claude", "CLAUDE.md"), markerStart,
			"run 'ctx install claude'"),
	}

	// Claude Code reads CLAUDE_CONFIG_DIR/.claude.json when the variable is
	// set and ~/.claude.json when it is not. Launchers differ, so a home that
	// only has one of them registered works in one terminal and silently has
	// no ghosttree in another.
	if fallback := filepath.Join(home, ".claude.json"); fallback != userCfg {
		checks = append(checks, fileCheck("claude fallback config", fallback, `"ghosttree"`,
			"run 'CLAUDE_CONFIG_DIR= ctx install claude' so launchers that ignore CLAUDE_CONFIG_DIR find it too"))
	}
	return checks
}

func VerifyCodex(home string) []Check {
	return []Check{
		fileCheck("codex mcp registration", filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.ghosttree]",
			"run 'ctx install codex'"),
		fileCheck("codex rule section", filepath.Join(home, ".codex", "AGENTS.md"), markerStart,
			"run 'ctx install codex'"),
	}
}

func fileCheck(name, path, needle, fix string) Check {
	c := Check{Name: name, Fix: fix, Detail: path}
	b, err := os.ReadFile(path)
	switch {
	case err != nil:
		c.Detail = path + " (missing)"
	case !strings.Contains(string(b), needle):
		c.Detail = path + " (no ghosttree entry)"
	default:
		c.OK = true
	}
	return c
}
