package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// transcriptWith writes a parent transcript whose last assistant message
// spawns the given subagents (type and prompt), as Claude Code writes it.
func transcriptWith(t *testing.T, calls ...[2]string) string {
	t.Helper()
	var content []map[string]any
	for i, c := range calls {
		content = append(content, map[string]any{"type": "tool_use", "id": "toolu_" + string(rune('a'+i)), "name": "Agent",
			"input": map[string]any{"subagent_type": c[0], "description": "task " + string(rune('a'+i)), "prompt": c[1]}})
	}
	lines := []map[string]any{
		{"type": "user", "message": map[string]any{"content": "please do it"}},
		{"type": "assistant", "message": map[string]any{"content": content}},
	}
	var b bytes.Buffer
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "parent.jsonl")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Parallel subagents of the same type get their own prompts, in order.
func TestSubagentTaskTakesEachPromptOnce(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := transcriptWith(t, [2]string{"Explore", "find the websocket code"}, [2]string{"general-purpose", "fix the flaky test"}, [2]string{"Explore", "list the migrations"})
	in := subagentStartInput{SessionID: "sess-parent", TranscriptPath: path, AgentType: "Explore"}
	if got := subagentTask(in); !strings.Contains(got, "find the websocket code") {
		t.Fatalf("first Explore: %q", got)
	}
	if got := subagentTask(in); !strings.Contains(got, "list the migrations") {
		t.Fatalf("second Explore: %q", got)
	}
	in.AgentType = "general-purpose"
	if got := subagentTask(in); !strings.Contains(got, "fix the flaky test") {
		t.Fatalf("general-purpose: %q", got)
	}
	if got := subagentTask(in); got != "" {
		t.Fatalf("a prompt was handed out twice: %q", got)
	}
}

// REQ-436, criterion 3: a subagent starts with the rules, the branch's work
// package with its open criteria, the pitfalls its task names and the mirror.
func TestSubagentStartBriefsTheSubagent(t *testing.T) {
	e := registerEnv(t)
	repo := coordRepo(t, "https://"+registerProject+".git")
	for _, args := range [][]string{{"checkout", "-q", "-b", "feat/sockets"}, {"commit", "-q", "--allow-empty", "-m", "start"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".ghosttree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".ghosttree", "INDEX.md"), []byte("# index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := e.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Websocket reconnect",
		Description: "d", Scope: scope.Axes{Project: registerProject}, Person: "robin"}, Criteria: []string{"reconnects after a server restart", "backs off"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreatePackage(requestdomain.PackageInput{Project: registerProject, Branch: "feat/sockets", Name: "Sockets",
		Members: []requestdomain.PackageMember{{RequestID: req.Request.ID, Role: "pflicht"}}, Person: "robin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "Websocket reconnect loops forever without jitter",
		Body: "The websocket client retried every 100 ms after a server restart.", Confidence: "trusted",
		Scope: scope.Axes{Project: registerProject}, Person: "robin"}); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{"session_id": "sess-brief", "cwd": repo, "agent_id": "agent-1", "agent_type": "general-purpose",
		"hook_event_name": "SubagentStart",
		"transcript_path": transcriptWith(t, [2]string{"general-purpose", "Make the websocket reconnect after a server restart"})}
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{"subagent-start", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	brief := got.HookSpecificOutput.AdditionalContext
	if got.HookSpecificOutput.HookEventName != "SubagentStart" {
		t.Fatalf("event %q", got.HookSpecificOutput.HookEventName)
	}
	for _, want := range []string{
		"context_search", "context_remember action=describe_file", "request action=criterion_met", // rules and tools
		"Work package", "Websocket reconnect", "reconnects after a server restart", // package and open criteria
		"loops forever without jitter",                // pitfall for the task
		filepath.Join(repo, ".ghosttree", "INDEX.md"), // mirror
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("briefing lacks %q:\n%s", want, brief)
		}
	}
	if n := len([]rune(brief)); n > subagentBriefMaxRunes+100 {
		t.Errorf("briefing has %d characters", n)
	}
}

// The synthetic probe of the installer gets a well-formed answer without a
// server call.
func TestSubagentStartAnswersTheProbe(t *testing.T) {
	t.Setenv("GHOSTTREE_HOOK_SYNTHETIC", "1")
	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(`{"cwd":"/tmp"}`), []string{"subagent-start", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), `"hookEventName":"SubagentStart"`) {
		t.Fatalf("probe answer %q", out.String())
	}
}
