package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeMCPRepairsStaleOwnedEntryAndPreservesForeignEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeTestFile(t, path, `{"theme":"dark","mcpServers":{"other":{"command":"other"},"ghosttree":{"type":"stdio","command":"wrong","args":["serve"]}}}`, 0o600)
	if _, err := registerClaudeMCP(path); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	readTestJSON(t, path, &cfg)
	servers := cfg["mcpServers"].(map[string]any)
	if servers["other"] == nil || cfg["theme"] != "dark" {
		t.Fatalf("foreign config was lost: %#v", cfg)
	}
	if got := servers["ghosttree"]; !jsonValuesEqual(got, claudeMCPEntry()) {
		t.Fatalf("ghosttree entry = %#v, want %#v", got, claudeMCPEntry())
	}
	assertMode(t, path, 0o600)
}

func TestOpencodeMCPRepairsStaleOwnedEntryAndPreservesForeignEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeTestFile(t, path, `{"model":"mine","mcp":{"other":{"command":["other"]},"ghosttree":{"type":"remote","url":"wrong"}}}`, 0o600)
	if _, err := registerOpencodeMCP(path); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	readTestJSON(t, path, &cfg)
	servers := cfg["mcp"].(map[string]any)
	if servers["other"] == nil || cfg["model"] != "mine" {
		t.Fatalf("foreign config was lost: %#v", cfg)
	}
	if got := servers["ghosttree"]; !jsonValuesEqual(got, opencodeMCPEntry()) {
		t.Fatalf("ghosttree entry = %#v, want %#v", got, opencodeMCPEntry())
	}
	assertMode(t, path, 0o600)
}

func TestCodexMCPRepairsWrongAndDuplicateOwnedTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, `model = "gpt-5"

[mcp_servers.other]
command = "other"

[mcp_servers.ghosttree]
command = "wrong"
args = ["serve"]

[projects."/tmp/repo"]
trust_level = "trusted"

[mcp_servers.ghosttree]
command = "also-wrong"
`, 0o600)
	if _, err := appendCodexMCP(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "[mcp_servers.ghosttree]") != 1 {
		t.Fatalf("owned table count != 1:\n%s", text)
	}
	for _, want := range []string{`model = "gpt-5"`, "[mcp_servers.other]", `command = "other"`, `[projects."/tmp/repo"]`, `command = "ctx"`, `args = ["mcp"]`} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q:\n%s", want, text)
		}
	}
	assertMode(t, path, 0o600)
}

func TestWriteAtomicUsesCreateModeOnlyForNewFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	writeTestFile(t, existing, "old", 0o600)
	if err := writeAtomic(existing, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertMode(t, existing, 0o600)
	created := filepath.Join(dir, "created")
	if err := writeAtomic(created, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertMode(t, created, 0o644)
}

func TestVerifyMCPRejectsStaleNamedEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(string) string
		body string
	}{
		{"claude", ClaudeUserConfigPath, `{"mcpServers":{"ghosttree":{"command":"wrong","args":["mcp"]}}}`},
		{"codex", func(home string) string { return filepath.Join(home, ".codex", "config.toml") }, "[mcp_servers.ghosttree]\ncommand = \"wrong\"\nargs = [\"mcp\"]\n"},
		{"opencode", func(home string) string { return filepath.Join(home, ".config", "opencode", "opencode.json") }, `{"mcp":{"ghosttree":{"type":"local","command":["wrong","mcp"],"enabled":true}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			writeTestFile(t, tc.path(home), tc.body, 0o644)
			selected := ComponentSet{ComponentMCP: true}
			checks := VerifySelected(tc.name, home, selected)
			if len(checks) == 0 || checks[0].OK {
				t.Fatalf("stale named entry passed: %+v", checks)
			}
		})
	}
}

func TestVerifySelectedKeepsClaudeFallbackMCPCheck(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	selected := ComponentSet{ComponentMCP: true}
	if _, err := InstallSelected("claude", home, selected); err != nil {
		t.Fatal(err)
	}
	checks := VerifySelected("claude", home, selected)
	var fallback *Check
	for i := range checks {
		if strings.Contains(checks[i].Name, "fallback") {
			fallback = &checks[i]
		}
	}
	if fallback == nil || fallback.OK {
		t.Fatalf("missing fallback config must remain visible: %+v", checks)
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readTestJSON(t *testing.T, path string, dst any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode = %04o, want %04o", got, want)
	}
}
