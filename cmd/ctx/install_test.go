package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

// installHome gives a test its own HOME and config directory and a ctx binary
// at an absolute path, with a service manager that only records calls.
func installHome(t *testing.T) (home, ctx string, calls *[]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ctx = filepath.Join(home, "opt", "ctx")
	if err := os.MkdirAll(filepath.Dir(ctx), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := installCtxExecutable
	installCtxExecutable = func(string) (string, error) { return ctx, nil }
	t.Cleanup(func() { installCtxExecutable = prev })
	var recorded []string
	restore := installer.SetServiceBackend("linux", func(name string, args ...string) error {
		recorded = append(recorded, name+" "+strings.Join(args, " "))
		return nil
	}, 1000)
	t.Cleanup(restore)
	return home, ctx, &recorded
}

func connect(t *testing.T) {
	t.Helper()
	if err := config.Save(config.Config{ServerURL: "https://example.test", Token: "t", Machine: "box"}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallUsesAbsolutePathAndSetsUpCollector(t *testing.T) {
	home, ctx, calls := installHome(t)
	connect(t)
	var out bytes.Buffer
	if code := run([]string{"install", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(string(settings), `"`+ctx+` hook session-start --harness claude"`) {
		t.Errorf("settings.json lacks the absolute hook:\n%s", settings)
	}
	claudeJSON, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if !strings.Contains(string(claudeJSON), `"command": "`+ctx+`"`) {
		t.Errorf(".claude.json lacks the absolute command:\n%s", claudeJSON)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "ghosttree-watch.service")); err != nil {
		t.Errorf("collector unit missing: %v\n%s", err, out.String())
	}
	if !slicesContain(*calls, "systemctl --user enable --now ghosttree-watch.service") {
		t.Errorf("service not enabled: %v", *calls)
	}
	if installer.CtxCommand() != "ctx" {
		t.Errorf("install leaked the ctx command setting: %q", installer.CtxCommand())
	}
	// Running it again changes nothing on disk.
	before, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	out.Reset()
	if code := run([]string{"install", "claude"}, &out); code != 0 {
		t.Fatalf("second exit %d: %s", code, out.String())
	}
	after, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !bytes.Equal(before, after) {
		t.Error("second install rewrote settings.json")
	}
}

func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestInstallNoWatchSkipsTheService(t *testing.T) {
	home, _, calls := installHome(t)
	connect(t)
	var out bytes.Buffer
	if code := run([]string{"install", "codex", "--no-watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(*calls) != 0 {
		t.Errorf("service manager was called: %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit directory exists: %v", err)
	}
}

func TestInstallWithoutConnectionDoesNotStartTheCollector(t *testing.T) {
	_, ctx, calls := installHome(t)
	var out bytes.Buffer
	if code := run([]string{"install", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(*calls) != 0 {
		t.Errorf("service manager was called: %v", *calls)
	}
	if !strings.Contains(out.String(), "not connected yet") || !strings.Contains(out.String(), ctx+" install watch") {
		t.Errorf("no hint:\n%s", out.String())
	}
}

func TestInstallWatchTargetSetsUpOnlyTheService(t *testing.T) {
	home, _, calls := installHome(t)
	connect(t)
	var out bytes.Buffer
	if code := run([]string{"install", "watch"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(*calls) == 0 {
		t.Error("service manager not called")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Error("watch target touched the Claude configuration")
	}
}

func TestInstallHelpAndUnknownHarness(t *testing.T) {
	installHome(t)
	for _, args := range [][]string{{"install", "--help"}, {"install", "-h"}, {"install", "claude", "--help"}} {
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.Contains(out.String(), "usage: ctx install") || strings.Contains(out.String(), "unknown harness") {
			t.Errorf("%v: output:\n%s", args, out.String())
		}
	}
	var out bytes.Buffer
	if code := run([]string{"install", "vim"}, &out); code != 2 || !strings.Contains(out.String(), `unknown harness "vim"`) || !strings.Contains(out.String(), "usage: ctx install") {
		t.Errorf("unknown harness: exit %d\n%s", code, out.String())
	}
}

func TestDoctorFixRewritesBareEntriesWithTheAbsolutePath(t *testing.T) {
	home, ctx, _ := installHome(t)
	prev := installer.CtxCommand()
	installer.SetCtxCommand("ctx")
	if _, err := installer.InstallClaude(home); err != nil {
		t.Fatal(err)
	}
	installer.SetCtxCommand(prev)
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	run([]string{"doctor", "claude", "--only", "mcp"}, &out)
	if !strings.Contains(out.String(), "fix: run '"+ctx+" install claude") {
		t.Errorf("doctor does not name the absolute fix:\n%s", out.String())
	}
	out.Reset()
	run([]string{"doctor", "claude", "--only", "hooks", "--fix"}, &out)
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(settings), `"ctx hook`) {
		t.Errorf("bare hook survived --fix:\n%s", settings)
	}
}

func TestDoctorDoesNotFailWhenOnlyTheShellLacksCtx(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := binaryCheck()
	if c.OK || !c.Unverified || !strings.Contains(c.Fix, "PATH") {
		t.Fatalf("check = %+v", c)
	}
}

func TestDoctorCollectorHintNamesTheCommandNotARepoFile(t *testing.T) {
	home, ctx, _ := installHome(t)
	defer useRunningCtx(home)()
	t.Setenv("PATH", t.TempDir()) // no systemctl: the service state is unknown
	var fix string
	for _, c := range collectorChecks(home) {
		if c.Name == "collector active" {
			fix = c.Fix
		}
	}
	if strings.Contains(fix, "deploy/") || !strings.Contains(fix, ctx+" install watch") {
		t.Fatalf("fix = %q", fix)
	}
}
