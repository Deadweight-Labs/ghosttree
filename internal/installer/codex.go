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

func InstallCodex(home string) ([]Change, error) {
	h := harnessNamed("codex")
	var changes []Change

	cfgPath := filepath.Join(home, ".codex", "config.toml")
	c, err := appendCodexMCP(cfgPath)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	// Codex reads the same hook shape as Claude Code from its own file. The
	// section it used to get instead — "call context_get yourself at session
	// start" — was written on the assumption that it had no hooks.
	hookChanges, err := installHooks(h, home)
	changes = append(changes, hookChanges...)
	if err != nil {
		return changes, err
	}
	// Ein geänderter Hook hat seine Freigabe verloren, ob wir es hinschreiben
	// oder nicht — Codex bindet sie an einen Hash der Definition. Der veraltete
	// Eintrag bliebe sonst stehen und liesse den Doctor grün melden für etwas,
	// das nicht mehr läuft.
	for _, c := range hookChanges {
		if strings.Contains(c.Action, "updated") {
			if err := dropStaleCodexTrust(cfgPath, h.HooksPath(home)); err != nil {
				return changes, err
			}
			break
		}
	}

	c, err = writeMarkerFile(h.RulePath(home), ruleForPath(h, h.RulePath(home)))
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	sc, err := installSkills(h, home)
	changes = append(changes, sc...)
	return changes, err
}

// installHooks registers every event-bearing channel the harness declares.
func installHooks(h Harness, home string) ([]Change, error) {
	if h.HooksPath == nil {
		return nil, nil
	}
	path := h.HooksPath(home)
	var changes []Change
	for _, channel := range h.Channels {
		event, command, matcher, ok := h.hookCommandFor(channel)
		if !ok {
			continue
		}
		c, err := addHook(path, event, command, matcher)
		if err != nil {
			return changes, err
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func harnessNamed(name string) Harness {
	for _, h := range Harnesses() {
		if h.Name == name {
			return h
		}
	}
	return Harness{Name: name}
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
