package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const cliReply = "Der Eintrag beschreibt eine Grenze des Wächters. Das trifft Agenten, die nur Messwerte tauschen. Empfehlung: freigeben."

// explainFixture is a database with two entries waiting for review and a model
// provider (an OpenAI-format test server) named by the configuration file.
func explainFixture(t *testing.T) (db string, calls *atomic.Int32, prompts *[]string) {
	t.Helper()
	dir := t.TempDir()
	db = filepath.Join(dir, "g.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Erster Eintrag", "Zweiter Eintrag"} {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: title, Body: "Text von " + title, Confidence: "staged", Scope: scope.Axes{Project: "github.com/x/y"}}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	calls, prompts = new(atomic.Int32), new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.Messages) > 1 {
			*prompts = append(*prompts, in.Messages[1].Content)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": cliReply}}}})
	}))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte("k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"format": "openai", "base_url": srv.URL, "model": "m1", "api_key_file": "key"})
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	t.Setenv(envExplainModel, "")
	t.Setenv(envLanguage, "")
	return db, calls, prompts
}

func TestExplainBackfillWritesTheMissingOnesWithinTheLimit(t *testing.T) {
	db, calls, prompts := explainFixture(t)
	var out bytes.Buffer
	if code := cmdExplain([]string{"backfill", "--db", db, "--limit", "1", "--language", "de"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "generated 1") || calls.Load() != 1 {
		t.Errorf("limit 1: %s (%d calls)", out.String(), calls.Load())
	}
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "<<<DATEN") {
		t.Errorf("the model got no marked data: %v", *prompts)
	}
	// Second run: the first is current, the second gets its note, then nothing is left.
	out.Reset()
	if code := cmdExplain([]string{"backfill", "--db", db, "--limit", "5", "--language", "de"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "generated 1, already current 1") || !strings.Contains(out.String(), "nothing left") {
		t.Errorf("second run: %s", out.String())
	}
	st, _ := store.Open(db)
	defer st.Close()
	e, ok, _ := st.Explanation(explain.KindKnowledge, 1)
	if !ok || e.Source != explain.SourceGenerated || e.Language != "de" || e.Model != "m1" || e.Text != cliReply {
		t.Errorf("stored: %+v", e)
	}
	if u, _ := st.LLMUsageOn("explain", time.Now().UTC().Format("2006-01-02")); u.Calls != 2 || u.Tokens() == 0 {
		t.Errorf("usage: %+v", u)
	}
}

func TestExplainBackfillStopsAtTheDailyCapAndStatusShowsIt(t *testing.T) {
	db, calls, _ := explainFixture(t)
	var out bytes.Buffer
	if code := cmdExplain([]string{"backfill", "--db", db, "--limit", "10", "--daily-calls", "1"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "stopped: daily calls limit reached") || calls.Load() != 1 {
		t.Errorf("%s (%d calls)", out.String(), calls.Load())
	}
	out.Reset()
	cmdExplain([]string{"status", "--db", db, "--daily-calls", "1"}, &out)
	for _, want := range []string{"model: m1", "(cap 1 calls", "this month: 1 calls", "stored: 0 by agents, 1 generated", "missing or outdated: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q: %s", want, out.String())
		}
	}
}

func TestExplainDryRunAndMissingConfigurationCallNothing(t *testing.T) {
	db, calls, _ := explainFixture(t)
	var out bytes.Buffer
	if code := cmdExplain([]string{"backfill", "--db", db, "--dry-run"}, &out); code != 0 || calls.Load() != 0 || !strings.Contains(out.String(), "missing or outdated: 2") {
		t.Errorf("dry run: exit %d, %d calls: %s", code, calls.Load(), out.String())
	}
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	out.Reset()
	if code := cmdExplain([]string{"backfill", "--db", db}, &out); code != 0 || !strings.Contains(out.String(), "no model configured") || calls.Load() != 0 {
		t.Errorf("no configuration: exit %d: %s", code, out.String())
	}
}

