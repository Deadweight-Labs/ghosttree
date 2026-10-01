package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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

func TestPauseGateAnswersTheDoctorProbeWithHarmlessJSON(t *testing.T) {
	pauseEnv(t)
	t.Setenv("GHOSTTREE_HOOK_SYNTHETIC", "1")
	// Even with a flag set, the synthetic probe must not block anything.
	_ = agentpause.WriteFlag(pauseTestAgent, agentpause.Flag{ControlID: 1})
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(`{"cwd":"/tmp","prompt":"","tool_input":{}}`), []string{"pause-gate", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["continue"] != nil || got["hookSpecificOutput"].(map[string]any)["hookEventName"] != "PreToolUse" ||
		got["hookSpecificOutput"].(map[string]any)["permissionDecision"] != nil {
		t.Fatalf("probe output = %q", out.String())
	}
	if acks, _ := agentpause.ReadAcks(pauseTestAgent, 0); len(acks) != 0 {
		t.Fatal("the probe must not write an ack")
	}
}

// ctx install claude --only hooks followed by ctx doctor claude --only hooks
// passes in a clean HOME, with the real binary on PATH (the probe runs it).
func TestInstallThenDoctorHooksPassesForClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "ctx"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GHOSTTREE_AGENT_ID", "")
	var out bytes.Buffer
	if code := cmdInstall([]string{"claude", "--only", "hooks"}, &out); code != 0 {
		t.Fatalf("install = %d\n%s", code, out.String())
	}
	out.Reset()
	if code := cmdDoctor([]string{"claude", "--only", "hooks"}, &out); code != 0 {
		t.Fatalf("doctor = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "pause-gate") {
		t.Fatalf("doctor does not mention the gate:\n%s", out.String())
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string  `json:"command"`
				Timeout float64 `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range s.Hooks["PreToolUse"] {
		for _, h := range g.Hooks {
			if strings.HasPrefix(h.Command, "ctx hook pause-gate") {
				found = true
				if h.Timeout != 5 || g.Matcher != "" {
					t.Fatalf("gate entry timeout=%v matcher=%q", h.Timeout, g.Matcher)
				}
			}
		}
	}
	if !found {
		t.Fatal("gate not installed")
	}
}
