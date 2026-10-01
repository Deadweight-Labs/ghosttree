package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/agentpause"
)

const pauseTestAgent = "claude:h:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func pauseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(agentIDEnv, pauseTestAgent)
	for _, k := range []string{"CODEX_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE_SESSION_ID"} {
		t.Setenv(k, "")
	}
}

func TestPauseGateHookPrintsTheMeasuredPauseOutput(t *testing.T) {
	pauseEnv(t)
	if err := agentpause.WriteFlag(pauseTestAgent, agentpause.Flag{ControlID: 12, Action: "pause", By: "robin"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := cmdHookWith(strings.NewReader(`{"session_id":"s","tool_name":"Bash","tool_use_id":"toolu_x"}`), []string{"pause-gate", "--harness", "claude"}, &out)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		Continue           *bool  `json:"continue"`
		StopReason         string `json:"stopReason"`
		HookSpecificOutput struct {
			HookEventName      string `json:"hookEventName"`
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%q: %v", out.String(), err)
	}
	if got.Continue == nil || *got.Continue || got.StopReason != "Paused by ghosttree (control #12)" ||
		got.HookSpecificOutput.HookEventName != "PreToolUse" || got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("output = %s", out.String())
	}
	if acks, _ := agentpause.ReadAcks(pauseTestAgent, 0); len(acks) != 1 || acks[0].ToolUseID != "toolu_x" {
		t.Fatalf("acks = %+v", acks)
	}
}

func TestPauseGateHookIsSilentAndOpenOtherwise(t *testing.T) {
	pauseEnv(t)
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(`{"tool_use_id":"t"}`), []string{"pause-gate", "--harness", "claude"}, &out); code != 0 || out.Len() != 0 {
		t.Fatalf("no flag: %d %q", code, out.String())
	}
	_ = agentpause.WriteFlag(pauseTestAgent, agentpause.Flag{ControlID: 1})
	// Codex hat keine gemessene Pause: das Flag gilt dort nicht.
	if code := cmdHookWith(strings.NewReader(`{}`), []string{"pause-gate", "--harness", "codex"}, &out); code != 0 || out.Len() != 0 {
		t.Fatalf("codex: %d %q", code, out.String())
	}
	// Ohne Launcher-Identitaet gibt es nichts nachzuschlagen.
	t.Setenv(agentIDEnv, "")
	if code := cmdHookWith(strings.NewReader(`{}`), []string{"pause-gate", "--harness", "claude"}, &out); code != 0 || out.Len() != 0 {
		t.Fatalf("no identity: %d %q", code, out.String())
	}
}

// Der Pausen-Zweig darf weder auf das Budget noch auf den Server warten.
func TestPauseGateHookDoesNotTouchTheBudgetPath(t *testing.T) {
	pauseEnv(t)
	t.Setenv("GHOSTTREE_URL", "http://127.0.0.1:1") // wuerde bei jedem Zugriff scheitern
	_ = agentpause.WriteFlag(pauseTestAgent, agentpause.Flag{ControlID: 5})
	var out bytes.Buffer
	start := time.Now()
	cmdHookWith(strings.NewReader(`{"session_id":"s","tool_use_id":"t"}`), []string{"pause-gate", "--harness", "claude"}, &out)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("pause-gate took %v", d)
	}
	// Kein Budget-Zustand entstanden: der Zweig hat hookbudget nie betreten.
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "ghosttree", "context-budget")); err == nil {
		t.Fatal("pause-gate created hookbudget state")
	}
}
