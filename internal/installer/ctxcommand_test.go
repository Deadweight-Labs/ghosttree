package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func useCtx(t *testing.T, path string) {
	t.Helper()
	prev := CtxCommand()
	SetCtxCommand(path)
	t.Cleanup(func() { SetCtxCommand(prev) })
}

func fakeBinary(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func hookCommands(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, groups := range file.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				out = append(out, h.Command)
			}
		}
	}
	return out
}

func TestInstallWritesAbsoluteCtxPathEverywhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ctx := fakeBinary(t, filepath.Join(home, "bin", "ctx"))
	useCtx(t, ctx)

	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(home); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallOpencode(home); err != nil {
		t.Fatal(err)
	}
	for _, c := range hookCommands(t, filepath.Join(home, ".claude", "settings.json")) {
		if !strings.HasPrefix(c, ctx+" hook ") {
			t.Errorf("claude hook %q does not start with the absolute path", c)
		}
	}
	for _, c := range hookCommands(t, filepath.Join(home, ".codex", "hooks.json")) {
		if !strings.HasPrefix(c, ctx+" hook ") {
			t.Errorf("codex hook %q does not start with the absolute path", c)
		}
	}
	cfg, _ := readJSONFile(ClaudeUserConfigPath(home))
	if got := entryCommand(cfg["mcpServers"].(map[string]any)["ghosttree"]); got != ctx {
		t.Errorf("claude mcp command = %q, want %q", got, ctx)
	}
	oc, _ := readJSONFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if got := entryCommand(oc["mcp"].(map[string]any)["ghosttree"]); got != ctx {
		t.Errorf("opencode mcp command = %q, want %q", got, ctx)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(toml), `command = "`+ctx+`"`) {
		t.Errorf("codex config lacks the absolute command:\n%s", toml)
	}
	for _, name := range []string{"claude", "codex", "opencode"} {
		for _, c := range VerifySelected(name, home, mustAll(t, name)) {
			if strings.Contains(c.Name, "registration") || strings.HasSuffix(c.Name, " hook") {
				if !c.OK {
					t.Errorf("%s: %s failed: %s", name, c.Name, c.Detail)
				}
			}
		}
	}
}

func mustAll(t *testing.T, harness string) ComponentSet {
	t.Helper()
	s, err := ResolveComponents(harness, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReinstallUpgradesBareEntriesAndLeavesForeignOnesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil { // bare ctx, as older versions wrote it
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	raw, _ := readJSONFile(settings)
	hooks := raw["hooks"].(map[string]any)
	hooks["SessionStart"] = append(hooks["SessionStart"].([]any), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/other/tool hook session-start --harness claude"}}})
	if err := writeJSONFile(settings, raw); err != nil {
		t.Fatal(err)
	}

	ctx := fakeBinary(t, filepath.Join(home, ".local", "bin", "ctx"))
	useCtx(t, ctx)
	changes, err := InstallClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 {
		t.Fatal("no changes reported")
	}
	got := hookCommands(t, settings)
	for _, c := range got {
		if c == "ctx hook session-start --harness claude" {
			t.Errorf("bare hook survived: %v", got)
		}
	}
	if !slices.Contains(got, "/opt/other/tool hook session-start --harness claude") {
		t.Errorf("foreign hook was touched: %v", got)
	}
	if !slices.Contains(got, ctx+" hook session-start --harness claude") {
		t.Errorf("absolute hook missing: %v", got)
	}
	// A second run changes nothing and does not add duplicates.
	before, _ := os.ReadFile(settings)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(settings)
	if string(before) != string(after) {
		t.Error("second install rewrote settings.json")
	}
}

func TestInstallQuotesCtxPathWithSpaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ctx := fakeBinary(t, filepath.Join(home, "My Tools", "ctx"))
	useCtx(t, ctx)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	want := `"` + ctx + `" hook session-start --harness claude`
	if got := hookCommands(t, filepath.Join(home, ".claude", "settings.json")); !slices.Contains(got, want) {
		t.Fatalf("hooks = %v, want %q", got, want)
	}
	words := splitCommand(want)
	if len(words) != 5 || words[0] != ctx {
		t.Fatalf("splitCommand = %q", words)
	}
	if _, err := InstallClaude(home); err != nil { // quoted entries are recognised as ours
		t.Fatal(err)
	}
	if n := len(hookCommands(t, filepath.Join(home, ".claude", "settings.json"))); n != 4 {
		t.Fatalf("hook count after reinstall = %d, want 4", n)
	}
}

func TestStableCtxPathPrefersTheNameOnPath(t *testing.T) {
	home := t.TempDir()
	real := fakeBinary(t, filepath.Join(home, "cellar", "1.2.3", "ctx"))
	stable := filepath.Join(home, ".local", "bin", "ctx")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, stable); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got := stableCtxPath(real, home, ""); got != stable {
		t.Errorf("stable path = %q, want %q", got, stable)
	}
	// A symlink as the executable resolves to the real file, then back to the stable name.
	if got := stableCtxPath(stable, home, ""); got != stable {
		t.Errorf("from symlink = %q, want %q", got, stable)
	}
	// An unrelated ctx at the stable location is not mistaken for this binary.
	other := fakeBinary(t, filepath.Join(t.TempDir(), "ctx"))
	if got := stableCtxPath(other, home, ""); got != other {
		t.Errorf("unrelated = %q, want %q", got, other)
	}
}

func TestDoctorNamesTheRightFixForBareCtxOffPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil { // bare ctx
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no ctx anywhere
	ctx := fakeBinary(t, filepath.Join(t.TempDir(), "ctx"))
	useCtx(t, ctx)

	var failed []Check
	for _, c := range VerifyClaude(home) {
		if !c.OK && (strings.Contains(c.Name, "registration") || strings.HasSuffix(c.Name, " hook")) {
			failed = append(failed, c)
		}
	}
	if len(failed) == 0 {
		t.Fatal("bare ctx off PATH passed the doctor")
	}
	for _, c := range failed {
		if !strings.Contains(c.Detail, "not on PATH") {
			t.Errorf("%s: detail %q does not name the cause", c.Name, c.Detail)
		}
		if !strings.Contains(c.Fix, ctx+" install claude") {
			t.Errorf("%s: fix %q does not use the absolute path", c.Name, c.Fix)
		}
	}
}

func TestDoctorAcceptsBareCtxThatIsOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fakeBinary(t, filepath.Join(dir, "ctx"))
	t.Setenv("PATH", dir)
	useCtx(t, filepath.Join(t.TempDir(), "ctx")) // expected path differs from the bare entry
	for _, c := range VerifyClaude(home) {
		if (strings.Contains(c.Name, "registration") || strings.HasSuffix(c.Name, " hook")) && !c.OK {
			t.Errorf("%s failed although ctx is on PATH: %s", c.Name, c.Detail)
		}
	}
}

func TestDoctorFlagsMovedAbsolutePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	gone := filepath.Join(t.TempDir(), "gone", "ctx")
	useCtx(t, gone)
	if _, err := InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	now := fakeBinary(t, filepath.Join(t.TempDir(), "ctx"))
	useCtx(t, now)
	var hit bool
	for _, c := range VerifyClaude(home) {
		if strings.Contains(c.Name, "registration") && !c.OK && strings.Contains(c.Detail, "does not exist") {
			hit = true
			if !strings.Contains(c.Fix, now+" install claude") {
				t.Errorf("fix = %q", c.Fix)
			}
		}
	}
	if !hit {
		t.Error("moved binary not reported")
	}
}
