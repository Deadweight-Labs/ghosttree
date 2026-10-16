package garden

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

func ids(es []Entry) []int64 {
	var out []int64
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func commitAll(t *testing.T, repo *Repo, msg string) {
	t.Helper()
	if _, err := repo.git("add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg); err != nil {
		t.Fatal(err)
	}
}

var rotNow = time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)

// Rotation: entries never looked at come first, then the ones looked at longest ago.
func TestRotateTakesNeverCheckedFirstThenTheOldestLook(t *testing.T) {
	repo := newRepo(t)
	head, _ := repo.Head()
	es := []Entry{entry(1, "`GoneA`"), entry(2, "`GoneB`"), entry(3, "`GoneC`"), entry(4, "`GoneD`")}
	seen := map[int64]Seen{
		1: {CheckedAt: "2026-09-01T00:00:00Z", Commit: "unknown-commit", EntryHash: HashOf(es[0])},
		2: {CheckedAt: "2026-08-01T00:00:00Z", Commit: "unknown-commit", EntryHash: HashOf(es[1])},
	}
	_ = head
	out, rot := Rotate(repo, es, seen, 14*24*time.Hour, rotNow)
	if got := ids(out); !same(got, []int64{3, 4, 2, 1}) {
		t.Fatalf("order %v, want never-checked 3,4 then the oldest look 2 before 1", got)
	}
	if rot.Never != 2 || rot.Unchanged != 0 || rot.Recent != 0 {
		t.Errorf("rotation %+v", rot)
	}
}

// min-age is the fallback when the code cannot cheaply be compared.
func TestRotateSkipsWhatWasLookedAtWithinMinAgeWhenChangeIsNotKnowable(t *testing.T) {
	repo := newRepo(t)
	es := []Entry{entry(1, "`GoneA`"), entry(2, "`GoneB`")}
	seen := map[int64]Seen{
		1: {CheckedAt: "2026-10-10T00:00:00Z", Commit: "unknown-commit", EntryHash: HashOf(es[0])}, // 6 days
		2: {CheckedAt: "2026-09-20T00:00:00Z", Commit: "unknown-commit", EntryHash: HashOf(es[1])}, // 26 days
	}
	out, rot := Rotate(repo, es, seen, 14*24*time.Hour, rotNow)
	if got := ids(out); !same(got, []int64{2}) || rot.Recent != 1 {
		t.Fatalf("got %v, rotation %+v", got, rot)
	}
	// --min-age 0 turns the rule off.
	out, _ = Rotate(repo, es, seen, 0, rotNow)
	if len(out) != 2 {
		t.Fatalf("min-age 0 still skipped: %v", ids(out))
	}
}

