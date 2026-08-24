package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The payloads below are the required shape from the JSON schemas embedded in
// codex-cli 0.149.0 — session-start.command.input and
// user-prompt-submit.command.input, every required field present. They are kept
// here verbatim because the claim "both harnesses speak the same wire format"
// is the whole reason ghosttree serves Codex with one implementation instead of
// two. If Codex ever diverges, this is where it shows.
const (
	codexSessionStartInput = `{
	  "cwd": "/tmp",
	  "hook_event_name": "SessionStart",
	  "model": "test-model",
	  "permission_mode": "default",
	  "session_id": "01998a2b-0000-7000-8000-000000000000",
	  "source": "startup",
	  "transcript_path": null
	}`
	codexUserPromptInput = `{
	  "agent_id": "a1",
	  "agent_type": "primary",
	  "cwd": "/tmp",
	  "hook_event_name": "UserPromptSubmit",
	  "model": "test-model",
	  "permission_mode": "default",
	  "prompt": "welches modell in ollama",
	  "session_id": "01998a2b-0000-7000-8000-000000000000",
	  "transcript_path": null,
	  "turn_id": "t1"
	}`
)

// assertCodexOutput checks the reply against session-start.command.output and
// user-prompt-submit.command.output, both of which set additionalProperties
// false and require hookEventName inside hookSpecificOutput.
func assertCodexOutput(t *testing.T, out *bytes.Buffer, wantEvent string) string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("hook output is not JSON (%v): %s", err, out)
	}
	allowed := map[string]bool{"continue": true, "decision": true, "hookSpecificOutput": true,
		"reason": true, "stopReason": true, "suppressOutput": true, "systemMessage": true}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("top-level key %q is not in the Codex output schema", key)
		}
	}
	specific, ok := raw["hookSpecificOutput"]
	if !ok {
		t.Fatalf("no hookSpecificOutput: %s", out)
	}
	var inner map[string]any
	if err := json.Unmarshal(specific, &inner); err != nil {
		t.Fatal(err)
	}
	for key := range inner {
		if key != "hookEventName" && key != "additionalContext" {
			t.Errorf("hookSpecificOutput key %q is not in the Codex output schema", key)
		}
	}
	if inner["hookEventName"] != wantEvent {
		t.Errorf("hookEventName = %v, want %q", inner["hookEventName"], wantEvent)
	}
	context, _ := inner["additionalContext"].(string)
	return context
}

func TestSessionStartAnswersACodexPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") == "" && r.URL.Query().Get("machine") == "" {
			t.Error("no scope reached the server")
		}
		w.Write([]byte("## Known context (ghosttree)\n\n- [pitfall|machine:workstation-a] etwas Gelerntes — …\n"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(codexSessionStartInput), []string{"session-start"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if ctx := assertCodexOutput(t, &out, "SessionStart"); !strings.Contains(ctx, "etwas Gelerntes") {
		t.Errorf("context did not reach the payload: %q", ctx)
	}
}

func TestUserPromptSubmitAnswersACodexPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "welches modell in ollama" {
			t.Errorf("prompt forwarded as %q", got)
		}
		w.Write([]byte("## Possibly relevant\n\n- [note|machine:workstation-a] Ollama inventory — …\n"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(codexUserPromptInput), []string{"user-prompt-submit"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if ctx := assertCodexOutput(t, &out, "UserPromptSubmit"); !strings.Contains(ctx, "Ollama inventory") {
		t.Errorf("context did not reach the payload: %q", ctx)
	}
}
