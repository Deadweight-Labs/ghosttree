package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCodexIdempotent(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5\"\n"), 0o644)
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Count(string(cfg), "[mcp_servers.ghosttree]") != 1 {
		t.Errorf("config.toml must contain exactly one ghosttree section:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "model = \"gpt-5\"") {
		t.Error("existing config content must be preserved")
	}
	agents, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	agentRules := strings.Join(strings.Fields(strings.ToLower(string(agents))), " ")
	if strings.Count(string(agents), "<!-- ghosttree:start -->") != 1 {
		t.Errorf("AGENTS.md marker section wrong:\n%s", agents)
	}
	if !strings.Contains(string(agents), "context_get") {
		t.Errorf("AGENTS.md must tell codex to call context_get:\n%s", agents)
	}
	for _, want := range []string{"request_search", "substantial", "trivial local fixes", "acceptance criteria", "evidence"} {
		if !strings.Contains(agentRules, want) {
			t.Errorf("AGENTS.md missing request-ledger guidance %q:\n%s", want, agents)
		}
	}
	for _, want := range []string{"repository-relative paths", "another subtree"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md missing refresh guidance %q:\n%s", want, agents)
		}
	}
	// Die Task-Tags standen hier bis zum 2026-08-25, obwohl das Task-Gate längst
	// gestrichen war (Entscheidung #150, gemessen: 17 Pfad-Gates gegen 0
	// Task-Gates). Ein Codex-Prüflauf hat es gefunden: der Regeltext, der in JEDE
	// Sitzung geht, wies Agenten auf eine API, die es nicht mehr gibt.
	for _, gone := range []string{"task tag", "explicit task"} {
		if strings.Contains(string(agents), gone) {
			t.Errorf("the rule text still promises the removed task gate (%q):\n%s", gone, agents)
		}
	}
}

func TestInstalledRulesKeepPlansAndSpecsOutOfGit(t *testing.T) {
	for _, want := range []string{"ctx doc import", ".ghosttree/edit/", "never commit specs or plans"} {
		if !strings.Contains(ruleText, want) {
			t.Errorf("ruleText missing %q", want)
		}
	}
}

func TestInstallClaudePreservesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"),
		[]byte(`{"model":"opus","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"other-tool"}]}]}}`), 0o644)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	s := string(b)
	for _, want := range []string{`"model"`, "other-tool", "ctx hook session-start"} {
		if !strings.Contains(s, want) {
			t.Errorf("settings.json missing %q:\n%s", want, s)
		}
	}
	InstallClaude(home) // idempotent
	b, _ = os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Count(string(b), "ctx hook session-start") != 1 {
		t.Error("hook must not be duplicated")
	}
}

// Verified on Claude Code 2.1.234: mcpServers in settings.json is ignored,
// `claude mcp add --scope user` writes <config dir>/.claude.json.
func TestInstallClaudeRegistersMCPInUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups":7}`), 0o644)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	InstallClaude(home)
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("user config is no longer valid json: %v", err)
	}
	if cfg["numStartups"] == nil {
		t.Error("unrelated keys must survive")
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	gt, _ := servers["ghosttree"].(map[string]any)
	if gt == nil || gt["command"] != "ctx" {
		t.Fatalf("ghosttree not registered: %v", cfg["mcpServers"])
	}
}

func TestInstallClaudeConfigDirOverride(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	os.MkdirAll(dir, 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude.json")); err != nil {
		t.Errorf("with CLAUDE_CONFIG_DIR set the user config lives there: %v", err)
	}
}

func TestInstallClaudeRuleSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("# mine\n\nkeep me\n"), 0o644)
	InstallClaude(home)
	InstallClaude(home)
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	s := string(b)
	if !strings.Contains(s, "keep me") {
		t.Error("existing CLAUDE.md content must survive")
	}
	if strings.Count(s, "<!-- ghosttree:start -->") != 1 {
		t.Errorf("rule section must appear once:\n%s", s)
	}
	if !strings.Contains(s, "context_remember") {
		t.Errorf("rule section must mention context_remember:\n%s", s)
	}
}

func failing(checks []Check) []string {
	var names []string
	for _, c := range checks {
		if !c.OK && !c.Unverified {
			names = append(names, c.Name)
		}
	}
	return names
}

func TestVerifyClaudeFailsOnFreshHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	checks := VerifyClaude(home)
	if len(checks) == 0 {
		t.Fatal("verify must report checks, not an empty list")
	}
	if len(failing(checks)) != len(checks) {
		t.Errorf("nothing is installed, so every check should fail: %+v", checks)
	}
	for _, c := range checks {
		if c.Fix == "" {
			t.Errorf("failing check %q must tell the user how to fix it", c.Name)
		}
	}
}

func TestVerifyClaudePassesAfterInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	if bad := failing(VerifyClaude(home)); len(bad) != 0 {
		t.Errorf("after install these still fail: %v", bad)
	}
}

