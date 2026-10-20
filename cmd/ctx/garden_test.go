package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Without a key the gardener is off: one line, exit 1, nothing touched.
func TestGardenWithoutKeyStopsWithOneLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "empty"))
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("GHOSTTREE_GARDEN_KEY", "")
	var out bytes.Buffer
	code := cmdGarden([]string{"check-knowledge", "--mode", "api", "--project", "github.com/o/r", "--repo", dir}, &out)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n"); n != 0 || !strings.Contains(out.String(), "no API key") {
		t.Fatalf("want a single line naming the missing key, got %q", out.String())
	}
}

type gardenFakeStore struct {
	entries []store.Knowledge
	written []store.Knowledge
	// What a thread needs: the threads opened, what they are linked to, their
	// posts and the agents registered. threadErr makes opening a thread fail.
	threads   []store.Thread
	links     map[int64][]store.ThreadLink
	posts     []store.CoordMessage
	agents    []store.CoordAgent
	threadErr error
	// What jobs 4 and 1 read: the requests of the project, the stored
	// descriptions and the paths reviewed and left empty. requestsErr makes
	// reading the requests fail.
	requests    []requestdomain.Detail
	ghosts      []store.GhostFile
	reviews     []store.GhostReview
	requestsErr error
}

func (f *gardenFakeStore) SearchRequests(flt requestdomain.SearchFilter) (requestdomain.SearchPage, error) {
	var page requestdomain.SearchPage
	if f.requestsErr != nil {
		return page, f.requestsErr
	}
	for _, d := range f.requests {
		if flt.State == "" || d.Request.State == flt.State {
			page.Results = append(page.Results, requestdomain.SearchHit{Request: d.Request})
		}
	}
	return page, nil
}

func (f *gardenFakeStore) GetRequest(id int64) (requestdomain.Detail, error) {
	for _, d := range f.requests {
		if d.Request.ID == id {
			return d, nil
		}
	}
	return requestdomain.Detail{}, errors.New("no such request")
}

func (f *gardenFakeStore) GhostTree(string, string) ([]store.GhostFile, error) { return f.ghosts, nil }

func (f *gardenFakeStore) GhostReviews(string, string) ([]store.GhostReview, error) {
	return f.reviews, nil
}

func (f *gardenFakeStore) ProjectKnowledge(string, bool) ([]store.Knowledge, error) {
	return append(append([]store.Knowledge{}, f.entries...), f.written...), nil
}

func (f *gardenFakeStore) Remember(k store.Knowledge, _ scope.Axes) (store.Knowledge, error) {
	f.written = append(f.written, k)
	return k, nil
}

func (f *gardenFakeStore) RegisterCoordAgent(a store.CoordAgent) (int64, error) {
	f.agents = append(f.agents, a)
	return int64(len(f.agents)), nil
}

func (f *gardenFakeStore) ThreadsForObject(kind, id, _ string) ([]store.Thread, error) {
	var out []store.Thread
	for _, t := range f.threads {
		for _, l := range f.links[t.ID] {
			if l.Kind == kind && l.ID == id {
				out = append(out, t)
				break
			}
		}
	}
	return out, nil
}

func (f *gardenFakeStore) ThreadLinks(id int64, _ string) ([]store.ThreadLink, error) {
	return f.links[id], nil
}

func (f *gardenFakeStore) CreateThread(t store.Thread, _ string) (int64, error) {
	if f.threadErr != nil {
		return 0, f.threadErr
	}
	t.ID = int64(len(f.threads) + 1)
	t.State = store.ThreadOpen
	if f.links == nil {
		f.links = map[int64][]store.ThreadLink{}
	}
	if t.Link != nil {
		l := *t.Link
		l.ThreadID = t.ID
		f.links[t.ID] = append(f.links[t.ID], l)
	}
	f.threads = append(f.threads, t)
	return t.ID, nil
}

func (f *gardenFakeStore) LinkThread(l store.ThreadLink, _ string) error {
	f.links[l.ThreadID] = append(f.links[l.ThreadID], l)
	return nil
}

func (f *gardenFakeStore) SendCoordMessage(m store.CoordMessage) (int64, error) {
	f.posts = append(f.posts, m)
	return int64(len(f.posts)), nil
}

type gardenFakeModel struct{ calls int }

func (m *gardenFakeModel) CompleteWithUsage(context.Context, string, []llm.Message, int) (string, llm.Usage, error) {
	m.calls++
	return `{"verdict":"stale","reason":"in cmd/x.go:1 umbenannt","evidence":["cmd/x.go:1"]}`, llm.Usage{InputTokens: 800, OutputTokens: 80}, nil
}

