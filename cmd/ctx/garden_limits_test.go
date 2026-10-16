package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type meteredGardenModel struct {
	calls int
	usage llm.Usage
}

func (m *meteredGardenModel) CompleteWithUsage(context.Context, string, []llm.Message, int) (string, llm.Usage, error) {
	m.calls++
	return `{"verdict":"holds","reason":"ok"}`, m.usage, nil
}

func manyEntries(n int) []store.Knowledge {
	var out []store.Knowledge
	for i := 1; i <= n; i++ {
		out = append(out, store.Knowledge{ID: int64(i), Type: "pitfall", Title: "t", Body: "siehe cmd/x.go", Status: "active", Confidence: "trusted",
			Scope: scope.Axes{Project: gardenProject}, ObservedAt: "2000-01-01T00:00:00Z"})
	}
	return out
}

func checkedIDs(t *testing.T, st *store.Store) map[int64]bool {
	t.Helper()
	m, err := st.GardenChecks(gardenProject)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for id := range m {
		out[id] = true
	}
	return out
}

var gardenNow = func() time.Time { return time.Date(2026, 10, 16, 3, 5, 0, 0, time.UTC) }

// Rotation: the second run takes other entries than the first.
func TestGardenSecondRunTakesOtherEntriesThanTheFirst(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := gardenRepo(t)
	fake := &gardenFakeStore{entries: manyEntries(6)}
	m := &meteredGardenModel{usage: llm.Usage{InputTokens: 10, OutputTokens: 10}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 1, Limit: 2, State: st, MinAge: 14 * 24 * time.Hour, Now: gardenNow}
	var out bytes.Buffer
	if code := runGardenCheck(fake, repo, gardenProject, opts, m, gardenPrice, "", &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	first := checkedIDs(t, st)
	if len(first) != 2 || !first[1] || !first[2] {
		t.Fatalf("first run checked %v, want 1 and 2", first)
	}
	out.Reset()
	runGardenCheck(fake, repo, gardenProject, opts, m, gardenPrice, "", &out)
	second := checkedIDs(t, st)
	if len(second) != 4 || !second[3] || !second[4] {
		t.Fatalf("second run checked %v in total, want 3 and 4 added", second)
	}
	if m.calls != 4 {
		t.Fatalf("model calls %d, want 2+2 and none repeated", m.calls)
	}
	if !strings.Contains(out.String(), "unverändert") {
		t.Errorf("the report does not say what was left out: %s", out.String())
	}
}

// A dry run reads and rotates but remembers nothing and records no usage.
func TestGardenDryRunLeavesTheMemoryAlone(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	fake := &gardenFakeStore{entries: manyEntries(3)}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 1, DryRun: true, State: st, MinAge: 14 * 24 * time.Hour, Now: gardenNow}
	var out bytes.Buffer
	runGardenCheck(fake, gardenRepo(t), gardenProject, opts, nil, gardenPrice, "", &out)
	if len(checkedIDs(t, st)) != 0 {
		t.Error("a dry run remembered checks")
	}
	if u, _ := st.LLMUsageOn("garden", "2026-10-16"); u.Calls != 0 {
		t.Errorf("a dry run recorded usage: %+v", u)
	}
}

// The monthly limit holds over a restart because the usage is in the database.
func TestGardenMonthlyLimitHoldsAcrossRestartsAndNamesItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gt.db")
	fake := &gardenFakeStore{entries: manyEntries(8)}
	repo := gardenRepo(t)
	m := &meteredGardenModel{usage: llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5000}}
	run := func(limit int) (int, string) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 5, Limit: limit, State: st, MinAge: 14 * 24 * time.Hour, Now: gardenNow,
			Limits: llmusage.Limits{Month: llmusage.Limit{Calls: 3}}}
		var out bytes.Buffer
		code := runGardenCheck(fake, repo, gardenProject, opts, m, gardenPrice, "", &out)
		return code, out.String()
	}
	code, out := run(2)
	if code != 0 || m.calls != 2 {
		t.Fatalf("first run: exit %d, %d calls: %s", code, m.calls, out)
	}
	code, out = run(5)
	if code != 3 || m.calls != 3 || !strings.Contains(out, "stopped: monthly calls limit reached: 3 of 3") {
		t.Fatalf("second run: exit %d, %d calls in total: %s", code, m.calls, out)
	}
	// After a restart nothing more is allowed this month ...
	code, out = run(5)
	if code != 3 || m.calls != 3 || !strings.Contains(out, "monthly calls limit reached") {
		t.Fatalf("third run: exit %d, %d calls in total: %s", code, m.calls, out)
	}
	// ... and the stored usage carries the cache tokens.
	st, _ := store.Open(path)
	defer st.Close()
	if u, _ := st.LLMUsageMonth("garden", "2026-10"); u.Calls != 3 || u.CacheReadTokens != 15000 {
		t.Fatalf("stored usage %+v", u)
	}
}

