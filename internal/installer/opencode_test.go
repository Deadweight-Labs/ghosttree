package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opencode ist die erste Umgebung ohne kommandobasierten Hook. Sie hat Plugins,
// aber die sind JavaScript und liefern nicht nachweislich Text in den Kontext —
// eine Behauptung in Delivers gehört gemessen, bevor sie dort steht.
func TestOpencodeIsDeclaredAsAPullOnlyHarness(t *testing.T) {
	h := harnessNamed("opencode")
	if h.Name != "opencode" {
		t.Fatal("opencode is not among the declared harnesses")
	}
	if h.HooksPath != nil {
		t.Error("opencode has no command hooks; a hooks path would invent one")
	}
	if !h.Serves(ChannelMCP) || !h.DeliversContext(ChannelMCP) {
		t.Error("opencode's MCP channel is the one that demonstrably works")
	}
	for _, c := range []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelPreToolUse} {
		if h.Serves(c) {
			t.Errorf("opencode does not offer %s", c)
		}
	}
}

// Der teuerste Fehler, den die Installation hier machen könnte: opencode liest
// ~/.claude/CLAUDE.md, SOLANGE keine globale AGENTS.md existiert. Eine anzulegen
// nimmt ihm alles andere weg, was dort steht — und der Doctor meldete danach
// grün (Wissen #1427).
func TestOpencodeRuleGoesWhereItAlreadyReadsInsteadOfCuttingTheFallback(t *testing.T) {
	h := harnessNamed("opencode")

	t.Run("globale AGENTS.md vorhanden", func(t *testing.T) {
		home := t.TempDir()
		agents := filepath.Join(home, ".config", "opencode", "AGENTS.md")
		os.MkdirAll(filepath.Dir(agents), 0o755)
		os.WriteFile(agents, []byte("# meine Regeln\n"), 0o644)
		if got := h.RulePath(home); got != agents {
			t.Errorf("rule path = %s, want the file opencode already reads", got)
		}
	})

	t.Run("nur CLAUDE.md vorhanden", func(t *testing.T) {
		home := t.TempDir()
		claude := filepath.Join(home, ".claude", "CLAUDE.md")
		os.MkdirAll(filepath.Dir(claude), 0o755)
		os.WriteFile(claude, []byte("# viele Regeln\n"), 0o644)
		if got := h.RulePath(home); got != claude {
			t.Errorf("rule path = %s, want CLAUDE.md — creating AGENTS.md would silence it", got)
		}
	})

	t.Run("nichts vorhanden", func(t *testing.T) {
		home := t.TempDir()
		want := filepath.Join(home, ".config", "opencode", "AGENTS.md")
		if got := h.RulePath(home); got != want {
			t.Errorf("rule path = %s, want %s", got, want)
		}
	})
}

func TestInstallOpencodeRegistersMCPAndKeepsForeignEntries(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	os.WriteFile(cfgPath, []byte(`{"model":"litellm/mistral-large","mcp":{"other":{"type":"local","command":["other"],"enabled":true}}}`), 0o644)

	if _, err := InstallOpencode(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Model string                     `json:"model"`
		MCP   map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is no longer valid JSON: %v\n%s", err, raw)
	}
	if cfg.Model != "litellm/mistral-large" {
		t.Errorf("installation dropped a foreign setting: %s", raw)
	}
	if _, ok := cfg.MCP["other"]; !ok {
		t.Errorf("installation dropped a foreign MCP server: %s", raw)
	}
	entry, ok := cfg.MCP["ghosttree"]
	if !ok {
		t.Fatalf("ghosttree was not registered: %s", raw)
	}
	var got struct {
		Type    string   `json:"type"`
		Command []string `json:"command"`
		Enabled bool     `json:"enabled"`
	}
	json.Unmarshal(entry, &got)
	if got.Type != "local" || len(got.Command) != 2 || got.Command[0] != "ctx" || got.Command[1] != "mcp" || !got.Enabled {
		t.Errorf("registration = %s, want a local ctx mcp server", entry)
	}
}

