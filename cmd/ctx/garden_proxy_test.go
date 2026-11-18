package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/aiproxy"
	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const gardenProxyKey = "sk-ant-api03-GARDENorgKEY-7777"

// TestGardenProxyClaudeHelper is the part of the stand-in for Claude Code that
// talks to the model: it calls what the session's environment points it at,
// the way Claude Code does, and notes what it used. Started by the script of
// TestGardenerRunsOverTheModelProxyWithTheAliasOfItsOwner.
func TestGardenProxyClaudeHelper(t *testing.T) {
	if os.Getenv("GT_GARDEN_PROXY_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	model := ""
	for i, a := range os.Args {
		if a == "--model" && i+1 < len(os.Args) {
			model = os.Args[i+1]
		}
	}
	body := fmt.Sprintf(`{"model":%q,"max_tokens":300,"messages":[{"role":"user","content":"prüfe"}]}`, model)
	status := 0
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+os.Getenv("ANTHROPIC_AUTH_TOKEN"))
		req.Header.Set("anthropic-version", "2023-06-01")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
	}
	f, err := os.OpenFile(os.Getenv("GT_GARDEN_PROXY_RECORD"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, "base=%s token=%s model=%s small=%s apikey=%s status=%d\n", os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv("ANTHROPIC_AUTH_TOKEN"),
			model, os.Getenv("ANTHROPIC_SMALL_FAST_MODEL"), os.Getenv("ANTHROPIC_API_KEY"), status)
		f.Close()
	}
	os.Exit(0)
}

// fakeOrgProvider answers like the Messages API and notes key and model of
// every call that reaches it.
type fakeOrgProvider struct {
	mu     sync.Mutex
	keys   []string
	models []string
}

