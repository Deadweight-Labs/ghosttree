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

// Prüfbefund B1: automatische Läufe beanspruchen nie den Menschen im Terminal.
func TestAutomatedCommandsAreNeverATerminalPerson(t *testing.T) {
	oldTTY := ttyCheck
	t.Cleanup(func() { ttyCheck = oldTTY })
	ttyCheck = func(*os.File) bool { return true }
	for _, name := range append(agentEnvMarkers, "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE_SESSION_ID") {
		t.Setenv(name, "")
	}
	if !terminalPersonFor([]string{"pain", "add", "x"}) {
		t.Error("ctx pain add typed by a person is not a person")
	}
	for _, cmd := range []string{"garden", "watch", "mcp", "hook", "serve", "distill-sessions", "migrate", "explain", "claude", "channel"} {
		if terminalPersonFor([]string{cmd}) {
			t.Errorf("ctx %s claims a person", cmd)
		}
	}
	if !terminalPersonFor(nil) {
		t.Error("the terminal interface (ctx without arguments) is not a person")
	}
}