func TestInstallOpencodeTwiceChangesNothingTheSecondTime(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallOpencode(home); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	first, _ := os.ReadFile(cfgPath)
	if _, err := InstallOpencode(home); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(cfgPath)
	if string(first) != string(second) {
		t.Errorf("second run changed the config:\n%s\n%s", first, second)
	}
}

// Eine Umgebung ohne Push-Kanal hat nur zwei Wege: das Werkzeug, das sie selbst
// aufrufen muss, und die Dateien, die ohnehin daliegen. Beide gehören in ihren
// Regeltext, und der Dateiweg zuerst — er kostet keinen Aufruf.
func TestARuleSectionWithoutPushNamesTheFilesOnDisk(t *testing.T) {
	withPush := ruleFor(harnessNamed("claude"))
	withoutPush := ruleFor(harnessNamed("opencode"))

	if !strings.Contains(withoutPush, ".ghosttree/INDEX.md") {
		t.Errorf("a harness without push must be pointed at the mirror:\n%s", withoutPush)
	}
	if !strings.Contains(withoutPush, "context_get") {
		t.Errorf("it must still be asked to fetch its context:\n%s", withoutPush)
	}
	if strings.Contains(withPush, "context_get` tool once") {
		t.Errorf("claude delivers its context and must not be asked again:\n%s", withPush)
	}
}

// Zwei Umgebungen können sich eine Regeldatei teilen — auf workstation-a tun es
// Claude Code und opencode, weil opencode ~/.claude/CLAUDE.md als Ersatz liest.
// Dann darf dort nicht stehen, was nur für eine von beiden stimmt: "No context
// is pushed into this harness" las Claude Code über sich selbst, obwohl es
// Kontext bekommt. Und weil beide Installationen dieselbe Datei schreiben,
// kippte der Text bei jedem Lauf hin und her.
func TestASharedRuleFileCarriesWhatIsTrueForBothHarnesses(t *testing.T) {
	home := t.TempDir()
	claudeRules := filepath.Join(home, ".claude", "CLAUDE.md")
	os.MkdirAll(filepath.Dir(claudeRules), 0o755)
	os.WriteFile(claudeRules, []byte("# eigene Regeln\n"), 0o644)

	// Beide schreiben nach CLAUDE.md: opencode über den Ersatzweg.
	if got := harnessNamed("opencode").RulePath(home); got != claudeRules {
		t.Fatalf("precondition: opencode rule path = %s", got)
	}
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	afterClaude, _ := os.ReadFile(claudeRules)
	if _, err := InstallOpencode(home); err != nil {
		t.Fatal(err)
	}
	afterOpencode, _ := os.ReadFile(claudeRules)

	if string(afterClaude) != string(afterOpencode) {
		t.Errorf("the shared section flips between installations:\n--- claude:\n%s\n--- opencode:\n%s",
			afterClaude, afterOpencode)
	}
	if !strings.Contains(string(afterOpencode), ".ghosttree/INDEX.md") {
		t.Errorf("the shared section must serve the harness without push:\n%s", afterOpencode)
	}
	// Selbstprüfend statt behauptend: der Satz muss auch dann stimmen, wenn ihn
	// eine Umgebung liest, die ihren Kontext sehr wohl bekommen hat.
	if strings.Contains(string(afterOpencode), "No context is pushed into this harness") {
		t.Errorf("a shared file must not claim something false about one of its readers:\n%s", afterOpencode)
	}
}

// Der gemeinsame Regeltext nennt den Spiegel als Ganzes, nicht nur den Baum.
// Seit REQ-175 liegen Wissen, Dokumente und der Auftragsspeicher genauso dort,
// und wer das nicht weiss, ruft ein Werkzeug für etwas auf, das danebenliegt.
func TestTheRuleTextNamesTheWholeMirror(t *testing.T) {
	for _, want := range []string{".ghosttree/INDEX.md", "knowledge/", "requests/", "grep"} {
		if !strings.Contains(ruleText, want) {
			t.Errorf("the rule text does not mention %q:\n%s", want, ruleText)
		}
	}
}
