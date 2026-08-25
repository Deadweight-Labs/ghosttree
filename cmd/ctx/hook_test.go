package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
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

// newRepo ist ein echtes Repo mit einem Commit. Der Hook fragt den Server nur,
// wenn ResolveGitContext ein Projekt UND eine Wurzel findet — ohne echtes Repo
// prüfte der Timeout-Test nichts, weil der Hook vorher aussteigt.
// Task 11 benutzt denselben Helfer.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"remote", "add", "origin", "https://github.com/test/repo.git"},
	} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"internal/store/knowledge.go", "internal/store/store.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package store\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Der Hook sitzt vor jedem Werkzeugaufruf. Ein hängender Server darf nichts
// kosten ausser dem Timeout, und die Ausgabe muss parsebar bleiben.
func TestPreToolUseHookSurvivesADeadServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	repo := newRepo(t)

	in := strings.NewReader(`{"session_id":"s1","cwd":"` + repo + `","tool_name":"Read","tool_input":{"file_path":"internal/store/knowledge.go"}}`)
	var out bytes.Buffer
	start := time.Now()
	code := cmdHookWith(in, []string{"pre-tool-use"}, &out)
	if code != 0 {
		t.Fatalf("a hook in front of a tool call must never fail, got exit %d", code)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("hook waited %v; the timeout is 900ms", time.Since(start))
	}
	event, ctx := decodeHook(t, &out)
	if event != "PreToolUse" {
		t.Fatalf("wrong event name: %q", event)
	}
	if ctx != "" {
		t.Fatalf("dead server must yield an empty context, got %q", ctx)
	}
}

func TestPreToolUseAsksForADescriptionOnlyWhenWriting(t *testing.T) {
	entries := []store.GhostFile{}
	fresh := map[string]ghost.Freshness{}

	reading := renderGhostDelivery(entries, fresh, false, false, "a.go", 0)
	if reading != "" {
		t.Fatalf("reading an undescribed file must stay silent, got %q", reading)
	}
	writing := renderGhostDelivery(entries, fresh, true, false, "a.go", 0)
	if !strings.Contains(writing, "context_describe_file") {
		t.Fatalf("writing an undescribed file must ask for one, got %q", writing)
	}
}

// Ein Pfad fehlt in der Auslieferung aus zwei Gruenden: es gibt nichts, oder es
// wurde in dieser Sitzung schon gesagt. Wer daraus "gibt es nicht" schliesst,
// behauptet beim zweiten Aendern derselben Datei, sie haette keine
// Beschreibung — und fordert eine an, die die vorhandene ersetzen wuerde.
// Dasselbe Muster wie bei null Suchtreffern (#732).
func TestSecondEditDoesNotClaimTheDescriptionIsMissing(t *testing.T) {
	described := `[{"path":"internal/store/knowledge.go","kind":"file","description":"die Wissenspfade"}]`
	var deliveries int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Die gezielte Nachfrage kennt keine Einmal-je-Sitzung-Regel.
		case strings.Contains(r.URL.Path, "/tree"):
			w.Write([]byte(described))
		default:
			deliveries++
			if deliveries == 1 {
				w.Write([]byte(described))
				return
			}
			w.Write([]byte(`[]`)) // schon gesagt
		}
	}))
	defer srv.Close()
	withConfig(t, srv.URL)
	repo := newRepo(t)

	edit := func() string {
		t.Helper()
		in := strings.NewReader(`{"session_id":"s1","cwd":"` + repo +
			`","tool_name":"Edit","tool_input":{"file_path":"internal/store/knowledge.go"}}`)
		var out bytes.Buffer
		if code := cmdHookWith(in, []string{"pre-tool-use"}, &out); code != 0 {
			t.Fatalf("exit %d", code)
		}
		_, ctx := decodeHook(t, &out)
		return ctx
	}

	if first := edit(); !strings.Contains(first, "die Wissenspfade") {
		t.Fatalf("beim ersten Mal kommt die Beschreibung mit, got %q", first)
	}
	second := edit()
	if strings.Contains(second, "keine Ghost-Datei") {
		t.Fatalf("die Datei IST beschrieben — der Hook darf das nicht bestreiten, got %q", second)
	}
}

func TestPreToolUseMarksAStaleDescriptionAsSuch(t *testing.T) {
	entries := []store.GhostFile{{Path: "a.go", Kind: "file", Description: "alte Beschreibung"}}
	fresh := map[string]ghost.Freshness{"a.go": {State: "stale", Percent: 61}}

	got := renderGhostDelivery(entries, fresh, false, true, "a.go", 0)
	if !strings.Contains(got, "alte Beschreibung") {
		t.Fatal("the description is still delivered")
	}
	if !strings.Contains(got, "61") || !strings.Contains(strings.ToUpper(got), "VERALTET") {
		t.Fatalf("a stale description must say how far it drifted, got %q", got)
	}
}