func gardenRepo(t *testing.T) *garden.Repo {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "x.go"), []byte("package cmd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	repo, err := garden.OpenRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

const gardenProject = "github.com/o/r"

func gardenEntries() []store.Knowledge {
	mk := func(id int64, typ string) store.Knowledge {
		return store.Knowledge{ID: id, Type: typ, Title: "t", Body: "siehe cmd/x.go", Status: "active", Confidence: "trusted",
			Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-01T00:00:00Z"}
	}
	return []store.Knowledge{mk(1, "pitfall"), mk(2, "decision"), mk(3, "note")}
}

var gardenPrice = garden.Pricing{InUSDPerMTok: 2, OutUSDPerMTok: 10}

// --propose files new staged notes marked as gardener and touches nothing
// else: gardenStore has no way to verify, deprecate or delete.
func TestGardenProposeWritesOnlyStagedGardenerNotes(t *testing.T) {
	st := &gardenFakeStore{entries: gardenEntries()}
	m := &gardenFakeModel{}
	var out bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, Propose: true}
	if code := runGardenCheck(st, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if m.calls != 2 {
		t.Fatalf("model calls = %d, want the pitfall and the decision only", m.calls)
	}
	if len(st.written) != 2 {
		t.Fatalf("wrote %d proposals, want 2", len(st.written))
	}
	for _, k := range st.written {
		if k.Confidence == "verified" || k.Harness != "gardener" || k.Status != "active" || k.Origin == "human" {
			t.Errorf("proposal must be gardener-marked and not verified: %+v", k)
		}
	}
	for _, k := range st.entries {
		if k.Confidence != "trusted" || k.Status != "active" {
			t.Errorf("checked entry was changed: %+v", k)
		}
	}
	// Idempotent: the same run again adds nothing.
	before := len(st.written)
	runGardenCheck(st, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out)
	if len(st.written) != before {
		t.Errorf("second run wrote %d more", len(st.written)-before)
	}
}

func TestGardenWithoutProposeWritesNothingAndDryRunCallsNothing(t *testing.T) {
	st := &gardenFakeStore{entries: gardenEntries()}
	m := &gardenFakeModel{}
	var out bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1}
	runGardenCheck(st, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out)
	if len(st.written) != 0 {
		t.Fatalf("report-only run wrote %d entries", len(st.written))
	}
	m2 := &gardenFakeModel{}
	opts.DryRun, opts.Propose = true, true
	runGardenCheck(st, gardenRepo(t), gardenProject, opts, m2, gardenPrice, "", &out)
	if m2.calls != 0 || len(st.written) != 0 {
		t.Fatalf("dry run called %d times and wrote %d", m2.calls, len(st.written))
	}
}

func TestGardenBudgetAbortIsCleanAndExitsNonZero(t *testing.T) {
	st := &gardenFakeStore{entries: gardenEntries()}
	m := &gardenFakeModel{}
	var out bytes.Buffer
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 0.00001, Propose: true}
	code := runGardenCheck(st, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out)
	if code != 3 || m.calls != 0 || len(st.written) != 0 {
		t.Fatalf("code=%d calls=%d written=%d, want 3/0/0\n%s", code, m.calls, len(st.written), out.String())
	}
	if !strings.Contains(out.String(), "Budget erreicht") {
		t.Errorf("report does not say what was left unchecked:\n%s", out.String())
	}
}

// gardenFakeRunner stands in for Claude Code: it judges every entry of the
// package as holding, except that it reports a contradiction between the two
// entries it is told about.
type gardenFakeRunner struct {
	calls int
	text  func(prompt string) string
}

func (f *gardenFakeRunner) RunSession(_ context.Context, s garden.SessionSpec) (garden.SessionResult, error) {
	f.calls++
	return garden.SessionResult{Text: f.text(s.Prompt), Turns: 3,
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 300, CacheWriteTokens: 400}}, nil
}

