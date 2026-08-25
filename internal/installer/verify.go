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
	h := harnessNamed("claude")
	userCfg := ClaudeUserConfigPath(home)
	checks := []Check{fileCheck("claude mcp registration", userCfg, `"ghosttree"`,
		"run 'ctx install claude'")}
	checks = append(checks, channelChecks(h, home)...)
	checks = append(checks, ruleSectionCheck(h, "claude rule section", h.RulePath(home),
		"run 'ctx install claude'"))

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
	h := harnessNamed("codex")
	checks := []Check{fileCheck("codex mcp registration", filepath.Join(home, ".codex", "config.toml"),
		"[mcp_servers.ghosttree]", "run 'ctx install codex'")}
	checks = append(checks, channelChecks(h, home)...)
	return append(checks, ruleSectionCheck(h, "codex rule section", h.RulePath(home),
		"run 'ctx install codex'"))
}

// channelChecks asks what the harness is capable of, not what happens to be in
// its config. A check built from the file finds nothing to complain about when
// ghosttree never wired the channel at all — which is how Codex showed two
// green ticks for 482 sessions while its session-start channel stood open and
// unused. Iterating the declared channels means an unserved one is a failing
// check with a name, not an absence nobody looks for.
func channelChecks(h Harness, home string) []Check {
	if h.HooksPath == nil {
		return nil
	}
	path := h.HooksPath(home)
	var checks []Check
	for _, channel := range h.Channels {
		_, command, _, ok := hookCommandFor(channel)
		if !ok {
			continue
		}
		checks = append(checks, fileCheck(h.Name+" "+string(channel)+" hook", path, command,
			"run 'ctx install "+h.Name+"' — this harness can fire the event and nothing is answering it"))
	}
	return checks
}

// ruleSectionCheck vergleicht den Inhalt des Regelabschnitts, nicht bloss seine
// Anwesenheit.
//
// Der Grund ist ein eigener Fehlschlag (Pitfall #1238): nach einer Änderung an
// ruleText trug jede schon eingerichtete Maschine weiter den alten Text, und
// fileCheck — das nur nach markerStart sucht — meldete dafür einen grünen
// Haken. Der veraltete Satz wurde jeder Sitzung als Anweisung mitgegeben und
// beschrieb eine Dateiform, die es nicht mehr gab. Genau die Art Drift, für die
// doctor gebaut ist, und die einzige, die es nicht sah.
//
// Verglichen wird nur zwischen den Markern. Was davor und dahinter steht,
// gehört anderen Werkzeugen und geht uns nichts an.
//
// Verglichen wird gegen ruleFor(h) und nicht gegen ruleText: eine Umgebung, bei
// der der Sitzungsbeginn nachweislich nichts ausliefert, bekommt einen Absatz
// mehr. Gegen ruleText zu prüfen erklärte genau diese Umgebungen für dauerhaft
// veraltet.
func ruleSectionCheck(h Harness, name, path, fix string) Check {
	c := Check{Name: name, Fix: fix, Detail: path}
	b, err := os.ReadFile(path)
	if err != nil {
		c.Detail = path + " (missing)"
		return c
	}
	got, ok := extractSection(string(b))
	switch {
	case !ok:
		c.Detail = path + " (no ghosttree entry)"
	case strings.TrimSpace(got) != strings.TrimSpace(ruleFor(h)):
		c.Detail = path + " (rule text is outdated)"
	default:
		c.OK = true
	}
	return c
}

// extractSection gibt zurück, was zwischen den Markern steht.
func extractSection(content string) (string, bool) {
	start := strings.Index(content, markerStart)
	end := strings.Index(content, markerEnd)
	if start < 0 || end <= start {
		return "", false
	}
	return content[start+len(markerStart) : end], true
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
