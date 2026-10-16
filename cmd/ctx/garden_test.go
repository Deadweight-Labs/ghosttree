package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
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
}

func (f *gardenFakeStore) ProjectKnowledge(string, bool) ([]store.Knowledge, error) {
	return append(append([]store.Knowledge{}, f.entries...), f.written...), nil
}

func (f *gardenFakeStore) Remember(k store.Knowledge, _ scope.Axes) (store.Knowledge, error) {
	f.written = append(f.written, k)
	return k, nil
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
	if len(st.written) != 2 {
		t.Fatalf("wrote %d, want the stale proposal and the contradiction: %s", len(st.written), out.String())
	}
	for _, k := range st.written {
		if k.Harness != "gardener" || k.Confidence != "staged" || k.Status != "active" || k.Origin == "human" {
			t.Errorf("proposal must be staged and gardener-marked: %+v", k)
		}
	}
	if !strings.Contains(out.String(), "1 of them about duplicates or contradictions") {
		t.Errorf("summary line: %s", out.String())
	}
	before := len(st.written)
	runGardenAgent(st, repo, gardenProject, opts, gardenPrice, "", &out)
	if len(st.written) != before {
		t.Errorf("second run wrote %d more", len(st.written)-before)
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
