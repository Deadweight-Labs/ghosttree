// Package gitsafe starts git for the client (hooks, collector, mirror) with the
// settings that keep a checkout from running its own programs. A working
// directory copied with its .git (an archive, a shared folder) brings its own
// .git/config: core.fsmonitor there is a program that `git status` starts. The
// command line beats the repository config, so these settings hold whatever the
// checkout says. safe.directory is left alone on purpose: git's refusal to use
// a checkout of another owner stays in force.
//
// What this does not cover: a filter or diff driver that .git/config defines
// and a tracked .gitattributes selects. Named drivers cannot be switched off
// with one option, and `git status` runs a clean filter only for a file whose
// stat data changed.
package gitsafe

import (
	"context"
	"os"
	"os/exec"
)

// Config are the options in front of every git subcommand.
var Config = []string{
	"--no-pager",
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.untrackedCache=false",
	"-c", "diff.external=",
}

// Args returns args behind the safe options.
func Args(args ...string) []string {
	return append(append(make([]string, 0, len(Config)+len(args)), Config...), args...)
}

// Env adds the settings that keep git from locking the index or asking at a
// terminal to env (os.Environ when nil).
func Env(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
}

// Command is exec.Command("git", args...) with the safe options.
func Command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", Args(args...)...)
	cmd.Env = Env(nil)
	return cmd
}

// CommandContext is Command with a context.
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", Args(args...)...)
	cmd.Env = Env(nil)
	return cmd
}
