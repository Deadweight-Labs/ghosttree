package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const cliReply = "Der Eintrag beschreibt eine Grenze des Wächters. Das trifft Agenten, die nur Messwerte tauschen. Empfehlung: freigeben."

// batchAPI is an Anthropic test server: the Messages endpoint of the single
// path and the Message Batches endpoints of the bulk path.
type batchAPI struct {
	mu        sync.Mutex
	singles   int
	submits   int
	requests  []map[string]any // params of every submitted request
	ended     bool
	failedID  string // custom_id answered with an error
	keysSeen  map[string]bool
	submitted []string
}

func (a *batchAPI) handler() http.Handler {
	mux := http.NewServeMux()
	note := func(r *http.Request) {
		a.mu.Lock()
		a.keysSeen[r.Header.Get("x-api-key")] = true
		a.mu.Unlock()
	}
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		a.mu.Lock()
		a.singles++
		a.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": cliReply}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	})
	mux.HandleFunc("/v1/messages/batches", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		var in struct {
			Requests []struct {
				CustomID string         `json:"custom_id"`
				Params   map[string]any `json:"params"`
			} `json:"requests"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.submits++
		for _, q := range in.Requests {
			a.requests = append(a.requests, q.Params)
			a.submitted = append(a.submitted, q.CustomID)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "msgbatch_" + strconv.Itoa(a.submits), "processing_status": "in_progress"})
	})
	mux.HandleFunc("/v1/messages/batches/", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		a.mu.Lock()
		defer a.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/v1/messages/batches/")
		if id, ok := strings.CutSuffix(path, "/results"); ok {
			_ = id
			for _, cid := range a.submitted {
				if cid == a.failedID {
					fmt.Fprintf(w, `{"custom_id":%q,"result":{"type":"errored","error":{"type":"error","error":{"type":"api_error","message":"overloaded"}}}}`+"\n", cid)
					continue
				}
				fmt.Fprintf(w, `{"custom_id":%q,"result":{"type":"succeeded","message":{"stop_reason":"end_turn","content":[{"type":"text","text":%q}],"usage":{"input_tokens":100000,"output_tokens":10000}}}}`+"\n", cid, cliReply)
			}
			return
		}
		status := "in_progress"
		if a.ended {
			status = "ended"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": path, "processing_status": status, "request_counts": map[string]int{"succeeded": len(a.submitted)}})
	})
	return mux
}

// explainFixture is a database with two entries waiting for review and an
// "explain" provider entry in llm.json that names a key file. The key file is
// written unless withKey is false.
func explainFixture(t *testing.T, withKey bool) (db, keyFile string, api *batchAPI) {
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

	api = &batchAPI{keysSeen: map[string]bool{}}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	keyFile = filepath.Join(dir, "anthropic-api-key")
	if withKey {
		if err := os.WriteFile(keyFile, []byte("sk-explain\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "llm.json")
	raw, _ := json.Marshal(map[string]any{
		// The proxy provider of the gardener and the distiller: explanations must never use it.
		"format": "anthropic", "base_url": "http://127.0.0.1:1", "model": "proxy-model", "api_key_file": "proxy-key",
		"explain": map[string]string{"base_url": srv.URL, "model": "claude-haiku-5-5", "api_key_file": keyFile},
	})
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "proxy-key"), []byte("dwl-proxy\n"), 0o600)
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	t.Setenv("GHOSTTREE_EXPLAIN_KEY", "")
	t.Setenv(envExplainModel, "")
	t.Setenv(envLanguage, "")
	for _, e := range []string{"GHOSTTREE_EXPLAIN_DAILY_CALLS", "GHOSTTREE_EXPLAIN_DAILY_TOKENS"} {
		t.Setenv(e, "")
		os.Unsetenv(e)
	}
	return db, keyFile, api
}

func runExplainCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := cmdExplain(args, &out)
	return code, out.String()
}

func TestExplainBatchSubmitsOnceThenCollectsAtHalfPrice(t *testing.T) {
	db, _, api := explainFixture(t, true)
	code, out := runExplainCmd(t, "backfill", "--db", db, "--batch", "--language", "de")
	if code != 0 || !strings.Contains(out, "submitted 2 objects in 1 batches") {
		t.Fatalf("submit: exit %d: %s", code, out)
	}
	if api.submits != 1 || len(api.requests) != 2 || api.singles != 0 {
		t.Fatalf("%d submits, %d requests, %d single calls", api.submits, len(api.requests), api.singles)
	}
	p := api.requests[0]
	if p["model"] != "claude-haiku-5-5" || p["max_tokens"] != float64(4096) || !strings.Contains(p["system"].(string), "Deutsch") {
		t.Errorf("params %v", p)
	}
	if !api.keysSeen["sk-explain"] || api.keysSeen["dwl-proxy"] || len(api.keysSeen) != 1 {
		t.Errorf("keys used: %v", api.keysSeen)
	}

	// The next hourly run while the batch is open: nothing is sent twice.
	code, out = runExplainCmd(t, "backfill", "--db", db, "--batch", "--language", "de")
	if code != 0 || api.submits != 1 || !strings.Contains(out, "submitted 0 objects") || !strings.Contains(out, "already waiting 2") || !strings.Contains(out, "still open 1") {
		t.Fatalf("second run: exit %d, %d submits: %s", code, api.submits, out)
	}
	code, out = runExplainCmd(t, "status", "--db", db)
	if code != 0 || !strings.Contains(out, "batches open: 1 (2 objects waiting)") || !strings.Contains(out, "missing or outdated: 2") {
		t.Errorf("status: %s", out)
	}

	api.mu.Lock()
	api.ended = true
	api.mu.Unlock()
	code, out = runExplainCmd(t, "backfill", "--db", db, "--batch", "--language", "de")
	if code != 0 || !strings.Contains(out, "collected 1 batches: saved 2, failed 0") || !strings.Contains(out, "nothing left") || api.submits != 1 {
		t.Fatalf("collect: exit %d: %s", code, out)
	}
	st, _ := store.Open(db)
	defer st.Close()
	e, ok, _ := st.Explanation(explain.KindKnowledge, 1)
	if !ok || e.Source != explain.SourceGenerated || e.Language != "de" || e.Model != "claude-haiku-5-5" || e.Text != cliReply {
		t.Errorf("stored: %+v", e)
	}
	// 2 x (100k input at 0.10 + 10k output at 0.50 per million) = $0.03 at list price; a batch costs half.
	u, _ := st.LLMUsageOn("explain", time.Now().UTC().Format("2006-01-02"))
	if u.Calls != 2 || u.InputTokens != 200000 || u.OutputTokens != 20000 || u.CostUSD() < 0.01499 || u.CostUSD() > 0.01501 {
		t.Errorf("usage %+v cost %.4f", u, u.CostUSD())
	}
}

func TestExplainCollectOnlyNeverSubmitsAndAFailedEntryDoesNotStopTheOthers(t *testing.T) {
	db, _, api := explainFixture(t, true)
	if code, out := runExplainCmd(t, "backfill", "--db", db, "--batch"); code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	api.mu.Lock()
	api.ended, api.failedID = true, "knowledge-1"
	api.mu.Unlock()
	code, out := runExplainCmd(t, "backfill", "--db", db, "--collect")
	if code != 0 || !strings.Contains(out, "saved 1, failed 1") || !strings.Contains(out, "knowledge 1: overloaded") || api.submits != 1 || strings.Contains(out, "submitted") {
		t.Fatalf("exit %d: %s", code, out)
	}
	st, _ := store.Open(db)
	defer st.Close()
	if _, ok, _ := st.Explanation(explain.KindKnowledge, 1); ok {
		t.Error("the failed entry got a text")
	}
	if _, ok, _ := st.Explanation(explain.KindKnowledge, 2); !ok {
		t.Error("the other entry lost its text")
	}
	// The failed one is not sent again at the next run.
	code, out = runExplainCmd(t, "backfill", "--db", db, "--batch")
	if code != 0 || api.submits != 1 || !strings.Contains(out, "already waiting 1") {
		t.Errorf("resubmitted: exit %d, %d submits: %s", code, api.submits, out)
	}
}

func TestExplainWithoutTheKeyFileSendsNothingUntilItAppears(t *testing.T) {
	db, keyFile, api := explainFixture(t, false)
	code, out := runExplainCmd(t, "backfill", "--db", db, "--batch")
	if code != 0 || !strings.Contains(out, "no model configured") || !strings.Contains(out, "does not exist yet") || !strings.Contains(out, "nothing sent") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if api.submits != 0 || api.singles != 0 || len(api.keysSeen) != 0 {
		t.Errorf("a request left without a key: %+v", api)
	}
	if code, out = runExplainCmd(t, "status", "--db", db); code != 0 || !strings.Contains(out, "no model configured") {
		t.Errorf("status: %s", out)
	}
	// Robin puts the key there: the next run submits.
	if err := os.WriteFile(keyFile, []byte("sk-explain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = runExplainCmd(t, "backfill", "--db", db, "--batch")
	if code != 0 || api.submits != 1 || !strings.Contains(out, "submitted 2 objects") {
		t.Fatalf("with the key: exit %d: %s", code, out)
	}
}

func TestExplainBackfillWithoutBatchIsRefusedAndTheLimitAndCapApply(t *testing.T) {
	db, _, api := explainFixture(t, true)
	if code, out := runExplainCmd(t, "backfill", "--db", db); code != 2 || !strings.Contains(out, "batch") || api.submits+api.singles != 0 {
		t.Errorf("one-by-one backfill: exit %d: %s", code, out)
	}
	code, out := runExplainCmd(t, "backfill", "--db", db, "--batch", "--limit", "1")
	if code != 0 || !strings.Contains(out, "submitted 1 objects") || len(api.requests) != 1 {
		t.Fatalf("limit 1: exit %d: %s", code, out)
	}
	// The one that waits counts as a call: a cap of one is used up.
	code, out = runExplainCmd(t, "backfill", "--db", db, "--batch", "--daily-calls", "1")
	if code != 0 || !strings.Contains(out, "stopped: daily calls limit reached") || api.submits != 1 {
		t.Errorf("cap: exit %d, %d submits: %s", code, api.submits, out)
	}
}

func TestExplainDryRunCallsNothing(t *testing.T) {
	db, _, api := explainFixture(t, true)
	code, out := runExplainCmd(t, "backfill", "--db", db, "--dry-run")
	if code != 0 || api.submits+api.singles != 0 || !strings.Contains(out, "missing or outdated: 2") || !strings.Contains(out, "model: claude-haiku-5-5") {
		t.Errorf("dry run: exit %d: %s", code, out)
	}
}

func TestTheProxyProviderIsNeverUsedForExplanations(t *testing.T) {
	db, _, _ := explainFixture(t, true)
	// Remove the explain entry: only the top-level proxy provider is left.
	dir := filepath.Dir(db)
	raw, _ := json.Marshal(map[string]any{"format": "anthropic", "base_url": "http://allgemein:18317", "model": "m", "api_key_file": "proxy-key"})
	os.WriteFile(filepath.Join(dir, "llm.json"), raw, 0o600)
	st, _ := store.Open(":memory:")
	defer st.Close()
	if svc := newExplainer(st, ExplainConfig{Mode: "auto", Language: "de"}, discardLogger()); svc.Enabled() {
		t.Error("the top-level proxy provider was used for explanations")
	}
	code, out := runExplainCmd(t, "backfill", "--db", db, "--batch")
	if code != 0 || !strings.Contains(out, "no model configured") {
		t.Errorf("exit %d: %s", code, out)
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
