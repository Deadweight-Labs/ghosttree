package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/installer"
)

func cmdInstall(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: ctx install claude|codex|opencode")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stdout, "home dir: %v\n", err)
		return 1
	}
	var changes []installer.Change
	switch args[0] {
	case "claude":
		changes, err = installer.InstallClaude(home)
	case "codex":
		changes, err = installer.InstallCodex(home)
	case "opencode":
		changes, err = installer.InstallOpencode(home)
	default:
		fmt.Fprintln(stdout, "usage: ctx install claude|codex|opencode")
		return 2
	}
	for _, c := range changes {
		fmt.Fprintf(stdout, "%-12s %s\n", c.Action, c.Path)
	}
	if err != nil {
		fmt.Fprintf(stdout, "install %s: %v\n", args[0], err)
		return 1
	}
	if args[0] == "claude" {
		fmt.Fprintln(stdout, "restart Claude Code to pick up the MCP server and hook")
	}
	return 0
}
