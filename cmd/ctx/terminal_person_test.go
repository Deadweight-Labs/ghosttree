package main

import (
	"os"
	"testing"
)

func TestTerminalPersonOnlyWithoutAgentAndWithATerminal(t *testing.T) {
	oldTTY := ttyCheck
	t.Cleanup(func() { ttyCheck = oldTTY })
	clear := func() {
		for _, name := range append(agentEnvMarkers, "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE_SESSION_ID") {
			t.Setenv(name, "")
		}
	}
	tty := true
	ttyCheck = func(*os.File) bool { return tty }
	clear()
	if !terminalPerson() {
		t.Fatal("a terminal without agent markers is not a person")
	}
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "OPENCODE_SESSION_ID", "T3CODE_THREAD_ID", "GHOSTTREE_AGENT_ID"} {
		clear()
		t.Setenv(name, "x")
		if terminalPerson() {
			t.Errorf("%s set, still a person", name)
		}
	}
	clear()
	tty = false
	if terminalPerson() {
		t.Error("without a terminal it is a person")
	}
}
