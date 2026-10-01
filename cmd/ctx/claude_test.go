package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
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
	raw, err := claudeMCPConfig("/opt/ctx", "claude:h:1", "")
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
	conf, _ := claudeMCPConfig("/opt/ctx", "claude:h:9", "")
	if code := runClaude(conf, "claude:h:9", "", []string{"--resume", "x y"}); code != 7 {
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
	if code := runClaude([]byte("{}"), "a", "", nil); code != 127 {
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

func TestClaudeAgentFlagNeedsValue(t *testing.T) {
	for _, args := range [][]string{{"claude", "--agent"}, {"claude", "--agent", ""}, {"claude", "--agent", "--dry-run"}} {
		var out bytes.Buffer
		if code := run(args, &out); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, out.String())
		}
	}
}

func TestClaudeDoubleDashEndsLauncherFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	run([]string{"claude", "--dry-run", "--", "--dry-run", "-p"}, &out)
	if !strings.Contains(out.String(), "server:ghosttree-channel --dry-run -p\n") {
		t.Fatalf("-- must be consumed and the rest passed on:\n%s", out.String())
	}
}

func TestCoordSendMentionRejectsBadValues(t *testing.T) {
	for _, v := range []string{"", "  ", "-x", "--machine"} {
		var out bytes.Buffer
		if code := run([]string{"coord", "send", "hi", "--mention", v}, &out); code != 2 {
			t.Errorf("mention %q: exit %d, want 2 (%s)", v, code, out.String())
		}
	}
	var out bytes.Buffer
	if code := run([]string{"coord", "send", "hi", "--mention"}, &out); code != 2 {
		t.Errorf("missing value: exit %d", code)
	}
}

func TestCoordAgentOverrideMatchesHarness(t *testing.T) {
	clear := func() {
		for _, k := range []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID", "OPENCODE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
			t.Setenv(k, "")
		}
	}
	clear()
	t.Setenv(agentIDEnv, "claude:h:1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	if got := coordAgentOverride(); got != "claude:h:1" {
		t.Errorf("claude harness: %q", got)
	}
	if got := currentSessionRef(); got != "sess-1" {
		t.Errorf("session ref must stay the harness id, got %q", got)
	}
	clear()
	t.Setenv("CODEX_THREAD_ID", "thr")
	if got := coordAgentOverride(); got != "" {
		t.Errorf("codex inside a ctx-claude session must not inherit the identity: %q", got)
	}
	if got := currentSessionRef(); got != "thr" {
		t.Errorf("codex session ref: %q", got)
	}
}

// Ein Fake-claude, das Signale mitschreibt und sich beendet.
func fakeClaudeTrapping(t *testing.T, dir string) (ready, rec string) {
	t.Helper()
	ready, rec = filepath.Join(dir, "ready"), filepath.Join(dir, "sig")
	script := "#!/bin/sh\n" +
		"for s in INT QUIT TERM HUP; do trap \"echo $s > " + rec + "; exit 9\" $s; done\n" +
		"touch " + ready + "\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ready, rec
}

func TestRunClaudeForwardsSignalsAndCleansUp(t *testing.T) {
	for _, sig := range []struct {
		name string
		sig  syscall.Signal
	}{{"INT", syscall.SIGINT}, {"QUIT", syscall.SIGQUIT}, {"TERM", syscall.SIGTERM}, {"HUP", syscall.SIGHUP}} {
		t.Run(sig.name, func(t *testing.T) {
			dir := t.TempDir()
			ready, rec := fakeClaudeTrapping(t, dir)
			t.Setenv(claudeBinEnv, filepath.Join(dir, "claude"))
			t.Setenv("TMPDIR", dir)
			codeCh := make(chan int, 1)
			go func() { codeCh <- runClaude([]byte("{}"), "claude:h:1", "", nil) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fake claude never started")
				}
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond) // Notify steht lange vor dem Start des Kindes
			if err := syscall.Kill(os.Getpid(), sig.sig); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-codeCh:
				if code != 9 {
					t.Fatalf("exit %d, want the child's 9", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("launcher did not return after the signal")
			}
			if b, _ := os.ReadFile(rec); strings.TrimSpace(string(b)) != sig.name {
				t.Fatalf("child saw %q, want %s", b, sig.name)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "ghosttree-claude-*.json")); len(left) != 0 {
				t.Fatalf("temp config left behind: %v", left)
			}
		})
	}
}

func TestClaudeAgentFlagRequiresPrefix(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"claude", "--dry-run", "--agent", "alice"}, &out); code != 2 || !strings.Contains(out.String(), `must start with "claude:"`) {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

func TestChannelSelfFallsBackToAgentEnvWithPrefixRule(t *testing.T) {
	for _, k := range []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID", "OPENCODE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv(agentIDEnv, "claude:h:7")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	if got := resolveChannelSelf(""); got != "claude:h:7" {
		t.Errorf("env fallback: %q", got)
	}
	if got := resolveChannelSelf("claude:explicit"); got != "claude:explicit" {
		t.Errorf("flag wins: %q", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "thr")
	if got := resolveChannelSelf(""); got != "thr" {
		t.Errorf("foreign prefix must fall back to the harness id: %q", got)
	}
}