// Pitfall #1238: doctor prüfte nur, OB die Marker dastehen, nicht WAS
// dazwischen steht. Nach einer Änderung an ruleText trug jede Maschine weiter
// den alten Text — und doctor, das genau für diese Art Drift gebaut ist, zeigte
// einen grünen Haken.
func TestVerifyClaudeDetectsAnOutdatedRuleText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	path := harnessNamed("claude").RulePath(home)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(b), "context_remember", "context_veraltet", 1)
	if stale == string(b) {
		t.Fatal("der Testtext muss den Regelabschnitt wirklich verändern")
	}
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	bad := failing(VerifyClaude(home))
	if len(bad) != 1 || !strings.Contains(bad[0], "rule section") {
		t.Fatalf("want exactly the rule section check to fail, got %v", bad)
	}

	// Und wieder grün, sobald der Installer ihn ersetzt hat.
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	if bad := failing(VerifyClaude(home)); len(bad) != 0 {
		t.Fatalf("nach dem Neuinstallieren muss es wieder passen: %v", bad)
	}
}

// Fremder Text um den Abschnitt herum ist der Normalfall — in CLAUDE.md steht
// auch anderer Leute Werkzeug. Nur der Abschnitt zwischen den Markern zählt.
func TestRuleSectionCheckIgnoresWhatIsAroundIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	path := harnessNamed("claude").RulePath(home)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte("# Meine eigenen Regeln\n\nNicht anfassen.\n\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	if bad := failing(VerifyClaude(home)); len(bad) != 0 {
		t.Fatalf("fremder Text daneben ist kein Drift: %v", bad)
	}
}

// The pitfall doctor exists for: with CLAUDE_CONFIG_DIR set, the installer
// writes only there, but launchers that do not set it read ~/.claude.json and
// see no ghosttree at all.
func TestVerifyClaudeDetectsUnregisteredFallbackConfig(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "cfgdir")
	os.MkdirAll(dir, 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	bad := failing(VerifyClaude(home))
	if len(bad) != 1 || !strings.Contains(bad[0], "fallback") {
		t.Errorf("want exactly the fallback config check to fail, got %v", bad)
	}
}

func TestVerifyCodex(t *testing.T) {
	home := t.TempDir()
	if bad := failing(VerifyCodex(home)); len(bad) == 0 {
		t.Error("fresh home: codex checks must fail")
	}
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	// Alles ausser der Vertrauensbestätigung: die erteilt ein Mensch in Codex'
	// Oberfläche über /hooks, und genau deshalb gibt es dafür einen eigenen
	// Check — installiert ist nicht dasselbe wie betriebsbereit.
	var bad []string
	for _, c := range failing(VerifyCodex(home)) {
		if !strings.Contains(c, "trust") {
			bad = append(bad, c)
		}
	}
	if len(bad) != 0 {
		t.Errorf("after install these still fail: %v", bad)
	}
}

func TestReplaceSection(t *testing.T) {
	got := replaceSection("a\n<!-- ghosttree:start -->\nold\n<!-- ghosttree:end -->\nb\n", "new")
	if strings.Contains(got, "old") || !strings.Contains(got, "new") {
		t.Errorf("section not replaced: %q", got)
	}
	if !strings.HasPrefix(got, "a\n") || !strings.HasSuffix(got, "b\n") {
		t.Errorf("surrounding content damaged: %q", got)
	}
}

// Other tools keep hooks in the same file — a lease daemon on SessionEnd, an
// approval bridge on PreToolUse, a WebFetch guard. Installing must add to that
// list, never replace it.
func TestInstallKeepsForeignHooks(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"bash /opt/example/session-lease.sh"}]}],` +
		`"PreToolUse":[{"matcher":"WebFetch","hooks":[{"type":"command","command":"exit 2"}]}]}}`
	if err := os.WriteFile(settings, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		"example/session-lease.sh", "WebFetch",
		"ctx hook session-start", "ctx hook user-prompt-submit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing after install:\n%s", want, got)
		}
	}
}

// Installing twice must not stack duplicates of either hook.
func TestInstallHooksAreIdempotent(t *testing.T) {
	home := t.TempDir()
	for range 2 {
		if _, err := InstallClaude(home); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	for _, cmd := range []string{"ctx hook session-start", "ctx hook user-prompt-submit"} {
		if n := strings.Count(string(raw), cmd); n != 1 {
			t.Errorf("%q registered %d times, want 1", cmd, n)
		}
	}
}

// Kriterium 6 von REQ-98: die Regel verbietet keinen Kommentar mehr, sie nennt
// einen Ort. Solange es keinen Alternativort gab, war der Kommentar der einzige
// mögliche Platz, und ihn zu verbieten hiess, die Information zu vernichten.
func TestRuleTextNamesTheAlternativePlaceInsteadOfForbiddingComments(t *testing.T) {
	if !strings.Contains(ruleText, "context_describe_file") {
		t.Fatal("the rule must name the tool that replaces the comment")
	}
	if !strings.Contains(ruleText, ".ghosttree/tree/") {
		t.Fatal("the rule must say where the tree is browsable")
	}
	if strings.Contains(ruleText, "never into source comments") {
		t.Fatal("the blanket prohibition must be gone; the rule redirects instead of forbidding")
	}
}
