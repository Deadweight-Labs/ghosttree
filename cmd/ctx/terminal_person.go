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