// Dollar limits count the cache at its own price.
func TestGardenDailyDollarLimitCountsCacheTokens(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	fake := &gardenFakeStore{entries: manyEntries(6)}
	// 1M cache reads cost $0.20 at 2/M input; each call is about $0.2002.
	m := &meteredGardenModel{usage: llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 1_000_000}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 5, State: st, MinAge: 14 * 24 * time.Hour, Now: gardenNow,
		Limits: llmusage.Limits{Day: llmusage.Limit{USD: 0.35}}}
	var out bytes.Buffer
	code := runGardenCheck(fake, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out)
	if code != 3 || m.calls != 2 || !strings.Contains(out.String(), "daily limit reached: $0.40 of $0.35") {
		t.Fatalf("exit %d, %d calls: %s", code, m.calls, out.String())
	}
	u, _ := st.LLMUsageOn("garden", "2026-10-16")
	if u.CostUSD() < 0.40 || u.CostUSD() > 0.41 {
		t.Errorf("stored cost %.4f, want about 0.4004", u.CostUSD())
	}
}

// Without a database a run still honours the limits for itself.
func TestGardenLimitsApplyToOneRunWithoutADatabase(t *testing.T) {
	fake := &gardenFakeStore{entries: manyEntries(5)}
	m := &meteredGardenModel{usage: llm.Usage{InputTokens: 10, OutputTokens: 10}}
	opts := gardenOpts{Types: map[string]bool{"pitfall": true}, MaxUSD: 5, Now: gardenNow, Limits: llmusage.Limits{Day: llmusage.Limit{Calls: 2}}}
	var out bytes.Buffer
	if code := runGardenCheck(fake, gardenRepo(t), gardenProject, opts, m, gardenPrice, "", &out); code != 3 || m.calls != 2 {
		t.Fatalf("exit %d, %d calls: %s", code, m.calls, out.String())
	}
}

func TestLimitFlagsOverrideEnvironmentAndZeroMeansUnlimited(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "llm.json")
	os.WriteFile(cfg, []byte(`{"limits":{"garden":{"monthly":{"usd":50}}}}`), 0o600)
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	t.Setenv("GHOSTTREE_GARDEN_DAILY_CALLS", "9")
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	resolve := addLimitFlags(fs, llmusage.JobGarden, "")
	if err := fs.Parse([]string{"--monthly-usd", "0", "--daily-tokens", "1000", "--daily-calls", ""}); err != nil {
		t.Fatal(err)
	}
	lim, err := resolve()
	if err != nil || lim.Month.USD != 0 || lim.Day.Tokens != 1000 || lim.Day.Calls != 0 {
		t.Fatalf("%+v %v", lim, err)
	}
	// Without flags the file and the environment decide.
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	resolve = addLimitFlags(fs, llmusage.JobGarden, "")
	fs.Parse(nil)
	if lim, _ := resolve(); lim.Month.USD != 50 || lim.Day.Calls != 9 {
		t.Fatalf("%+v", lim)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	resolve = addLimitFlags(fs, llmusage.JobGarden, "")
	fs.Parse([]string{"--daily-usd", "abc"})
	if _, err := resolve(); err == nil {
		t.Error("a bad number was accepted")
	}
}

func TestCostShowsUsagePerJobDayAndMonthWithTheLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gt.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	st.AddLLMUsage("garden", today, llmusage.Usage{Calls: 12, InputTokens: 900, OutputTokens: 100, CacheReadTokens: 4000, CostMicroUSD: 1_250_000})
	st.AddLLMUsage("explain", today, llmusage.Usage{Calls: 3, InputTokens: 300})
	st.Close()
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("GHOSTTREE_GARDEN_MONTHLY_USD", "2")
	t.Setenv("GHOSTTREE_GARDEN_DAILY_USD", "")
	var out bytes.Buffer
	if code := cmdCost([]string{"--db", path}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	for _, want := range []string{"garden", "explain", "today", "this month", "12 calls", "5000 tokens", "$1.2500", "limit: $2.00", "limit: 200 calls, 400000 tokens", "(cache: 4000 read"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("cost output lacks %q:\n%s", want, out.String())
		}
	}
}
