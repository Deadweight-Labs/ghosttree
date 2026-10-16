package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func writeFakeClaude(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func ttyWith(answers ...string) *fakeTTY { return &fakeTTY{answers: answers} }

func TestInvokedAsGhclaudeGoesByTheBasenameOfArgv0(t *testing.T) {
	for argv0, want := range map[string]bool{
		"ghclaude": true, "/home/x/.local/bin/ghclaude": true, "./ghclaude": true,
		"ctx": false, "/usr/bin/ctx": false, "claude": false, "ghclaude2": false,
	} {
		if got := invokedAsGhclaude(argv0); got != want {
			t.Errorf("invokedAsGhclaude(%q) = %v, want %v", argv0, got, want)
		}
	}
}

func TestGhclaudePassesEveryArgumentUnchangedAndLoadsTheChannel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	bin := writeFakeClaude(t, home, "exit 0\n")
	t.Setenv(claudeBinEnv, bin)
	t.Setenv(claudeDryRunEnv, "")
	var gotPath string
	var gotArgv, gotEnv []string
	prev := ghclaudeExec
	ghclaudeExec = func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return nil
	}
	t.Cleanup(func() { ghclaudeExec = prev })

	// The arguments the Agent SDK uses, plus launcher flags that ctx claude
	// would swallow: ghclaude must hand all of them to claude.
	in := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--resume", "abc", "--dry-run", "--agent", "x", "--role", "lead", "--model", "sonnet", "--", "hello world"}
	var out bytes.Buffer
	if code := runGhclaude("/x/ghclaude", in, &out); code != 126 {
		t.Fatalf("returns only when exec failed; got %d", code)
	}
	if gotPath != bin || gotArgv[0] != bin {
		t.Fatalf("exec %q argv0 %q, want %q", gotPath, gotArgv[0], bin)
	}
	n := len(gotArgv) - len(in)
	if n < 4 || !reflect.DeepEqual(gotArgv[n:], in) {
		t.Fatalf("arguments changed or not at the end:\n got %q\nwant %q", gotArgv[n:], in)
	}
	head := strings.Join(gotArgv[1:n], " ")
	if !strings.Contains(head, "--dangerously-load-development-channels server:"+channelServerName) ||
		!strings.Contains(head, "--mcp-config") || !strings.Contains(head, `"ghosttree-channel"`) {
		t.Errorf("channel not loaded: %s", head)
	}
	env := strings.Join(gotEnv, "\n")
	if !strings.Contains(env, agentIDEnv+"=claude:") || strings.Count(env, agentIDEnv+"=") != 1 {
		t.Errorf("agent identity missing or duplicated in env")
	}
	// --resume names an unknown session, so none is claimed.
	if !strings.Contains(env, sessionIDEnv+"=\n") && !strings.HasSuffix(env, sessionIDEnv+"=") {
		t.Errorf("session id must be empty for --resume")
	}
	if strings.Contains(head, "--session-id") {
		t.Errorf("--session-id injected next to --resume: %s", head)
	}
}

