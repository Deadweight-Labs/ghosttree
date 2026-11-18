package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/serverinstall"
)

// stubSystem answers like a machine on which everything works: accounts that
// were added are listed, and the backup unit leaves a copy.
type stubSystem struct {
	cmds     []string
	prefix   string
	accounts []string
}

func (s *stubSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	s.cmds = append(s.cmds, line)
	switch {
	case strings.Contains(line, "login-link"):
		return "https://gt.example.com/ui/login/code?code=XYZ\nvalid 15m\n", nil
	case strings.Contains(line, "db check"):
		return "ok\n", nil
	case strings.Contains(line, "is-active"):
		return "active\n", nil
	case strings.Contains(line, "account add "):
		for i, a := range args {
			if a == "add" {
				s.accounts = append(s.accounts, args[i+1])
			}
		}
	case strings.Contains(line, "account list"):
		var b strings.Builder
		for i, a := range s.accounts {
			fmt.Fprintf(&b, "person:%d\t%s\tkind=person state=active admin=%t email=", i+1, a, a != "gardener")
			if a == "gardener" {
				b.WriteString(" gardener=true")
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	case strings.Contains(line, "start ghosttree-backup.service") && s.prefix != "":
		dir := filepath.Join(s.prefix, serverinstall.BackupDir)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "ghosttree-20260101-030000.db"), []byte("db"), 0o600)
	}
	return "", nil
}

func (s *stubSystem) Get(ctx context.Context, url string) ([]byte, error) {
	return []byte(`{"ok":true}`), nil
}

type scriptedTerminal struct {
	answers []string
	asked   []string
}

func (t *scriptedTerminal) Ask(prompt string) (string, error) {
	t.asked = append(t.asked, prompt)
	if len(t.answers) == 0 {
		return "", errors.New("no more answers")
	}
	a := t.answers[0]
	t.answers = t.answers[1:]
	return a, nil
}
func (t *scriptedTerminal) Close() error { return nil }

func withServerSeams(t *testing.T, sys serverinstall.System, term terminal) string {
	t.Helper()
	prefix := t.TempDir()
	if st, ok := sys.(*stubSystem); ok {
		st.prefix = prefix
	}
	oldSys, oldPrefix, oldTTY := serverSystem, serverPrefix, installTTY
	serverSystem, serverPrefix = sys, prefix
	installTTY = func() (terminal, error) {
		if term == nil {
			return nil, os.ErrNotExist
		}
		return term, nil
	}
	t.Cleanup(func() { serverSystem, serverPrefix, installTTY = oldSys, oldPrefix, oldTTY })
	return prefix
}

func TestServerInstallAsksOnlyWhatTheOptionsLeaveOpen(t *testing.T) {
	// Domain given as an option: not asked. Everything else is answered on the terminal.
	term := &scriptedTerminal{answers: []string{"2", "ops@example.com", "alex", "de", ""}}
	prefix := withServerSeams(t, &stubSystem{}, term)
	var out bytes.Buffer
	code := cmdServer([]string{"install", "--domain", "gt.example.com"}, &out)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	for _, q := range term.asked {
		if strings.Contains(q, "Public name") {
			t.Errorf("asked for what an option answered: %q", q)
		}
	}
	env, _ := os.ReadFile(filepath.Join(prefix, serverinstall.ServerEnv))
	for _, want := range []string{"GHOSTTREE_TLS_ACME_HOST=gt.example.com", "GHOSTTREE_TLS_ACME_EMAIL=ops@example.com", "GHOSTTREE_LANGUAGE=de"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("server.env lacks %q:\n%s", want, env)
		}
	}
	if !strings.Contains(out.String(), "Sign in as alex") || !strings.Contains(out.String(), "https://gt.example.com/ui/login/code?code=XYZ") {
		t.Errorf("no login link:\n%s", out.String())
	}
}

func TestServerInstallWithYesAsksNothingAndKeepsTheServerLocal(t *testing.T) {
	term := &scriptedTerminal{}
	prefix := withServerSeams(t, &stubSystem{}, term)
	var out bytes.Buffer
	if code := cmdServer([]string{"install", "--yes"}, &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if len(term.asked) != 0 {
		t.Errorf("asked with --yes: %v", term.asked)
	}
	env, _ := os.ReadFile(filepath.Join(prefix, serverinstall.ServerEnv))
	if strings.Contains(string(env), "PUBLIC_URL") || !strings.Contains(string(env), "GHOSTTREE_LISTEN=127.0.0.1:8474") {
		t.Errorf("server.env:\n%s", env)
	}
}

func TestServerInstallWithoutATerminalTakesTheOptionsAndDefaults(t *testing.T) {
	prefix := withServerSeams(t, &stubSystem{}, nil)
	var out bytes.Buffer
	if code := cmdServer([]string{"install", "--domain", "gt.example.com", "--admin", "robin"}, &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(prefix, serverinstall.ProxyDir, "Caddyfile")); err != nil {
		t.Errorf("default TLS is a proxy with its configuration: %v", err)
	}
	if !strings.Contains(out.String(), "Caddyfile") || !strings.Contains(out.String(), "Sign in as robin") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestServerInstallRefusesBadAnswersBeforeTouchingAnything(t *testing.T) {
	prefix := withServerSeams(t, &stubSystem{}, nil)
	for _, args := range [][]string{
		{"install", "--yes", "--domain", "10.0.0.1"},
		{"install", "--yes", "--tls", "builtin"},
		{"install", "--yes", "--ai", "anthropic"},
		{"install", "--yes", "extra"},
		{"install", "--yes", "--nope"},
	} {
		var out bytes.Buffer
		if code := cmdServer(args, &out); code == 0 {
			t.Errorf("%v accepted:\n%s", args, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(prefix, serverinstall.EtcDir)); err == nil {
		t.Error("files were written for a refused answer")
	}
}

func TestServerCommandNamesItsSubcommands(t *testing.T) {
	var out bytes.Buffer
	if code := cmdServer(nil, &out); code != 2 || !strings.Contains(out.String(), "ctx server install") {
		t.Errorf("no args: %d %s", code, out.String())
	}
	out.Reset()
	if code := cmdServer([]string{"bogus"}, &out); code != 2 {
		t.Errorf("unknown: %d", code)
	}
	out.Reset()
	if code := cmdServer([]string{"install", "--help"}, &out); code != 0 || !strings.Contains(out.String(), "--tls builtin") {
		t.Errorf("install --help: %d\n%s", code, out.String())
	}
	// install.sh asks `ctx server install --help` to learn whether this ctx can do it.
	if run([]string{"server", "install", "--help"}, &bytes.Buffer{}) != 0 {
		t.Error("ctx server is not wired into the command table")
	}
}

func TestServerUpdateNeedsAReleaseKeyAndAnInstalledServer(t *testing.T) {
	withServerSeams(t, &stubSystem{}, nil)
	old := serverRelease
	t.Cleanup(func() { serverRelease = old })
	var out bytes.Buffer
	serverRelease = func() (serverinstall.Release, error) { return serverinstall.Release{}, errors.New("no key") }
	if code := cmdServer([]string{"update"}, &out); code != 1 || !strings.Contains(out.String(), "no key") {
		t.Errorf("%d %s", code, out.String())
	}
	out.Reset()
	serverRelease = func() (serverinstall.Release, error) { return serverinstall.Release{}, nil }
	if code := cmdServer([]string{"update", "--version", "v0.2.0"}, &out); code != 1 || !strings.Contains(out.String(), "ctx server install") {
		t.Errorf("%d %s", code, out.String())
	}
}