func TestExplainUsageAndArguments(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"nonsense"}, {"backfill"}, {"backfill", "--db", "x", "--limit", "-1"}} {
		out.Reset()
		if code := cmdExplain(args, &out); code != 2 || !strings.Contains(out.String(), "usage: ctx explain") {
			t.Errorf("%v: exit %d: %s", args, code, out.String())
		}
	}
}

func TestServeReadsTheExplainSettingsFromFlagsAndEnvironment(t *testing.T) {
	t.Setenv(envExplain, "")
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("GHOSTTREE_EXPLAIN_DAILY_CALLS", "")
	t.Setenv("GHOSTTREE_EXPLAIN_DAILY_TOKENS", "")
	os.Unsetenv("GHOSTTREE_EXPLAIN_DAILY_CALLS")
	os.Unsetenv("GHOSTTREE_EXPLAIN_DAILY_TOKENS")
	cfg, err := parseServeConfig([]string{"--language", "de"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Explain.Mode != "auto" || cfg.Explain.Limits.Day.Calls != 200 || cfg.Explain.Limits.Day.Tokens != 400000 || cfg.Explain.Limits.Month.Any() || cfg.Explain.Language != "de" {
		t.Errorf("defaults: %+v", cfg.Explain)
	}
	t.Setenv("GHOSTTREE_EXPLAIN_DAILY_CALLS", "7")
	cfg, err = parseServeConfig([]string{"--explain", "off", "--explain-daily-tokens", "99", "--explain-model", "small"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Explain.Mode != "off" || cfg.Explain.Limits.Day.Calls != 7 || cfg.Explain.Limits.Day.Tokens != 99 || cfg.Explain.Model != "small" {
		t.Errorf("overrides: %+v", cfg.Explain)
	}
	if _, err := parseServeConfig([]string{"--explain", "maybe"}, &bytes.Buffer{}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestServerStartsWithoutAModelAndExplainsNothing(t *testing.T) {
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := newExplainer(st, ExplainConfig{Mode: "auto", Language: "de"}, discardLogger())
	if svc.Enabled() {
		t.Error("a model appeared from nowhere")
	}
	if off := newExplainer(st, ExplainConfig{Mode: ""}, discardLogger()); off.Enabled() {
		t.Error("an empty mode must count as off")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Every command that creates something for people takes --explanation, and
// the text lands beside the object.
func TestCLICreatingCommandsTakeAnExplanation(t *testing.T) {
	st := moveCLIFixture(t)
	note := "Das ist die Einordnung für Menschen. Es geht um eine Grenze, die später stört. Empfehlung: freigeben."
	run1 := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, out.String())
		}
		return out.String()
	}
	run1("request", "create", "--type", "feature", "--title", "Bauen", "--project", cliHere, "--ac", "c", "--explanation", note)
	run1("knowledge", "add", "--type", "note", "--title", "Wissen", "--body", "b", "--scope", "project", "--project", cliHere, "--explanation", note)
	run1("pain", "add", "Es hakt", "--project", cliHere, "--explanation", note)
	run1("task", "add", "--kind", "input", "--title", "Entscheiden", "--project", cliHere, "--request", "REQ-1", "--explanation", note)
	for _, kind := range []string{explain.KindRequest, explain.KindKnowledge, explain.KindPain, explain.KindTask} {
		e, ok, err := st.Explanation(kind, 1)
		if err != nil || !ok || e.Text != note || e.Source != explain.SourceAgent {
			t.Errorf("%s: %+v %v %v", kind, e, ok, err)
		}
	}
	var out bytes.Buffer
	leak := "Der Zugang sk-" + strings.Repeat("a1", 12) + " liegt hier im Klartext und das ist schlecht."
	if code := run([]string{"pain", "add", "Leck", "--project", cliHere, "--explanation", leak}, &out); code == 0 || strings.Contains(out.String(), "sk-a1") {
		t.Errorf("secret explanation accepted: %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"coord", "status", "--explanation", "x"}, &out); code != 2 || !strings.Contains(out.String(), "--explanation only applies to send") {
		t.Errorf("explanation on another coord command: %d %s", code, out.String())
	}
}