// The measured contract: ghclaude replaces itself with claude, so exit code,
// signals and stdin/stdout belong to claude.
func TestGhclaudeAsALinkExecsClaudeWithExitCodeStdinAndSamePid(t *testing.T) {
	if testing.Short() {
		t.Skip("builds ctx")
	}
	dir := t.TempDir()
	ctxBin := filepath.Join(dir, "bin", "ctx")
	if err := os.MkdirAll(filepath.Dir(ctxBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("go", "build", "-o", ctxBin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	link := filepath.Join(filepath.Dir(ctxBin), "ghclaude")
	if err := os.Symlink("ctx", link); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "cap")
	// claude's own PATH entry is absent: it is found next to ghclaude.
	writeFakeClaude(t, filepath.Dir(ctxBin), `echo "$$" > `+capture+`.pid
for a in "$@"; do printf '%s\n' "$a"; done > `+capture+`.args
cat > `+capture+`.stdin
echo "id=$GHOSTTREE_AGENT_ID" > `+capture+`.env
exit 7
`)
	cmd := exec.Command(link, "-p", "--output-format", "stream-json", "a b")
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + dir + "/cfg", "PATH=/usr/bin:/bin"}
	cmd.Stdin = strings.NewReader(`{"type":"user"}` + "\n")
	err := cmd.Run()
	var ee *exec.ExitError
	if !asExit(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("exit code = %v, want 7", err)
	}
	pid, _ := os.ReadFile(capture + ".pid")
	if strings.TrimSpace(string(pid)) != strconv.Itoa(cmd.Process.Pid) {
		t.Errorf("claude pid %q != started pid %d: not exec'd", pid, cmd.Process.Pid)
	}
	stdin, _ := os.ReadFile(capture + ".stdin")
	if string(stdin) != `{"type":"user"}`+"\n" {
		t.Errorf("stdin = %q", stdin)
	}
	args, _ := os.ReadFile(capture + ".args")
	if !strings.HasSuffix(string(args), "-p\n--output-format\nstream-json\na b\n") ||
		!strings.Contains(string(args), "--dangerously-load-development-channels\nserver:ghosttree-channel\n") {
		t.Errorf("args:\n%s", args)
	}
	if env, _ := os.ReadFile(capture + ".env"); !strings.HasPrefix(string(env), "id=claude:") {
		t.Errorf("env = %q", env)
	}
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func TestGhclaudeFindsClaudeNextToItselfAndNeverStartsItself(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/nonexistent")
	t.Setenv(claudeBinEnv, "")
	if _, err := findClaude(filepath.Join(dir, "ghclaude")); err == nil {
		t.Fatal("found claude where there is none")
	}
	want := writeFakeClaude(t, dir, "exit 0\n")
	got, err := findClaude(filepath.Join(dir, "ghclaude"))
	if err != nil || got != want {
		t.Fatalf("findClaude = %q, %v; want %q", got, err, want)
	}
	// A "claude" that is this very test binary is refused.
	self, _ := os.Executable()
	other := t.TempDir()
	if err := os.Symlink(self, filepath.Join(other, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other)
	if p, err := findClaude("ghclaude"); err == nil {
		t.Fatalf("resolved to itself: %s", p)
	}
}

func TestInstallClaudeAsksOnceAndLinksGhclaudeNextToCtx(t *testing.T) {
	home, ctx, _ := installHome(t)
	tty := ttyWith("")
	installTTY = func() (terminal, error) { return tty, nil }
	t.Cleanup(func() { installTTY = func() (terminal, error) { return nil, os.ErrNotExist } })
	var out bytes.Buffer
	if code := run([]string{"install", "claude", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(tty.asked) != 1 || tty.asked[0] != "Agents can be woken by messages while idle. Install ghclaude? [Y/n] " {
		t.Fatalf("asked %q", tty.asked)
	}
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	if dest, err := os.Readlink(link); err != nil || dest != "ctx" {
		t.Fatalf("link = %q, %v; want relative link to ctx (home %s)", dest, err, home)
	}
	if !sameFile(link, ctx) {
		t.Error("link does not lead to ctx")
	}
	text := out.String()
	for _, want := range []string{"created", link, "Claude executable", "interactive terminal"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}

	// Idempotent: a second run does not ask again and changes nothing.
	tty2 := ttyWith("n")
	installTTY = func() (terminal, error) { return tty2, nil }
	out.Reset()
	if code := run([]string{"install", "claude", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(tty2.asked) != 0 || !strings.Contains(out.String(), "unchanged") {
		t.Errorf("second run asked %q or did not report unchanged:\n%s", tty2.asked, out.String())
	}
}

func TestInstallClaudeAnswerNoOrNoTerminalCreatesNoLink(t *testing.T) {
	_, ctx, _ := installHome(t)
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	tty := ttyWith("n")
	installTTY = func() (terminal, error) { return tty, nil }
	t.Cleanup(func() { installTTY = func() (terminal, error) { return nil, os.ErrNotExist } })
	var out bytes.Buffer
	if code := run([]string{"install", "claude", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("link created despite answer n")
	}

	installTTY = func() (terminal, error) { return nil, os.ErrNotExist }
	out.Reset()
	if code := run([]string{"install", "claude", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("link created without a terminal")
	}
	if !strings.Contains(out.String(), "no terminal to ask") || !strings.Contains(out.String(), "install ghclaude") {
		t.Errorf("no hint without a terminal:\n%s", out.String())
	}

	out.Reset()
	tty3 := ttyWith("y")
	installTTY = func() (terminal, error) { return tty3, nil }
	if code := run([]string{"install", "claude", "--no-watch", "--no-ghclaude"}, &out); code != 0 || len(tty3.asked) != 0 {
		t.Fatalf("--no-ghclaude asked %q (exit %d)", tty3.asked, code)
	}
}

func TestInstallGhclaudeLinksWithoutAQuestionAndNeverTouchesAForeignFile(t *testing.T) {
	_, ctx, _ := installHome(t)
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	var out bytes.Buffer
	if code := run([]string{"install", "ghclaude"}, &out); code != 0 || !sameFile(link, ctx) {
		t.Fatalf("exit %d, link ok %v:\n%s", code, sameFile(link, ctx), out.String())
	}
	// A stale link is replaced.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", link); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"install", "ghclaude"}, &out); code != 0 || !sameFile(link, ctx) || !strings.Contains(out.String(), "updated") {
		t.Fatalf("stale link not replaced (exit %d):\n%s", code, out.String())
	}
	// A regular file of that name is not ours.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"install", "ghclaude"}, &out); code == 0 {
		t.Fatalf("overwrote a foreign file:\n%s", out.String())
	}
	if b, _ := os.ReadFile(link); string(b) != "mine" {
		t.Errorf("foreign file changed: %q", b)
	}
}

func TestUninstallGhclaudeRemovesOnlyTheLink(t *testing.T) {
	_, ctx, _ := installHome(t)
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	var out bytes.Buffer
	run([]string{"install", "ghclaude"}, &out)
	out.Reset()
	if code := run([]string{"uninstall", "ghclaude"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Lstat(link); err == nil {
		t.Fatal("link still there")
	}
	if _, err := os.Stat(ctx); err != nil {
		t.Fatalf("ctx itself was removed: %v", err)
	}
	out.Reset()
	if code := run([]string{"uninstall", "ghclaude"}, &out); code != 0 || !strings.Contains(out.String(), "not installed") {
		t.Fatalf("second uninstall: exit %d:\n%s", code, out.String())
	}
	if err := os.WriteFile(link, []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"uninstall", "ghclaude"}, &out); code == 0 {
		t.Error("removed a regular file")
	}
	if b, _ := os.ReadFile(link); string(b) != "mine" {
		t.Errorf("foreign file changed: %q", b)
	}
}

func TestDoctorChecksTheGhclaudeLink(t *testing.T) {
	home, ctx, _ := installHome(t)
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	c := ghclaudeCheck(home)
	if c.OK || !c.Unverified || !strings.Contains(c.Detail, "not installed") {
		t.Errorf("absent link: %+v", c)
	}
	if err := os.Symlink("ctx", link); err != nil {
		t.Fatal(err)
	}
	if c := ghclaudeCheck(home); !c.OK || c.Detail != link {
		t.Errorf("good link: %+v", c)
	}
	os.Remove(link)
	if err := os.Symlink("gone", link); err != nil {
		t.Fatal(err)
	}
	c = ghclaudeCheck(home)
	if c.OK || c.Unverified || !strings.Contains(c.Fix, "install ghclaude") {
		t.Errorf("broken link must fail with a fix: %+v", c)
	}
	// doctor --fix renews a broken link.
	var out bytes.Buffer
	run([]string{"doctor", "--fix", "--only", "ghclaude"}, &out)
	if !sameFile(link, ctx) {
		t.Errorf("doctor --fix did not renew the link:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ghclaude") {
		t.Errorf("doctor output lacks ghclaude:\n%s", out.String())
	}
}

func TestJoinOffersGhclaudeAtItsOwnTerminalAndHintsWithoutOne(t *testing.T) {
	_, ctx, _ := installHome(t)
	link := filepath.Join(filepath.Dir(ctx), "ghclaude")
	var out bytes.Buffer

	offerGhclaude(context.Background(), &out, nil, os.Getenv("HOME"))
	if _, err := os.Lstat(link); err == nil || !strings.Contains(out.String(), "no terminal to ask") {
		t.Fatalf("without terminal: link=%v\n%s", err, out.String())
	}

	out.Reset()
	tty := ttyWith("")
	offerGhclaude(context.Background(), &out, tty, os.Getenv("HOME"))
	if !sameFile(link, ctx) || len(tty.asked) != 1 {
		t.Fatalf("default answer must install: asked %q\n%s", tty.asked, out.String())
	}
}
