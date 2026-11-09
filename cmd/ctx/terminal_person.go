package main

import (
	"os"
	"strings"
)

// agentEnvMarkers are environment variables an agent harness sets for the
// commands it runs (Claude Code, Codex, OpenCode, T3, ctx claude). One of them
// is enough: the process does not run for a person at a keyboard.
var agentEnvMarkers = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_AGENT_SDK_VERSION",
	"CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED",
	"OPENCODE", "OPENCODE_SESSION_ID", "T3CODE_THREAD_ID", agentIDEnv,
}

// Replaceable in tests.
var (
	ttyCheck = func(f *os.File) bool {
		st, err := f.Stat()
		return err == nil && st.Mode()&os.ModeCharDevice != 0
	}
	envLookup = os.LookupEnv
)

// terminalPerson says whether this ctx runs for a person typing in a
// terminal: no agent session is known, no harness has marked the environment,
// and both stdin and stdout are a terminal. It is a self-report the server
// records as such (REQ-620); it decides no right.
func terminalPerson() bool {
	if currentSessionRef() != "" {
		return false
	}
	for _, name := range agentEnvMarkers {
		if v, ok := envLookup(name); ok && strings.TrimSpace(v) != "" {
			return false
		}
	}
	return ttyCheck(os.Stdin) && ttyCheck(os.Stdout)
}

// automatedCommands run on their own (servers, watchers, hooks, agent loops,
// model jobs) even when started from a terminal: what they write is never a
// person typing, so they never claim one.
var automatedCommands = map[string]bool{
	"serve": true, "watch": true, "mcp": true, "channel": true, "claude": true, "hook": true,
	"garden": true, "explain": true, "distill-sessions": true, "migrate": true, "embed": true,
	"eval": true, "mirror": true, "snapshot": true,
}

func automatedCommand(cmd string) bool { return automatedCommands[cmd] }

// terminalPersonFor is terminalPerson for a command line: never for an
// automated command (ctx garden in a terminal is the gardener, not a person).
// ctx without arguments opens the terminal interface and counts like any
// other command a person types.
func terminalPersonFor(args []string) bool {
	return (len(args) == 0 || !automatedCommand(args[0])) && terminalPerson()
}