// Unchanged entry and unchanged code: left out whatever the age.
func TestRotateLeavesOutEntriesWhoseCodeAndTextAreUnchanged(t *testing.T) {
	repo := newRepo(t)
	e := entry(1, "`SessionDistillationExists` in internal/distill.go")
	other := entry(2, "`SessionDistillationExists` in internal/distill.go")
	old, _ := repo.Head()
	// A commit that touches neither the file nor the symbol.
	if err := os.WriteFile(filepath.Join(repo.Root, "README.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "readme")
	seen := map[int64]Seen{
		1: {CheckedAt: "2026-01-01T00:00:00Z", Commit: old, EntryHash: HashOf(e)},
		2: {CheckedAt: "2026-01-01T00:00:00Z", Commit: old, EntryHash: HashOf(other)},
	}
	out, rot := Rotate(repo, []Entry{e, other}, seen, 14*24*time.Hour, rotNow)
	if len(out) != 0 || rot.Unchanged != 2 {
		t.Fatalf("unchanged entries were not left out: %v %+v", ids(out), rot)
	}
	// Now the named file changes: the entry is no longer "unchanged". It is due
	// again once the last look is older than min-age; before that min-age holds
	// it back so a busy file does not cost a call every night.
	src := "package internal\n\nfunc SessionDistillationExists() bool { return true }\n"
	if err := os.WriteFile(filepath.Join(repo.Root, "internal", "distill.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "change distill")
	seen[1] = Seen{CheckedAt: "2026-10-15T00:00:00Z", Commit: old, EntryHash: HashOf(e)}
	out, rot = Rotate(repo, []Entry{e}, seen, 14*24*time.Hour, rotNow)
	if len(out) != 0 || rot.Unchanged != 0 || rot.Recent != 1 {
		t.Fatalf("a recent look must hold a changed entry back: %v %+v", ids(out), rot)
	}
	seen[1] = Seen{CheckedAt: "2026-09-01T00:00:00Z", Commit: old, EntryHash: HashOf(e)}
	out, rot = Rotate(repo, []Entry{e}, seen, 14*24*time.Hour, rotNow)
	if len(out) != 1 || rot.Unchanged != 0 {
		t.Fatalf("a changed file did not make the entry due: %v %+v", ids(out), rot)
	}
}

func TestRotateRechecksAnEditedEntryAndSymbolChangesInOtherFiles(t *testing.T) {
	repo := newRepo(t)
	e := entry(1, "`SessionDistillationExists`")
	old, _ := repo.Head()
	seen := map[int64]Seen{1: {CheckedAt: "2026-10-15T00:00:00Z", Commit: old, EntryHash: "other-wording"}}
	if out, _ := Rotate(repo, []Entry{e}, seen, 14*24*time.Hour, rotNow); len(out) != 1 {
		t.Fatal("an edited entry must be checked again even when looked at yesterday")
	}
	seen[1] = Seen{CheckedAt: "2026-09-01T00:00:00Z", Commit: old, EntryHash: HashOf(e)}
	if err := os.WriteFile(filepath.Join(repo.Root, "internal", "caller.go"), []byte("package internal\n\nvar _ = SessionDistillationExists()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "add caller")
	if out, rot := Rotate(repo, []Entry{e}, seen, 14*24*time.Hour, rotNow); len(out) != 1 || rot.Unchanged != 0 {
		t.Fatal("a change that mentions the entry's symbol must keep it from being counted as unchanged")
	}
}

func TestRotateSkipsEverythingWhenTheHeadIsTheSame(t *testing.T) {
	repo := newRepo(t)
	e := entry(1, "`GoneA`")
	head, _ := repo.Head()
	seen := map[int64]Seen{1: {CheckedAt: "2026-01-01T00:00:00Z", Commit: head, EntryHash: HashOf(e)}}
	out, rot := Rotate(repo, []Entry{e}, seen, 14*24*time.Hour, rotNow)
	if len(out) != 0 || rot.Unchanged != 1 {
		t.Fatalf("%v %+v", ids(out), rot)
	}
}

// PR #127: the answer cap leaves room for the model's own thinking.
func TestTheModelIsAskedForFourThousandNinetySixOutputTokens(t *testing.T) {
	if MaxOutputTokens != 4096 {
		t.Fatalf("MaxOutputTokens = %d, want 4096", MaxOutputTokens)
	}
	repo := newRepo(t)
	m := &capturingModel{text: `{"verdict":"holds","reason":"ok"}`}
	Check(context.Background(), repo, []Entry{entry(1, "`SessionDistillationExists`")}, Options{Model: m, Pricing: price, MaxUSD: 1})
	if m.maxTokens != 4096 {
		t.Fatalf("the call asked for max_tokens=%d, want 4096", m.maxTokens)
	}
}

type capturingModel struct {
	text      string
	maxTokens int
}

func (c *capturingModel) CompleteWithUsage(_ context.Context, _ string, _ []llm.Message, maxTokens int) (string, llm.Usage, error) {
	c.maxTokens = maxTokens
	return c.text, llm.Usage{InputTokens: 10, OutputTokens: 10}, nil
}

// Cache tokens are billed at their own prices.
func TestCacheTokensCountAtTheirOwnPrices(t *testing.T) {
	repo := newRepo(t)
	m := &fakeModel{text: `{"verdict":"holds","reason":"ok"}`, usage: llm.Usage{InputTokens: 100, OutputTokens: 100, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}}
	res := Check(context.Background(), repo, []Entry{entry(1, "`SessionDistillationExists`")}, Options{Model: m, Pricing: price, MaxUSD: 100})
	// in 100*$2 + out 100*$10 per M, read 1M * $0.20, write 1M * $2.50
	want := 100*2e-6 + 100*10e-6 + 0.20 + 2.50
	if got := res.Spent; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("spent %.6f, want %.6f", got, want)
	}
	v := res.Verdicts[0]
	if v.CacheRead != 1_000_000 || v.CacheWrite != 1_000_000 {
		t.Errorf("verdict lacks cache tokens: %+v", v)
	}
	// An explicit cache price wins over the derived one.
	p := Pricing{InUSDPerMTok: 2, OutUSDPerMTok: 10, CacheReadUSDPerMTok: 1, CacheWriteUSDPerMTok: 3}
	if got := p.CostUsage(llm.Usage{CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}); got < 3.999 || got > 4.001 {
		t.Errorf("explicit cache prices ignored: %.4f", got)
	}
}

// A limit that is reached ends the run cleanly and names itself.
func TestGateStopsTheRunAndNamesTheLimit(t *testing.T) {
	repo := newRepo(t)
	m := &fakeModel{text: `{"verdict":"holds","reason":"ok"}`, usage: llm.Usage{InputTokens: 10, OutputTokens: 10}}
	calls := 0
	gate := func() *llmusage.Reached {
		if calls >= 2 {
			return &llmusage.Reached{Period: "month", Figure: "usd", Limit: 5, Used: 5}
		}
		return nil
	}
	es := []Entry{entry(1, "`SessionDistillationExists`"), entry(2, "`SessionDistillationExists`"), entry(3, "`SessionDistillationExists`"), entry(4, "`SessionDistillationExists`")}
	res := Check(context.Background(), repo, es, Options{Model: m, Pricing: price, MaxUSD: 100, Gate: gate,
		OnCall: func(Verdict, llm.Usage, bool) { calls++ }})
	if res.StoppedBy != "limit" || !strings.Contains(res.LimitHit, "monthly limit reached") || m.calls != 2 {
		t.Fatalf("stopped by %q (%q) after %d calls", res.StoppedBy, res.LimitHit, m.calls)
	}
	if n := len(res.Verdicts); n != 4 || res.Verdicts[2].Kind != NotRun {
		t.Fatalf("the rest must be listed as not run: %+v", res.Verdicts)
	}
	var b strings.Builder
	WriteReport(&b, "p", res, rotNow)
	if !strings.Contains(b.String(), "monthly limit reached") || !strings.Contains(b.String(), "Der Gärtner hat 2 Einträge geprüft, 0 Vorschläge.") {
		t.Errorf("report: %s", b.String())
	}
}
