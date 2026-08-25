package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no hooks file at %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("hooks file is not JSON: %v", err)
	}
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("no hooks key in %s: %s", path, b)
	}
	return hooks
}

func commandsFor(t *testing.T, hooks map[string]any, event string) []string {
	t.Helper()
	var out []string
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, ok := hm["command"].(string); ok {
				out = append(out, cmd)
			}
		}
	}
	return out
}

func TestInstallCodexWiresItsHooks(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	hooks := readHooks(t, filepath.Join(home, ".codex", "hooks.json"))
	for event, want := range map[string]string{
		"SessionStart":     hookCommand,
		"UserPromptSubmit": promptHookCommand,
	} {
		got := commandsFor(t, hooks, event)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s commands = %v, want exactly %q", event, got, want)
		}
	}
}

// Another tool keeps its lease hooks in this very file. Replacing the list would
// silently disarm them.
func TestInstallCodexKeepsForeignHooks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"description":"Global lifecycle hooks.","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"bash /home/user/Projects/session-lease/hooks/nw-codex-lease.sh","timeout":5}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	got := commandsFor(t, readHooks(t, path), "SessionStart")
	if len(got) != 2 {
		t.Fatalf("SessionStart commands = %v, want the foreign one kept alongside ours", got)
	}
	var foundForeign, foundOurs bool
	for _, cmd := range got {
		foundForeign = foundForeign || strings.Contains(cmd, "nw-codex-lease.sh")
		foundOurs = foundOurs || cmd == hookCommand
	}
	if !foundForeign || !foundOurs {
		t.Errorf("commands = %v, want both", got)
	}
	// Keys the installer knows nothing about have to survive too.
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "Global lifecycle hooks.") {
		t.Errorf("an unrelated top-level key was dropped: %s", b)
	}
}

func TestInstallingTwiceChangesNothingTheSecondTime(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if string(before) != string(after) {
		t.Errorf("second install rewrote the file:\n%s\nvs\n%s", before, after)
	}
}

// The rule section compensates for what does not arrive by itself. The test is
// delivery, not registration: Codex 0.149.0 runs the session-start hook and
// drops its additionalContext, so dropping the paragraph because the hook is
// wired would remove the one channel that works there.
func TestARuleSectionAsksForWhatDoesNotArriveByItself(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "At session start call the") {
		t.Errorf("codex must keep being asked while its hook context does not arrive:\n%s", b)
	}
	if !strings.Contains(string(b), "context_remember") {
		t.Errorf("the shared rule text is missing:\n%s", b)
	}

	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "At session start call the") {
		t.Errorf("claude delivers session-start context and must not be asked again:\n%s", b)
	}
}

// Registering a channel is not the same claim as delivering through it, and
// conflating the two is how a wired-but-silent hook would look like success.
func TestWiringAChannelIsNotClaimingItDelivers(t *testing.T) {
	codex := harnessNamed("codex")
	if !codex.Serves(ChannelSessionStart) {
		t.Error("codex session-start must still be wired: the contract is correct and costs nothing")
	}
	if codex.DeliversContext(ChannelSessionStart) {
		t.Error("codex session-start must not be claimed as delivering until a transcript shows it")
	}
	if !codex.DeliversContext(ChannelMCP) {
		t.Error("mcp is the channel that does work for codex")
	}
}

func TestDoctorReportsAnUnservedChannelAsAGap(t *testing.T) {
	home := t.TempDir()
	// Everything a presence check looks at is in place: MCP registered, rule
	// section written. Only the channels are unserved.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(codexMCPSection), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMarkerFile(filepath.Join(home, ".codex", "AGENTS.md"), ruleText); err != nil {
		t.Fatal(err)
	}

	var gaps []string
	for _, c := range VerifyCodex(home) {
		if !c.OK {
			gaps = append(gaps, c.Name)
		}
	}
	want := map[string]bool{"codex session-start hook": true, "codex user-prompt-submit hook": true}
	for _, name := range gaps {
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("doctor stayed silent about %v; reported gaps were %v", want, gaps)
	}

	// And it goes quiet once the channels are actually served.
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	for _, c := range VerifyCodex(home) {
		if !c.OK {
			t.Errorf("check %q still failing after install: %s", c.Name, c.Detail)
		}
	}
}

func TestBothHarnessesWireTheSameChannels(t *testing.T) {
	// The finding behind REQ-160: the two runtimes accept the same events with
	// the same payloads, so neither deserves a weaker wiring than the other. If
	// that ever stops being true, the difference belongs in the declaration,
	// where the doctor and the rule text both read it.
	claude := harnessNamed("claude")
	codex := harnessNamed("codex")
	for _, c := range []Channel{ChannelSessionStart, ChannelUserPrompt, ChannelMCP} {
		if !claude.Serves(c) || !codex.Serves(c) {
			t.Errorf("channel %q: claude=%v codex=%v", c, claude.Serves(c), codex.Serves(c))
		}
	}
}

func TestPreToolUseHookIsWiredWithAMatcher(t *testing.T) {
	home := t.TempDir()
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	settings, err := readJSONFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PreToolUse"].([]any)
	if len(groups) == 0 {
		t.Fatal("PreToolUse must be wired")
	}
	found := false
	for _, g := range groups {
		group, _ := g.(map[string]any)
		matcher, _ := group["matcher"].(string)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == preToolHookCommand {
				found = true
				// Ohne Matcher startet ctx bei jedem Bash-Aufruf mit.
				if matcher == "" {
					t.Fatal("the PreToolUse hook must carry a matcher, or it spawns on every Bash call")
				}
				for _, tool := range []string{"Read", "Edit", "Write", "NotebookEdit"} {
					if !strings.Contains(matcher, tool) {
						t.Fatalf("matcher %q does not cover %s", matcher, tool)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("the ghosttree PreToolUse hook was not written")
	}
}

func TestWiringPreToolUseKeepsForeignHooks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"session-lease-lease.sh"}]}]}}`
	if err := os.WriteFile(path, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "session-lease-lease.sh") {
		t.Fatal("a foreign PreToolUse hook must survive installation")
	}
}