func TestGardenAgentFilesProposalsAndRelationsOnceAndOnlyStaged(t *testing.T) {
	repo := gardenRepo(t)
	one := func(id int64, verdict, extra string) string {
		return fmt.Sprintf(`{"id":%d,"verdict":%q,"reason":"r"%s}`, id, verdict, extra)
	}
	st := &gardenFakeStore{entries: []store.Knowledge{
		{ID: 1, Type: "pitfall", Title: "eins", Body: "siehe cmd/x.go, Dienst läuft auf orax", Status: "active", Confidence: "trusted", Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-01T00:00:00Z"},
		{ID: 2, Type: "decision", Title: "zwei", Body: "siehe cmd/x.go, Dienst läuft auf apps-vm", Status: "active", Confidence: "trusted", Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-02T00:00:00Z"},
	}}
	fr := &gardenFakeRunner{text: func(string) string {
		return `{"verdicts":[` + one(1, "stale", `,"evidence":["cmd/x.go:1"]`) + "," + one(2, "holds", "") + `],` +
			`"relations":[{"kind":"contradiction","ids":[1,2],"reason":"Ort","quotes":[{"id":1,"text":"Dienst läuft auf orax"},{"id":2,"text":"Dienst läuft auf apps-vm"}]}]}`
	}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, Propose: true,
		Sessions: 4, PerSession: 30, Turns: 30, Runner: fr}
	var out bytes.Buffer
	if code := runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if fr.calls != 1 {
		t.Fatalf("sessions = %d, want one for the two entries", fr.calls)
	}
	// The contradiction is a thread now, not a note.
	if len(st.written) != 1 || len(st.threads) != 1 || len(st.posts) != 1 {
		t.Fatalf("wrote %d notes, %d threads, %d posts, want the stale proposal and one thread with its first post: %s",
			len(st.written), len(st.threads), len(st.posts), out.String())
	}
	for _, k := range st.written {
		if k.Harness != "gardener" || k.Confidence != "staged" || k.Status != "active" || k.Origin == "human" {
			t.Errorf("proposal must be staged and gardener-marked: %+v", k)
		}
	}
	if !strings.Contains(out.String(), "1 contradictions opened as threads") {
		t.Errorf("summary line: %s", out.String())
	}
	before, threads := len(st.written), len(st.threads)
	runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out)
	if len(st.written) != before || len(st.threads) != threads {
		t.Errorf("second run wrote %d notes and opened %d threads more", len(st.written)-before, len(st.threads)-threads)
	}
	for _, k := range st.entries {
		if k.Confidence != "trusted" || k.Status != "active" {
			t.Errorf("checked entry was changed: %+v", k)
		}
	}
}

func TestGardenAgentRecordsUsageAsJobGardenAndRemembersChecks(t *testing.T) {
	st := &gardenFakeStore{entries: gardenEntries()}
	state, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	fr := &gardenFakeRunner{text: func(p string) string {
		return `{"verdicts":[{"id":1,"verdict":"holds"},{"id":2,"verdict":"unclear"}]}`
	}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, State: state,
		Sessions: 4, PerSession: 30, Turns: 30, Runner: fr, MinAge: 14 * 24 * time.Hour}
	var out bytes.Buffer
	if code := runGardenAgent(st, gardenRepo(t), gardenProject, opts, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	u, _ := state.LLMUsageOn(llmusage.JobGarden, time.Now().UTC().Format("2006-01-02"))
	if u.Calls != 3 || u.CacheReadTokens != 300 || u.CacheWriteTokens != 400 || u.OutputTokens != 20 {
		t.Fatalf("usage %+v", u)
	}
	if m, _ := state.GardenChecks(gardenProject); len(m) != 2 {
		t.Fatalf("remembered %d checks, want 2", len(m))
	}
	// Recently checked and unchanged: the second run has nothing due.
	runGardenAgent(st, gardenRepo(t), gardenProject, opts, gardenPrice, "", &out)
	if fr.calls != 1 {
		t.Errorf("second run started %d sessions in total", fr.calls)
	}
}

func TestGardenAgentDailyLimitStopsBeforeTheFirstSession(t *testing.T) {
	st := &gardenFakeStore{entries: gardenEntries()}
	state, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.AddLLMUsage(llmusage.JobGarden, time.Now().UTC().Format("2006-01-02"), llmusage.Usage{Calls: 100}); err != nil {
		t.Fatal(err)
	}
	fr := &gardenFakeRunner{text: func(string) string { return `{"verdicts":[]}` }}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true, "decision": true}, MaxUSD: 1, State: state,
		Limits: llmusage.Limits{Day: llmusage.Limit{Calls: 50}}, Sessions: 4, PerSession: 30, Turns: 30, Runner: fr}
	var out bytes.Buffer
	if code := runGardenAgent(st, gardenRepo(t), gardenProject, opts, gardenPrice, "", &out); code != 3 || fr.calls != 0 {
		t.Fatalf("code=%d calls=%d\n%s", code, fr.calls, out.String())
	}
}

func TestGardenAgentModeFlagsAreChecked(t *testing.T) {
	var out bytes.Buffer
	for _, a := range []string{"--mode=nope", "--sessions=0", "--turns=3", "--per-session=0"} {
		out.Reset()
		if code := cmdGarden([]string{"check-knowledge", a}, &out); code != 2 {
			t.Errorf("%s: exit %d", a, code)
		}
	}
}

