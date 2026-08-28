package installer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/skills"
)

// Check is one inspected piece of harness wiring. Fix is filled in even when
// OK, so callers can show what a passing check is guarding.
type Check struct {
	Name       string    `json:"name"`
	Component  Component `json:"component,omitempty"`
	OK         bool      `json:"ok"`
	Unverified bool      `json:"unverified,omitempty"`
	Detail     string    `json:"detail"`
	Fix        string    `json:"fix"`
}

// VerifyClaude reports whether Claude Code is still wired to ghosttree.
// Installing is idempotent, but nothing re-checks afterwards that the harness
// still reads what we wrote: config files get rewritten by other tools, homes
// get migrated, CLAUDE_CONFIG_DIR appears.
func VerifyClaude(home string) []Check {
	h := harnessNamed("claude")
	userCfg := ClaudeUserConfigPath(home)
	mcpCheck := jsonEntryCheck("claude mcp registration", userCfg, "mcpServers", claudeMCPEntry(), "run 'ctx install claude'")
	mcpCheck.Component = ComponentMCP
	checks := []Check{mcpCheck}
	checks = append(checks, channelChecks(h, home)...)
	rule := ruleSectionCheck(h, "claude rule section", h.RulePath(home), "run 'ctx install claude'")
	rule.Component = ComponentRules
	checks = append(checks, rule)
	skill := skillCheck(h, "claude skills", home, "run 'ctx install claude'")
	skill.Component = ComponentSkills
	checks = append(checks, skill)

	// Claude Code reads CLAUDE_CONFIG_DIR/.claude.json when the variable is
	// set and ~/.claude.json when it is not. Launchers differ, so a home that
	// only has one of them registered works in one terminal and silently has
	// no ghosttree in another.
	if fallback := filepath.Join(home, ".claude.json"); fallback != userCfg {
		fallbackCheck := jsonEntryCheck("claude fallback config", fallback, "mcpServers", claudeMCPEntry(),
			"run 'CLAUDE_CONFIG_DIR= ctx install claude' so launchers that ignore CLAUDE_CONFIG_DIR find it too")
		fallbackCheck.Component = ComponentMCP
		checks = append(checks, fallbackCheck)
	}
	return checks
}

func VerifyCodex(home string) []Check {
	h := harnessNamed("codex")
	mcpCheck := codexMCPCheck(filepath.Join(home, ".codex", "config.toml"), "run 'ctx install codex'")
	mcpCheck.Component = ComponentMCP
	checks := []Check{mcpCheck}
	checks = append(checks, channelChecks(h, home)...)
	// Nach dem Kanalcheck, weil er die Frage danach beantwortet: der Eintrag ist
	// da — läuft er auch?
	trust := codexTrustCheck(home)
	trust.Component = ComponentHooks
	checks = append(checks, trust)
	rule := ruleSectionCheck(h, "codex rule section", h.RulePath(home), "run 'ctx install codex'")
	rule.Component = ComponentRules
	checks = append(checks, rule)
	skill := skillCheck(h, "codex skills", home, "run 'ctx install codex'")
	skill.Component = ComponentSkills
	return append(checks, skill)
}

