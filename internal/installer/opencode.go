package installer

import (
	"path/filepath"
)

// InstallOpencode verdrahtet die einzige Verbindung, die opencode anbietet: den
// MCP-Server. Kein Hook wird eingetragen, weil es keinen gibt — opencode kennt
// nur JavaScript-Plugins, und ein Kanal, den ghosttree nicht ausliefert, gehört
// nicht in seine Deklaration.
//
// Die Regelsektion geht dorthin, wo opencode ohnehin liest. Siehe
// opencodeRulePath: eine neu angelegte globale AGENTS.md würde ihm den
// Ersatzweg auf ~/.claude/CLAUDE.md nehmen und damit alles, was dort sonst noch
// steht.
func InstallOpencode(home string) ([]Change, error) {
	selected, _ := ResolveComponents("opencode", nil)
	return installOpencodeSelected(home, selected)
}

func installOpencodeSelected(home string, selected ComponentSet) ([]Change, error) {
	h := harnessNamed("opencode")
	var changes []Change

	if selected[ComponentMCP] {
		c, err := registerOpencodeMCP(filepath.Join(home, ".config", "opencode", "opencode.json"))
		if err != nil {
			return changes, err
		}
		changes = append(changes, c)
	}
	if selected[ComponentRules] {
		c, err := writeMarkerFile(h.RulePath(home), ruleForPath(h, h.RulePath(home)))
		if err != nil {
			return changes, err
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// registerOpencodeMCP trägt den Server unter dem Schlüssel `mcp` ein, in der
// Form McpLocalConfig aus opencode.ai/config.json. Fremde Einträge und fremde
// Einstellungen der Datei bleiben stehen — dort steht auch anderer Leute
// Werkzeug, und sie zu ersetzen entwaffnet es still.
func registerOpencodeMCP(path string) (Change, error) {
	cfg, err := readJSONFile(path)
	if err != nil {
		return Change{}, err
	}
	servers, _ := cfg["mcp"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	want := opencodeMCPEntry()
	if jsonValuesEqual(servers["ghosttree"], want) {
		return Change{Path: path, Action: "unchanged"}, nil
	}
	servers["ghosttree"] = want
	cfg["mcp"] = servers
	return Change{Path: path, Action: "mcp server registered"}, writeJSONFile(path, cfg)
}

func opencodeMCPEntry() map[string]any {
	return map[string]any{
		"type":    "local",
		"command": []any{"ctx", "mcp"},
		"enabled": true,
	}
}