func (f *fakeOrgProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &req)
	f.mu.Lock()
	f.keys, f.models = append(f.keys, r.Header.Get("x-api-key")), append(f.models, req.Model)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2000,"output_tokens":100}}`)
}

// AC-695.3: the gardener of an organization runs over the server's model
// proxy, with the alias its owner chose and the connection behind it: the
// unit's runner gets a run token with the start of the run, the sessions call
// the proxy with it, the provider gets the organization's key, the call is
// booked on the gardener's run, and the token ends with the run. Without an
// alias the runner keeps its llm.json.
func TestGardenerRunsOverTheModelProxyWithTheAliasOfItsOwner(t *testing.T) {
	fx := newProvenanceFixture(t)
	st := fx.st
	org, err := st.OrgByRef("acme")
	if err != nil {
		t.Fatal(err)
	}

	// The organization's connection and the alias of the gardener.
	provider := &fakeOrgProvider{}
	upstream := httptest.NewTLSServer(provider)
	t.Cleanup(upstream.Close)
	box, _ := secretbox.New(bytes.Repeat([]byte{5}, 32))
	conn, err := st.SaveAIConnection("person:1", store.AIConnectionInput{OrgID: org.ID, Name: "acme-anthropic", Format: store.AIFormatAnthropic,
		BaseURL: upstream.URL, KeyLast4: secretbox.Last4(gardenProxyKey), Models: []store.AIModel{{Model: "claude-sonnet-5-5", InUSD: 3, OutUSD: 15}}},
		func(purpose string) ([]byte, error) { return box.Seal(gardenProxyKey, purpose) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAIAlias("person:1", store.AIAlias{Level: store.AILevelOwner, OrgID: org.ID, Alias: "gaertner", ConnectionID: conn.ID, Model: "claude-sonnet-5-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGardenSettings("person:1", store.GardenSettings{OrgID: org.ID, Enabled: true, Frequency: store.GardenDaily, AtMinute: 0,
		Repos: []string{provenanceProject}, BudgetRun: 3, BudgetMon: 90, ModelAlias: "gaertner"}); err != nil {
		t.Fatal(err)
	}

	// The server with its model proxy, as ctx serve starts them.
	proxy := &aiproxy.Proxy{Store: st, Box: box, FileKeys: map[int64]string{},
		Upstream: func(store.AIConnection, *url.URL) (*http.Client, error) { return upstream.Client(), nil }}
	proxySrv := httptest.NewServer(proxy)
	t.Cleanup(proxySrv.Close)
	api := httptest.NewServer(server.New(st, server.WithAIProxy(proxySrv.URL)))
	t.Cleanup(api.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerURL = api.URL
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	// Claude Code's stand-in: the helper calls the model, then the fixture's
	// script answers the session.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(fx.dir, "model-calls")
	claude := filepath.Join(fx.dir, "claude-proxy")
	script := "#!/bin/sh\nprompt=$(cat)\n" +
		"GT_GARDEN_PROXY_HELPER=1 GT_GARDEN_PROXY_RECORD=" + record + " " + self + " -test.run='^TestGardenProxyClaudeHelper$' -- \"$@\" >/dev/null 2>&1\n" +
		"printf '%s' \"$prompt\" | " + fx.claude + " \"$@\"\n"
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// The runner, as the unit starts it: run-due with a checkout per repository.
	reposDir := filepath.Join(fx.dir, "repos")
	if err := os.MkdirAll(filepath.Join(reposDir, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fx.repo.Root, filepath.Join(reposDir, "acme", "ghosttree")); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(fx.dir, "state.json")
	spend := func() (int64, error) {
		fst, err := openFileState(statePath)
		if err != nil {
			return 0, err
		}
		u, err := fst.LLMUsageMonth(llmusage.JobGarden, time.Now().UTC().Format("2006-01"))
		return u.CostMicroUSD, err
	}
	var out bytes.Buffer
	run := func(extra []string, ai *store.GardenAI) int {
		full := append([]string{"check-knowledge"}, extra...)
		full = append(full, "--state-file", statePath, "--claude", claude, "--claude-dir", filepath.Join(fx.dir, "sessions"), "--sessions", "1", "--no-mcp")
		return cmdGardenWith(full, &out, ai)
	}
	gardener := client.New(cfg)
	if code := runGardenDue(gardener, reposDir, "", spend, run, &out); code != 0 {
		t.Fatalf("run-due: exit %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "model gaertner over the server's model proxy (connection acme-anthropic)") {
		t.Fatalf("run-due output:\n%s", out.String())
	}

	// The sessions called the proxy with the run's token and the alias.
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("no session called a model: %v\n%s", err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var token string
	for _, l := range lines {
		f := map[string]string{}
		for _, kv := range strings.Fields(l) {
			k, v, _ := strings.Cut(kv, "=")
			f[k] = v
		}
		if f["base"] != proxySrv.URL || !strings.HasPrefix(f["token"], store.AIRunTokenPrefix) || f["model"] != "gaertner" || f["small"] != "gaertner" ||
			f["apikey"] != "" || f["status"] != "200" {
			t.Fatalf("a session's model call: %s", l)
		}
		token = f["token"]
	}
	// The provider got the organization's key and the real model.
	if len(provider.keys) != len(lines) || provider.keys[0] != gardenProxyKey || provider.models[0] != "claude-sonnet-5-5" {
		t.Fatalf("provider saw keys %d of %d, model %v", len(provider.keys), len(lines), provider.models)
	}
	// Booked on the gardener's run, in the organization's cost overview.
	rep, err := st.AICostReportOf(org.ID, store.AIMonthStart())
	if err != nil || rep.Calls != len(lines) || len(rep.ByWorkflow) != 1 || rep.ByWorkflow[0].Key != store.GardenBlock || rep.ByConnection[0].Key != "acme-anthropic" {
		t.Fatalf("cost report: %+v %v", rep, err)
	}
	runs, _ := st.GardenRuns(org.ID, 5)
	if len(runs) != 1 || runs[0].State != "done" {
		t.Fatalf("garden runs: %+v\n%s", runs, out.String())
	}
	if u, _ := st.AIRunUsageOf(runs[0].WorkflowRunID); u.Calls != len(lines) || u.Connections[0] != "acme-anthropic" {
		t.Fatalf("the workflow run of the gardener: %+v", u)
	}
	// The token ended with the run.
	req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+aiproxy.PathAnthropic, strings.NewReader(`{"model":"gaertner","max_tokens":5,"messages":[]}`))
	req.Header.Set("x-api-key", token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the token after the run: %v %v", resp, err)
	}

	// Without an alias the runner gets nothing from the server and keeps its
	// llm.json.
	g, _ := st.GardenSettingsFor(org.ID)
	g.ModelAlias = ""
	if err := st.SetGardenSettings("person:1", g); err != nil {
		t.Fatal(err)
	}
	start, err := gardener.StartGardenRun(org.ID, provenanceProject)
	if err != nil || start.AI != nil || start.Fallback != store.GardenAINoAlias {
		t.Fatalf("start without an alias: %+v %v", start, err)
	}
	_ = gardener.FinishGardenRun(start.ID, "done", 0, "")
}