// skillCheck answers two questions in this order: are the skills there at all,
// and do they still match what ctx wrote.
//
// The order matters and the first question is easy to forget. A drift check
// alone is green on a machine where nothing was ever installed — nothing
// differs from nothing. That is the same green-check-over-an-empty-channel that
// let Codex go 482 sessions without context, and an existing test caught it
// here before it shipped.
//
// Drift itself is not an error to fix, it is a fact to know. Running an adapted
// skill is allowed and the installer protects it; running one without knowing
// is the failure, because updates then skip it silently.
func skillCheck(h Harness, name, home, fix string) Check {
	if h.SkillsRoot == nil {
		// Not an absence to report: this harness has no skill channel at all,
		// and a permanently red check for something a harness does not offer
		// teaches people to skim past red checks.
		return Check{Name: name, OK: true, Detail: "not offered by this harness", Fix: fix}
	}
	root := h.SkillsRoot(home)
	c := Check{Name: name, Detail: root, Fix: fix}

	var missing []string
	for _, skill := range skills.Names() {
		files, err := skills.Files(skill)
		if err != nil {
			return c
		}
		for rel := range files {
			if _, err := os.Stat(filepath.Join(root, skill, filepath.FromSlash(rel))); err != nil {
				missing = append(missing, skill+"/"+rel)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		c.Detail = root + " (not installed: " + strings.Join(missing, ", ") + ")"
		return c
	}

	manifest := readManifest()
	var unowned, invalidFrontmatter []string
	for _, skill := range skills.Names() {
		files, err := skills.Files(skill)
		if err != nil {
			return c
		}
		for rel := range files {
			target := filepath.Join(root, skill, filepath.FromSlash(rel))
			if manifest[target] == "" {
				unowned = append(unowned, skill+"/"+rel)
			}
			if rel == "SKILL.md" {
				raw, err := os.ReadFile(target)
				if err != nil || !validSkillFrontmatter(raw) {
					invalidFrontmatter = append(invalidFrontmatter, skill+"/"+rel)
				}
			}
		}
	}
	if len(invalidFrontmatter) > 0 {
		sort.Strings(invalidFrontmatter)
		c.Detail = root + " (invalid SKILL.md frontmatter: " + strings.Join(invalidFrontmatter, ", ") + ")"
		return c
	}
	if len(unowned) > 0 {
		sort.Strings(unowned)
		c.Detail = root + " (ownership manifest missing: " + strings.Join(unowned, ", ") + ")"
		return c
	}

	drift := SkillDrift(h, home)
	if len(drift) == 0 {
		c.OK = true
		return c
	}
	short := make([]string, 0, len(drift))
	for _, p := range drift {
		short = append(short, strings.TrimPrefix(p, root+string(filepath.Separator)))
	}
	c.Detail = root + " (yours, not ours: " + strings.Join(short, ", ") + " — updates will skip them)"
	return c
}

func validSkillFrontmatter(raw []byte) bool {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return false
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return false
	}
	frontmatter := "\n" + text[4:4+end] + "\n"
	return strings.Contains(frontmatter, "\nname:") && strings.Contains(frontmatter, "\ndescription:")
}

// VerifyOpencode prüft die einzige Verbindung, die es hier gibt. Kein
// Kanalcheck, weil opencode keine Hooks hat — und keine erfundene Prüfung, die
// dauerhaft rot stünde für etwas, das die Umgebung nicht anbietet.
//
// Die Regelsektion wird an dem Pfad geprüft, den opencodeRulePath ermittelt:
// dieselbe Datei, in die installiert wurde. Wer stattdessen fest auf die globale
// AGENTS.md prüfte, meldete rot, obwohl die Sektion dort steht, wo opencode sie
// tatsächlich liest.
func VerifyOpencode(home string) []Check {
	h := harnessNamed("opencode")
	rulePath := h.RulePath(home)
	mcpCheck := jsonEntryCheck("opencode mcp registration",
		filepath.Join(home, ".config", "opencode", "opencode.json"),
		"mcp", opencodeMCPEntry(), "run 'ctx install opencode'")
	mcpCheck.Component = ComponentMCP
	rule := ruleSectionCheck(h, "opencode rule section ("+filepath.Base(rulePath)+")", rulePath, "run 'ctx install opencode'")
	rule.Component = ComponentRules
	return []Check{mcpCheck, rule}
}

func jsonEntryCheck(name, path, container string, want map[string]any, fix string) Check {
	c := Check{Name: name, Detail: path, Fix: fix}
	cfg, err := readJSONFile(path)
	if err != nil {
		c.Detail = path + " (missing or invalid)"
		return c
	}
	entries, _ := cfg[container].(map[string]any)
	if !jsonValuesEqual(entries["ghosttree"], want) {
		c.Detail = path + " (ghosttree entry missing or outdated)"
		return c
	}
	c.OK = true
	return c
}

func codexMCPCheck(path, fix string) Check {
	c := Check{Name: "codex mcp registration", Detail: path, Fix: fix}
	raw, err := os.ReadFile(path)
	if err != nil {
		c.Detail = path + " (missing)"
		return c
	}
	ranges := codexOwnedTableRanges(string(raw))
	if len(ranges) != 1 || strings.TrimSpace(string(raw)[ranges[0][0]:ranges[0][1]]) != strings.TrimSpace(codexMCPSection) {
		c.Detail = path + " (ghosttree table missing, duplicate, or outdated)"
		return c
	}
	c.OK = true
	return c
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
		event, command, matcher, ok := h.hookCommandFor(channel)
		if !ok {
			continue
		}
		check := hookCheck(h.Name+" "+string(channel)+" hook", path, event, command, matcher,
			"run 'ctx install "+h.Name+"' — this harness can fire the event and nothing is answering it")
		check.Component = ComponentHooks
		checks = append(checks, check)
	}
	return checks
}

func hookCheck(name, path, event, command, matcher, fix string) Check {
	c := Check{Name: name, Fix: fix, Detail: path}
	settings, err := readJSONFile(path)
	if err != nil {
		c.Detail = path + " (missing or invalid)"
		return c
	}
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	commandFound := false
	for _, g := range groups {
		group, _ := g.(map[string]any)
		current, _ := group["matcher"].(string)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			handler, _ := h.(map[string]any)
			if handler["command"] != command {
				continue
			}
			commandFound = true
			if current == matcher {
				c.OK = true
				return c
			}
		}
	}
	if commandFound {
		c.Detail = path + " (wrong matcher)"
	}
	return c
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
// Verglichen wird gegen ruleForPath(h, path) und nicht gegen ruleText: eine Umgebung, bei
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
	case strings.TrimSpace(got) != strings.TrimSpace(ruleForPath(h, path)):
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