// --no-mcp takes the ghosttree tools away from the session and nothing else:
// the fixed git server (ctx itself) is started either way, only the ghosttree
// server, its allowlist entries and its mention in the prompt go. Run through
// cmdGarden with a stand-in for claude that records its command line.
func TestGardenNoMCPLeavesOnlyTheGhosttreeToolsOut(t *testing.T) {
	repo := gardenRepo(t)
	if out, err := exec.Command("git", "-C", repo.Root, "remote", "add", "origin", "https://github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gardenEntries()[:1])
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("GHOSTTREE_GARDEN_KEY", "")
	cfgJSON, _ := json.Marshal(map[string]string{"server_url": srv.URL, "token": "t"})
	if err := os.MkdirAll(filepath.Join(dir, "ghosttree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ghosttree", "config.json"), cfgJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > " + record + "\ncat > /dev/null\ncat <<'EOF'\n" +
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"{\"verdicts\":[{\"id\":1,\"verdict\":\"holds\",\"reason\":\"r\"}]}","usage":{"input_tokens":1,"output_tokens":1}}` + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	sessionArgs := func(extra ...string) map[string]string {
		t.Helper()
		os.Remove(record)
		var out bytes.Buffer
		args := append([]string{"check-knowledge", "--project", gardenProject, "--repo", repo.Root, "--claude", bin,
			"--claude-dir", filepath.Join(dir, "sessions"), "--sessions", "1"}, extra...)
		if code := cmdGarden(args, &out); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		raw, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("claude was not started: %v\n%s", err, out.String())
		}
		parts := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		got := map[string]string{}
		for i, a := range parts {
			if strings.HasPrefix(a, "--") && i+1 < len(parts) {
				got[a] = parts[i+1]
			}
		}
		return got
	}
	servers := func(args map[string]string) map[string]map[string]any {
		t.Helper()
		var cfg struct {
			Servers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(args["--mcp-config"]), &cfg); err != nil {
			t.Fatalf("mcp config: %v", err)
		}
		return cfg.Servers
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	with := sessionArgs()
	if s := servers(with); len(s) != 2 || s["ghosttree"] == nil || s[garden.GitServerName] == nil {
		t.Errorf("default: servers %v", s)
	}
	for _, w := range []string{"mcp__ghosttree__context_search", "mcp__ghosttree__context_get", "mcp__gardengit__git_log", "mcp__gardengit__git_show"} {
		if !strings.Contains(with["--allowedTools"], w) {
			t.Errorf("default allowlist lacks %s: %s", w, with["--allowedTools"])
		}
	}
	if !strings.Contains(with["--append-system-prompt"], "context_search") {
		t.Error("default prompt does not name the ghosttree tools")
	}

	without := sessionArgs("--no-mcp")
	s := servers(without)
	if len(s) != 1 || s["ghosttree"] != nil || s[garden.GitServerName] == nil {
		t.Fatalf("--no-mcp: servers %v, want only %s", s, garden.GitServerName)
	}
	if s[garden.GitServerName]["command"] != self {
		t.Errorf("git server is started with %v, want this binary %s", s[garden.GitServerName]["command"], self)
	}
	if strings.Contains(without["--allowedTools"], "ghosttree") || strings.Contains(without["--allowedTools"], "context_") {
		t.Errorf("--no-mcp allowlist: %s", without["--allowedTools"])
	}
	for _, w := range []string{"mcp__gardengit__git_log", "mcp__gardengit__git_show"} {
		if !strings.Contains(without["--allowedTools"], w) {
			t.Errorf("--no-mcp allowlist lacks %s: %s", w, without["--allowedTools"])
		}
	}
	if p := without["--append-system-prompt"]; strings.Contains(p, "context_") || strings.Contains(p, "ghosttree") || !strings.Contains(p, "git_log") {
		t.Errorf("--no-mcp prompt: %s", p)
	}
}

func TestAgentDenyReadSkipsWhatContainsTheCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := filepath.Join(home, ".config", "repo")
	got := agentDenyRead(root, filepath.Join(home, "claude"), filepath.Join(home, "data", "g.db"), "")
	has := func(p string) bool {
		for _, g := range got {
			if g == p {
				return true
			}
		}
		return false
	}
	if has(filepath.Join(home, ".config")) {
		t.Errorf("a directory that contains the checkout must not be denied: %v", got)
	}
	for _, want := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "claude"), filepath.Join(home, "data", "g.db"), filepath.Join(home, "data", "g.db-wal")} {
		if !has(want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}
