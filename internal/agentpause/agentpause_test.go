package agentpause

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func stateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

const agent = "claude:h:11111111-2222-3333-4444-555555555555"

func TestGateOutputIsExactlyTheMeasuredFormat(t *testing.T) {
	stateEnv(t)
	if err := WriteFlag(agent, Flag{ControlID: 7, Action: "pause", By: "robin"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	paused := Gate(agent, strings.NewReader(`{"session_id":"s","tool_name":"Bash","tool_use_id":"toolu_1"}`), &out)
	if !paused {
		t.Fatal("flag is set, gate must pause")
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not json: %q", out.String())
	}
	want := map[string]any{
		"continue":   false,
		"stopReason": "Paused by ghosttree (control #7)",
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": got["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"],
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v\nwant keys %v", got, want)
	}
	reason := want["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.HasPrefix(reason, "Paused by ghosttree (control #7)") || !strings.Contains(reason, "Do not retry") {
		t.Fatalf("reason = %q", reason)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("exactly one line expected: %q", out.String())
	}
}

func TestGateWritesAnAckPerBlockedCall(t *testing.T) {
	stateEnv(t)
	_ = WriteFlag(agent, Flag{ControlID: 3, Action: "pause"})
	var out bytes.Buffer
	Gate(agent, strings.NewReader(`{"session_id":"s1","tool_name":"Write","tool_use_id":"toolu_a","agent_id":"sub9","agent_type":"general-purpose"}`), &out)
	Gate(agent, strings.NewReader(`{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_b"}`), &out)
	acks, next := ReadAcks(agent, 0)
	if len(acks) != 2 || next == 0 {
		t.Fatalf("acks = %+v", acks)
	}
	a := acks[0]
	if a.ControlID != 3 || a.ToolUseID != "toolu_a" || a.ToolName != "Write" || a.AgentID != "sub9" || a.SessionID != "s1" || a.At == "" {
		t.Fatalf("ack = %+v", a)
	}
	if acks[1].AgentID != "" || acks[1].ToolUseID != "toolu_b" {
		t.Fatalf("main-session ack must carry no agent_id: %+v", acks[1])
	}
	more, _ := ReadAcks(agent, next)
	if len(more) != 0 {
		t.Fatalf("offset must skip what was read: %+v", more)
	}
}

func TestGateIsSilentWithoutFlagOrIdentity(t *testing.T) {
	stateEnv(t)
	var out bytes.Buffer
	if Gate(agent, strings.NewReader(`{}`), &out) || out.Len() != 0 {
		t.Fatalf("no flag: paused=%v out=%q", false, out.String())
	}
	_ = WriteFlag(agent, Flag{ControlID: 1})
	if Gate("", strings.NewReader(`{}`), &out) || out.Len() != 0 {
		t.Fatal("without an agent identity there is nothing to look up")
	}
	// Das Flag eines anderen Agenten gilt nicht.
	if Gate("claude:h:other", strings.NewReader(`{}`), &out) || out.Len() != 0 {
		t.Fatal("another agent's flag must not pause this one")
	}
}

func TestGateFailsClosedOnlyForItsOwnDecision(t *testing.T) {
	stateEnv(t)
	dir, _ := Dir(agent)
	_ = os.MkdirAll(dir, 0o700)
	// Kaputtes Flag: die Pause gilt trotzdem.
	_ = os.WriteFile(filepath.Join(dir, flagName), []byte("{not json"), 0o600)
	var out bytes.Buffer
	if !Gate(agent, strings.NewReader(`garbage`), &out) {
		t.Fatal("a corrupt flag must still pause")
	}
	var got map[string]any
	if json.Unmarshal(out.Bytes(), &got) != nil || got["continue"] != false {
		t.Fatalf("output = %q", out.String())
	}
	// Unlesbarer Stdin aendert daran nichts.
	out.Reset()
	if !Gate(agent, errReader{}, &out) {
		t.Fatal("a broken stdin must not lift the pause")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, os.ErrClosed }

func TestGateIsFast(t *testing.T) {
	stateEnv(t)
	_ = WriteFlag(agent, Flag{ControlID: 1})
	start := time.Now()
	for i := 0; i < 50; i++ {
		Gate(agent, strings.NewReader(`{"tool_use_id":"x"}`), &bytes.Buffer{})
	}
	if per := time.Since(start) / 50; per > 20*time.Millisecond {
		t.Fatalf("gate takes %v per call", per)
	}
}

func TestFlagRoundTripAndRemove(t *testing.T) {
	stateEnv(t)
	if _, ok := ReadFlag(agent); ok {
		t.Fatal("no flag yet")
	}
	if err := WriteFlag(agent, Flag{ControlID: 9, Action: "interrupt", By: "ben", Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	f, ok := ReadFlag(agent)
	if !ok || f.ControlID != 9 || f.Action != "interrupt" || f.By != "ben" {
		t.Fatalf("flag = %+v %v", f, ok)
	}
	RemoveFlag(agent)
	RemoveFlag(agent) // idempotent
	if _, ok := ReadFlag(agent); ok {
		t.Fatal("flag must be gone")
	}
}

func TestDirIsPerAgentAndDoesNotLeakTheIdentityIntoThePath(t *testing.T) {
	stateEnv(t)
	a, _ := Dir("claude:h:a")
	b, _ := Dir("claude:h:b")
	if a == b || strings.Contains(a, "claude") {
		t.Fatalf("dirs %q %q", a, b)
	}
}
