package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func decodeHook(t *testing.T, out *bytes.Buffer) (string, string) {
	t.Helper()
	var got sessionStartOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("hook output is not valid JSON (%v): %s", err, out.String())
	}
	return got.HookSpecificOutput.HookEventName, got.HookSpecificOutput.AdditionalContext
}

func TestUserPromptSubmitHookDeliversRelevantKnowledge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			t.Error("prompt was not forwarded")
		}
		w.Write([]byte("## Possibly relevant\n\n- [note|machine:workstation-a] Ollama inventory — …\n"))
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	stdin := strings.NewReader(`{"prompt":"welches modell in ollama","cwd":"/tmp"}`)
	if code := cmdHookWith(stdin, []string{"user-prompt-submit"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	event, ctx := decodeHook(t, &out)
	if event != "UserPromptSubmit" {
		t.Errorf("event = %q", event)
	}
	if !strings.Contains(ctx, "Ollama inventory") {
		t.Errorf("relevant knowledge missing: %q", ctx)
	}
}

// The hook runs between pressing enter and the model seeing the prompt. A server
// that hangs must cost nothing but the timeout, and must still emit a payload
// the harness can parse — an empty one.
func TestUserPromptSubmitHookSurvivesADeadServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	started := time.Now()
	stdin := strings.NewReader(`{"prompt":"anything","cwd":"/tmp"}`)
	if code := cmdHookWith(stdin, []string{"user-prompt-submit"}, &out); code != 0 {
		t.Fatalf("exit = %d, a hook must never fail the turn", code)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("hook blocked for %s, want the short timeout", elapsed)
	}
	event, ctx := decodeHook(t, &out)
	if event != "UserPromptSubmit" || ctx != "" {
		t.Errorf("dead server must yield an empty context, got %q / %q", event, ctx)
	}
}

// An empty prompt is not worth a round trip.
func TestUserPromptSubmitHookSkipsAnEmptyPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be called for an empty prompt")
	}))
	defer srv.Close()
	withConfig(t, srv.URL)

	var out bytes.Buffer
	if code := cmdHookWith(strings.NewReader(`{"prompt":"   "}`), []string{"user-prompt-submit"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if _, ctx := decodeHook(t, &out); ctx != "" {
		t.Errorf("context = %q, want empty", ctx)
	}
}
