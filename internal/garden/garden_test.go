package garden

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func newRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package internal\n\nfunc SessionDistillationExists() bool { return false }\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "distill.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "add distill")
	repo, err := OpenRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func entry(id int64, body string) Entry {
	return Entry{ID: id, Type: "pitfall", Title: fmt.Sprintf("entry %d", id), Body: body, ObservedAt: "2000-01-01T00:00:00Z"}
}

// fakeModel answers every call with a fixed text and usage and counts calls.
type fakeModel struct {
	text  string
	usage llm.Usage
	err   error
	calls int
}

func (f *fakeModel) CompleteWithUsage(context.Context, string, []llm.Message, int) (string, llm.Usage, error) {
	f.calls++
	return f.text, f.usage, f.err
}

var price = Pricing{InUSDPerMTok: 2, OutUSDPerMTok: 10}

func TestRefsAndContextComeFromTheCheckout(t *testing.T) {
	repo := newRepo(t)
	cc := repo.Gather(entry(1, "`SessionDistillationExists` in internal/distill.go und `GoneSymbol`"), ContextChars)
	if !cc.HasRefs() {
		t.Fatal("expected refs")
	}
	for _, want := range []string{"internal/distill.go:3:", "add distill", "Symbol GoneSymbol: KEIN Treffer"} {
		if !strings.Contains(cc.Text, want) {
			t.Errorf("context lacks %q:\n%s", want, cc.Text)
		}
	}
}

func TestEntryWithoutReferencesIsSkippedWithoutACall(t *testing.T) {
	repo := newRepo(t)
	m := &fakeModel{}
	res := Check(context.Background(), repo, []Entry{entry(1, "Nichts zum Nachschlagen hier.")}, Options{Model: m, Pricing: price, MaxUSD: 1})
	if m.calls != 0 || res.Verdicts[0].Kind != Skipped {
		t.Fatalf("calls=%d kind=%s", m.calls, res.Verdicts[0].Kind)
	}
}

func TestDryRunCallsNothingAndPricesTheRun(t *testing.T) {
	repo := newRepo(t)
	res := Check(context.Background(), repo, []Entry{entry(1, "`SessionDistillationExists`")}, Options{Pricing: price, MaxUSD: 1, DryRun: true})
	if res.Spent <= 0 || res.UpperBoundUSD < res.Spent || res.Verdicts[0].In == 0 {
		t.Fatalf("dry run not priced: %+v", res)
	}
}

func TestBudgetStopsBeforeTheCallThatCouldExceedIt(t *testing.T) {
	repo := newRepo(t)
	m := &fakeModel{text: `{"verdict":"holds","reason":"ok"}`, usage: llm.Usage{InputTokens: 4000, OutputTokens: 900}}
	entries := []Entry{entry(1, "`SessionDistillationExists`"), entry(2, "`SessionDistillationExists`"), entry(3, "`SessionDistillationExists`")}
	// One call costs $0.017 and its worst case is under $0.01: one fits.
	budget := price.Cost(5000, MaxOutputTokens) + 0.0005
	res := Check(context.Background(), repo, entries, Options{Model: m, Pricing: price, MaxUSD: budget})
	if res.StoppedBy != "budget" {
		t.Fatalf("StoppedBy = %q", res.StoppedBy)
	}
	if m.calls == 0 || m.calls >= len(entries) {
		t.Fatalf("calls = %d, want the run cut short", m.calls)
	}
	if res.Spent > budget {
		t.Fatalf("spent %.4f over budget %.4f", res.Spent, budget)
	}
	if last := res.Verdicts[len(res.Verdicts)-1]; last.Kind != NotRun {
		t.Fatalf("remaining entries must be reported as not run, got %s", last.Kind)
	}
	// A budget below a single worst case makes no call at all.
	m2 := &fakeModel{text: `{"verdict":"holds"}`}
	Check(context.Background(), repo, entries, Options{Model: m2, Pricing: price, MaxUSD: 0.00001})
	if m2.calls != 0 {
		t.Fatalf("made %d calls with no budget", m2.calls)
	}
}

