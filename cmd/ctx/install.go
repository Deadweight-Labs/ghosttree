package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

type repeatedStrings []string

func (v *repeatedStrings) String() string { return strings.Join(*v, ",") }

func (v *repeatedStrings) Set(value string) error {
	*v = append(*v, value)
	return nil
}

const installUsage = `usage: ctx install claude|codex|opencode [--only component]... [--no-watch]
       ctx install watch

  claude, codex, opencode  wire the harness: hooks, MCP server, rules, skills.
                           claude and codex also set up the collector service.
  watch                    set up only the collector service (ctx watch as a
                           systemd user unit on Linux, a LaunchAgent on macOS)
  --only component         install only this component (mcp, hooks, rules,
                           skills; repeatable)
  --no-watch               do not set up the collector service
`

// Replaceable for tests: the binary whose path goes into configuration.
var installCtxExecutable = installer.ResolveCtxExecutable

// ctxHint is how to type this binary in a command we print: the bare name when
// the shell finds this very binary under it, the absolute path otherwise,
// because the printed command has to work in a shell whose PATH lacks ctx.
func ctxHint() string {
	home, _ := os.UserHomeDir()
	exe, err := installCtxExecutable(home)
	if err != nil {
		return "ctx"
	}
	if p, err := exec.LookPath("ctx"); err == nil {
		a, errA := os.Stat(p)
		b, errB := os.Stat(exe)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			return "ctx"
		}
	}
	return shellWord(exe)
}

func shellWord(w string) string {
	if w != "" && !strings.ContainsAny(w, " \t\"'\\$`&|;<>()*?[]{}#~!") {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// useRunningCtx makes the installer write the absolute path of the running
// binary and returns the function that restores the previous setting.
func useRunningCtx(home string) func() {
	prev := installer.CtxCommand()
	if exe, err := installCtxExecutable(home); err == nil && filepath.IsAbs(exe) {
		installer.SetCtxCommand(exe)
	}
	return func() { installer.SetCtxCommand(prev) }
}

func cmdInstall(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, installUsage)
		return 2
	}
	harness := args[0]
	if harness == "-h" || harness == "--help" || harness == "help" {
		fmt.Fprint(stdout, installUsage)
		return 0
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() { fmt.Fprint(stdout, installUsage) }
	var only repeatedStrings
	fs.Var(&only, "only", "install only this component (repeatable)")
	noWatch := fs.Bool("no-watch", false, "do not set up the collector service")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "home dir: %v\n", err)
		return 1
	}
	defer useRunningCtx(home)()
	if harness == "watch" {
		if len(only) != 0 {
			fmt.Fprintln(stdout, "ctx install watch takes no --only")
			return 2
		}
		return installCollector(stdout, home)
	}
	selected, err := installer.ResolveComponents(harness, only)
	if err != nil {
		if len(installer.SupportedComponents(harness)) == 0 {
			fmt.Fprintf(stdout, "unknown harness %q\n\n%s", harness, installUsage)
		} else {
			fmt.Fprintln(stdout, err)
		}
		return 2
	}
	changes, err := installer.InstallSelected(harness, home, selected)
	for _, c := range changes {
		fmt.Fprintf(stdout, "%-12s %s\n", c.Action, c.Path)
	}
	if err != nil {
		fmt.Fprintf(stdout, "install %s: %v\n", harness, err)
		return 1
	}
	if (harness == "claude" || harness == "codex") && len(only) == 0 && !*noWatch {
		installCollector(stdout, home)
	}
	restartStaleWatch(stdout)
	doctor := ctxHint() + " doctor " + harness
	for _, component := range only {
		doctor += " --only " + component
	}
	switch harness {
	case "codex":
		if selected[installer.ComponentHooks] {
			fmt.Fprintf(stdout, "next: run /hooks to trust changed ghosttree hooks, start a fresh Codex session, then %s\n", doctor)
		} else {
			fmt.Fprintf(stdout, "next: start a fresh Codex session, then %s\n", doctor)
		}
	case "claude":
		fmt.Fprintf(stdout, "next: restart Claude Code or start a fresh session, then %s\n", doctor)
	case "opencode":
		fmt.Fprintf(stdout, "next: restart OpenCode or start a fresh session, then %s\n", doctor)
	}
	return 0
}

// installCollector sets up the collector service when this machine has a
// ghosttree connection. Without one `ctx watch` would only fail and restart
// every thirty seconds, so the service waits for the join or setup that
// writes the config.
func installCollector(stdout io.Writer, home string) int {
	if _, err := config.Load(); err != nil {
		fmt.Fprintf(stdout, "collector   not set up: this machine is not connected yet; run 'ctx join' or 'ctx setup', then '%s install watch'\n", ctxHint())
		return 1
	}
	changes, err := installer.InstallWatchService(home)
	for _, c := range changes {
		fmt.Fprintf(stdout, "%-12s %s\n", c.Action, c.Path)
	}
	if err != nil {
		fmt.Fprintf(stdout, "collector service: %v\n", err)
		return 1
	}
	return 0
}
