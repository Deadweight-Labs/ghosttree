package installer

import (
	"os"
	"path/filepath"
	"strings"
)

const codexMCPSection = `
[mcp_servers.ghosttree]
command = "ctx"
args = ["mcp"]
`

// codexRule adds the session-start instruction: codex has no hook mechanism,
// so it has to be told to fetch the context itself.
const codexRule = ruleText + `

At session start call the ` + "`context_get`" + ` tool once to load project context.`

func InstallCodex(home string) ([]Change, error) {
	var changes []Change

	cfgPath := filepath.Join(home, ".codex", "config.toml")
	c, err := appendCodexMCP(cfgPath)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	c, err = writeMarkerFile(filepath.Join(home, ".codex", "AGENTS.md"), codexRule)
	if err != nil {
		return changes, err
	}
	return append(changes, c), nil
}

func appendCodexMCP(path string) (Change, error) {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Change{}, err
	}
	if strings.Contains(string(old), "[mcp_servers.ghosttree]") {
		return Change{Path: path, Action: "unchanged"}, nil
	}
	content := string(old)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Change{}, err
	}
	action := "updated"
	if len(old) == 0 {
		action = "created"
	}
	return Change{Path: path, Action: action}, writeAtomic(path, []byte(content+codexMCPSection), 0o644)
}