func TestStaleNeedsEvidenceFromTheContext(t *testing.T) {
	repo := newRepo(t)
	e := entry(1, "`SessionDistillationExists`")
	invented := &fakeModel{text: `{"verdict":"stale","reason":"weg","evidence":["deadbeef","nowhere.go:9"]}`}
	res := Check(context.Background(), repo, []Entry{e}, Options{Model: invented, Pricing: price, MaxUSD: 1})
	if v := res.Verdicts[0]; v.Kind != Unclear || len(v.Unverified) != 2 {
		t.Fatalf("invented evidence must downgrade to unclear: %+v", v)
	}
	grounded := &fakeModel{text: `{"verdict":"stale","reason":"umbenannt","evidence":["internal/distill.go:3"]}`}
	res = Check(context.Background(), repo, []Entry{e}, Options{Model: grounded, Pricing: price, MaxUSD: 1})
	if res.Verdicts[0].Kind != Stale {
		t.Fatalf("grounded evidence should stand: %+v", res.Verdicts[0])
	}
}

type recordingSink struct{ written []store.Knowledge }

func (r *recordingSink) Remember(k store.Knowledge, _ scope.Axes) (store.Knowledge, error) {
	r.written = append(r.written, k)
	return k, nil
}

func TestProposalIsStagedGardenerMarkedAndNeverVerified(t *testing.T) {
	verdicts := []Verdict{
		{Entry: Entry{ID: 7, Title: "a"}, Kind: Stale, Reason: "weg", Evidence: []string{"abc1234"}},
		{Entry: Entry{ID: 8, Title: "b"}, Kind: Correction, Reason: "anders", Correction: "neu", Evidence: []string{"x.go:1"}},
		{Entry: Entry{ID: 9, Title: "c"}, Kind: Holds},
		{Entry: Entry{ID: 10, Title: "d"}, Kind: Unclear},
		{Entry: Entry{ID: 11, Title: "e"}, Kind: Failed},
	}
	sink := &recordingSink{}
	n, err := Propose(sink, "github.com/o/r", nil, verdicts)
	if err != nil || n != 2 || len(sink.written) != 2 {
		t.Fatalf("n=%d err=%v written=%d, want exactly the stale and the correction", n, err, len(sink.written))
	}
	for _, k := range sink.written {
		if k.Confidence == "verified" || k.ConfirmedBy != "" || k.Status != "active" {
			t.Errorf("proposal must be neither verified nor closing anything: %+v", k)
		}
		if k.Harness != "gardener" || !strings.HasPrefix(k.SessionRef, "gardener:") || !strings.HasPrefix(k.Title, "[Gärtner]") {
			t.Errorf("proposal lacks gardener provenance: %+v", k)
		}
	}
	// Running again over the stored proposals must not duplicate them.
	n, _ = Propose(sink, "github.com/o/r", sink.written, verdicts)
	if n != 0 {
		t.Errorf("second run wrote %d duplicates", n)
	}
}

// Sink exposes only Remember. This asserts at compile time that proposing has
// no route to verifying, deprecating or deleting an entry.
var _ Sink = (*recordingSink)(nil)

func TestLoadGardenConfigIsOffWithoutAKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GHOSTTREE_LLM_CONFIG", filepath.Join(dir, "absent.json"))
	t.Setenv("GHOSTTREE_GARDEN_KEY", "")
	if _, err := llm.LoadGardenConfig(); err != llm.ErrGardenDisabled {
		t.Fatalf("err = %v, want ErrGardenDisabled", err)
	}
	// An OpenAI distiller entry is not a gardener key.
	cfg := filepath.Join(dir, "llm.json")
	if err := os.WriteFile(cfg, []byte(`{"format":"openai","model":"x","api_key_file":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHOSTTREE_LLM_CONFIG", cfg)
	if _, err := llm.LoadGardenConfig(); err != llm.ErrGardenDisabled {
		t.Fatalf("distiller config must not enable the gardener: %v", err)
	}
	t.Setenv("GHOSTTREE_GARDEN_KEY", "sk-test")
	got, err := llm.LoadGardenConfig()
	if err != nil || got.APIKey != "sk-test" || got.Format != "anthropic" {
		t.Fatalf("env key not honoured: %v", err)
	}
}
