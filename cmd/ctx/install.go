package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

type repeatedStrings []string

func (v *repeatedStrings) String() string { return strings.Join(*v, ",") }

func (v *repeatedStrings) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func cmdInstall(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: ctx install claude|codex|opencode [--only component]...")
		return 2
	}
	harness := args[0]
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var only repeatedStrings
	fs.Var(&only, "only", "install only this component (repeatable)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	selected, err := installer.ResolveComponents(harness, only)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "home dir: %v\n", err)
		return 1
	}
	changes, err := installer.InstallSelected(harness, home, selected)
	for _, c := range changes {
		fmt.Fprintf(stdout, "%-12s %s\n", c.Action, c.Path)
	}
	if err != nil {
		fmt.Fprintf(stdout, "install %s: %v\n", harness, err)
		return 1
	}
	doctor := "ctx doctor " + harness
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
