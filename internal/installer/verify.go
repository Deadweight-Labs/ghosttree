package installer

import (
	"os"
	"os/exec"
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
	mcpCheck := jsonEntryCheck("claude mcp registration", userCfg, "mcpServers", claudeMCPEntry(), "run '"+ctxRun()+" install claude'")
	mcpCheck.Component = ComponentMCP
	checks := []Check{mcpCheck}
	checks = append(checks, channelChecks(h, home)...)
	rule := ruleSectionCheck(h, "claude rule section", h.RulePath(home), "run '"+ctxRun()+" install claude'")
	rule.Component = ComponentRules
	checks = append(checks, rule)
	skill := skillCheck(h, "claude skills", home, "run '"+ctxRun()+" install claude'")
	skill.Component = ComponentSkills
	checks = append(checks, skill)

	// Claude Code reads CLAUDE_CONFIG_DIR/.claude.json when the variable is
	// set and ~/.claude.json when it is not. Launchers differ, so a home that
	// only has one of them registered works in one terminal and silently has
	// no ghosttree in another.
	if fallback := filepath.Join(home, ".claude.json"); fallback != userCfg {
		fallbackCheck := jsonEntryCheck("claude fallback config", fallback, "mcpServers", claudeMCPEntry(),
			"run 'CLAUDE_CONFIG_DIR= "+ctxRun()+" install claude' so launchers that ignore CLAUDE_CONFIG_DIR find it too")
		fallbackCheck.Component = ComponentMCP
		checks = append(checks, fallbackCheck)
	}
	return checks
}

func VerifyCodex(home string) []Check {
	h := harnessNamed("codex")
	mcpCheck := codexMCPCheck(filepath.Join(home, ".codex", "config.toml"), "run '"+ctxRun()+" install codex'")
	mcpCheck.Component = ComponentMCP
	checks := []Check{mcpCheck}
	checks = append(checks, channelChecks(h, home)...)
	// Nach dem Kanalcheck, weil er die Frage danach beantwortet: der Eintrag ist
	// da — läuft er auch?
	trust := codexTrustCheck(home)
	trust.Component = ComponentHooks
	checks = append(checks, trust)
	rule := ruleSectionCheck(h, "codex rule section", h.RulePath(home), "run '"+ctxRun()+" install codex'")
	rule.Component = ComponentRules
	checks = append(checks, rule)
	skill := skillCheck(h, "codex skills", home, "run '"+ctxRun()+" install codex'")
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
		"mcp", opencodeMCPEntry(), "run '"+ctxRun()+" install opencode'")
	mcpCheck.Component = ComponentMCP
	rule := ruleSectionCheck(h, "opencode rule section ("+filepath.Base(rulePath)+")", rulePath, "run '"+ctxRun()+" install opencode'")
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
	have := entries["ghosttree"]
	if jsonValuesEqual(have, want) {
		c.OK = true
		return c
	}
	if cmd := entryCommand(have); cmd != "" && jsonValuesEqual(have, withCommand(want, cmd)) {
		if why := commandProblem(cmd); why != "" {
			c.Detail = path + " (" + why + ")"
			return c
		}
		c.OK = true
		return c
	}
	c.Detail = path + " (ghosttree entry missing or outdated)"
	return c
}

// entryCommand reads the command word out of an MCP entry, whether the harness
// stores it as a string or as the first element of an argv list.
func entryCommand(entry any) string {
	m, _ := entry.(map[string]any)
	switch v := m["command"].(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			s, _ := v[0].(string)
			return s
		}
	}
	return ""
}

// withCommand returns the entry with its command word replaced.
func withCommand(entry map[string]any, command string) map[string]any {
	out := make(map[string]any, len(entry))
	for k, v := range entry {
		out[k] = v
	}
	if list, ok := entry["command"].([]any); ok && len(list) > 0 {
		replaced := append([]any{command}, list[1:]...)
		out["command"] = replaced
		return out
	}
	out["command"] = command
	return out
}

// commandProblem says why a configured way of starting ctx cannot work, or ""
// when it can. A bare name is only good while the PATH of the process that
// starts it contains ctx; the doctor can only judge its own PATH, which is
// still the best available evidence.
func commandProblem(command string) string {
	if !filepath.IsAbs(command) {
		if _, err := exec.LookPath(command); err != nil {
			return "runs '" + command + "', which is not on PATH; the harness cannot start it"
		}
		return ""
	}
	if info, err := os.Stat(command); err != nil || info.IsDir() {
		return "runs " + command + ", which does not exist"
	}
	return ""
}

func codexMCPCheck(path, fix string) Check {
	c := Check{Name: "codex mcp registration", Detail: path, Fix: fix}
	raw, err := os.ReadFile(path)
	if err != nil {
		c.Detail = path + " (missing)"
		return c
	}
	ranges := codexOwnedTableRanges(string(raw))
	if len(ranges) != 1 {
		c.Detail = path + " (ghosttree table missing, duplicate, or outdated)"
		return c
	}
	table := strings.TrimSpace(string(raw)[ranges[0][0]:ranges[0][1]])
	if table != strings.TrimSpace(codexMCPSection()) {
		cmd := codexTableCommand(table)
		if cmd == "" || table != strings.TrimSpace(codexMCPSectionFor(cmd)) {
			c.Detail = path + " (ghosttree table missing, duplicate, or outdated)"
			return c
		}
		if why := commandProblem(cmd); why != "" {
			c.Detail = path + " (" + why + ")"
			return c
		}
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
			"run '"+ctxRun()+" install "+h.Name+"' — this harness can fire the event and nothing is answering it")
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
	problem := ""
	for _, g := range groups {
		group, _ := g.(map[string]any)
		current, _ := group["matcher"].(string)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			handler, _ := h.(map[string]any)
			installed, _ := handler["command"].(string)
			if installed != command {
				if !sameCtxHook(installed, command) {
					continue
				}
				if why := hookCommandProblem(installed); why != "" {
					problem = why
					continue
				}
			}
			commandFound = true
			if current == matcher {
				c.OK = true
				return c
			}
		}
	}
	switch {
	case commandFound:
		c.Detail = path + " (wrong matcher)"
	case problem != "":
		c.Detail = path + " (" + problem + ")"
	}
	return c
}

// ruleSectionCheck compares only the managed section and accounts for
// path-specific harness behavior.
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

// hookCommandProblem judges the binary word of an installed hook command.
func hookCommandProblem(command string) string {
	words := splitCommand(command)
	if len(words) == 0 {
		return ""
	}
	return commandProblem(words[0])
}
