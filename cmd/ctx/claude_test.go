package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNewAgentIDShape(t *testing.T) {
	id, err := newAgentID("mainex")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^claude:mainex:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("unexpected id %q", id)
	}
	other, _ := newAgentID("mainex")
	if other == id {
		t.Fatal("ids must differ per launch")
	}
	if id, _ := newAgentID(""); !strings.HasPrefix(id, "claude:unknown:") {
		t.Fatalf("empty machine: %q", id)
	}
}

func TestClaudeMCPConfigSharesIdentity(t *testing.T) {
	raw, err := claudeMCPConfig("/opt/ctx", "claude:h:1")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("want exactly the channel server, got %v", cfg.MCPServers)
	}
	s := cfg.MCPServers["ghosttree-channel"]
	if s.Command != "/opt/ctx" || strings.Join(s.Args, " ") != "channel --agent claude:h:1" {
		t.Fatalf("server: %+v", s)
	}
	if s.Env[agentIDEnv] != "claude:h:1" {
		t.Fatalf("env: %v", s.Env)
	}
}

func TestClaudeArgsPassThrough(t *testing.T) {
	got := claudeArgs("/tmp/x.json", []string{"-p", "hello world", "--model", "opus"})
	want := []string{"--mcp-config", "/tmp/x.json", "--dangerously-load-development-channels", "server:ghosttree-channel", "-p", "hello world", "--model", "opus"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got %q", got)
	}
}

func TestAgentIDEnvOverridesHarnessSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	t.Setenv(agentIDEnv, "claude:h:42")
	if got := currentSessionRef(); got != "claude:h:42" {
		t.Fatalf("got %q", got)
	}
}

func TestClaudeDryRunFormat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if code := run([]string{"claude", "--dry-run", "-p", "say hi"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	s := out.String()
	id := regexp.MustCompile(`(?m)^agent: (\S+)$`).FindStringSubmatch(s)
	if id == nil {
		t.Fatalf("no agent line:\n%s", s)
	}
	for _, want := range []string{
		"command: claude --mcp-config <tmp> --dangerously-load-development-channels server:ghosttree-channel -p 'say hi'",
		"env: GHOSTTREE_AGENT_ID=" + id[1],
		"mcp-config:\n{",
		`"channel",`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, id[1]) != 4 { // agent, env, --agent arg, config env
		t.Errorf("identity must appear identically 4 times:\n%s", s)
	}
}

func TestClaudeDryRunViaEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(claudeDryRunEnv, "1")
	t.Setenv(claudeBinEnv, "/bin/false")
	var out bytes.Buffer
	if code := run([]string{"claude"}, &out); code != 0 || !strings.Contains(out.String(), "command: /bin/false --mcp-config") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

// Ein Fake-claude prüft Argumente, Umgebung, Config-Datei, Exit-Code und
// Aufräumen.
func TestRunClaudeLaunchesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	fake := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n{ echo \"$@\"; echo \"$GHOSTTREE_AGENT_ID\"; echo \"$1\"; } > " + rec + "\ncp \"$2\" " + rec + ".cfg\nexit 7\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeBinEnv, fake)
	t.Setenv("TMPDIR", dir)
	conf, _ := claudeMCPConfig("/opt/ctx", "claude:h:9")
	if code := runClaude(conf, "claude:h:9", []string{"--resume", "x y"}); code != 7 {
		t.Fatalf("exit code %d, want 7", code)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !strings.Contains(lines[0], "--dangerously-load-development-channels server:ghosttree-channel --resume x y") || lines[1] != "claude:h:9" {
		t.Fatalf("recorded: %q", lines)
	}
	cfg, _ := os.ReadFile(rec + ".cfg")
	if !bytes.Equal(cfg, conf) {
		t.Fatalf("config not readable by claude: %q", cfg)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "ghosttree-claude-*.json"))
	if len(left) != 0 {
		t.Fatalf("temp config left behind: %v", left)
	}
}

func TestRunClaudeMissingBinary(t *testing.T) {
	t.Setenv(claudeBinEnv, filepath.Join(t.TempDir(), "nope"))
	if code := runClaude([]byte("{}"), "a", nil); code != 127 {
		t.Fatalf("code %d", code)
	}
}

func TestClaudeAgentFlagAndPassThroughAfterIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if code := run([]string{"claude", "--dry-run", "--agent", "claude:lab:1", "--dry-run", "-p", "x"}, &out); code != 0 {
		t.Fatal(out.String())
	}
	s := out.String()
	// -p beendet die Launcher-Flags.
	if !strings.Contains(s, "agent: claude:lab:1\n") || !strings.Contains(s, "server:ghosttree-channel -p x\n") {
		t.Fatalf("got:\n%s", s)
	}
	out.Reset()
	run([]string{"claude", "--dry-run", "-p", "--dry-run"}, &out)
	if !strings.Contains(out.String(), "-p --dry-run\n") {
		t.Fatalf("flag after first claude arg must pass through:\n%s", out.String())
	}
}
