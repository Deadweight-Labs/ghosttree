package sessiondistill

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func chunk(seq int, size int) store.Chunk {
	return store.Chunk{Seq: seq, Role: "user", Text: strings.Repeat("a", size)}
}

// OpenAI bills a prompt above the short-context threshold at double the rate,
// so the budget exists to stay under a price cliff, not just under the context
// window.
func TestSelectWithinBudgetKeepsSmallSessionsWhole(t *testing.T) {
	chunks := []store.Chunk{chunk(0, 100), chunk(1, 100), chunk(2, 100)}
	got, dropped := SelectWithinBudget(chunks, DefaultBudget)
	if len(got) != 3 || dropped != 0 {
		t.Errorf("kept %d dropped %d, want all three kept", len(got), dropped)
	}
}

// Truncation must be reported. A distiller that silently drops half a
// transcript looks exactly like one that found nothing worth keeping.
func TestSelectWithinBudgetReportsWhatItDropped(t *testing.T) {
	budget := Budget{MaxChars: 250}
	chunks := []store.Chunk{chunk(0, 100), chunk(1, 100), chunk(2, 100), chunk(3, 100)}
	got, dropped := SelectWithinBudget(chunks, budget)
	if dropped == 0 {
		t.Fatal("oversized transcript reported no drops")
	}
	if len(got)+dropped != len(chunks) {
		t.Errorf("kept %d + dropped %d != %d input chunks", len(got), dropped, len(chunks))
	}
	total := 0
	for _, c := range got {
		total += len(c.Text)
	}
	if total > budget.MaxChars {
		t.Errorf("kept %d chars, over the budget of %d", total, budget.MaxChars)
	}
}

// The beginning of a session states the task and the end states the outcome;
// the middle is mostly tool traffic. Truncating from the middle keeps both
// ends, which is where durable knowledge sits.
func TestSelectWithinBudgetKeepsBothEnds(t *testing.T) {
	chunks := []store.Chunk{chunk(0, 100), chunk(1, 100), chunk(2, 100), chunk(3, 100), chunk(4, 100)}
	got, _ := SelectWithinBudget(chunks, Budget{MaxChars: 300})
	if len(got) == 0 {
		t.Fatal("nothing kept")
	}
	if got[0].Seq != 0 {
		t.Errorf("first kept chunk = %d, want the opening chunk", got[0].Seq)
	}
	if got[len(got)-1].Seq != 4 {
		t.Errorf("last kept chunk = %d, want the closing chunk", got[len(got)-1].Seq)
	}
}

// Chunk order carries the sequence numbers the model must quote against, so
// selection must never reorder them.
func TestSelectWithinBudgetPreservesOrder(t *testing.T) {
	chunks := []store.Chunk{chunk(0, 100), chunk(1, 100), chunk(2, 100), chunk(3, 100), chunk(4, 100)}
	got, _ := SelectWithinBudget(chunks, Budget{MaxChars: 300})
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("chunk order broken at %d: %d after %d", i, got[i].Seq, got[i-1].Seq)
		}
	}
}

// A single chunk larger than the whole budget must not produce an empty
// prompt: the session would be marked processed with nothing extracted.
func TestSelectWithinBudgetAlwaysKeepsSomething(t *testing.T) {
	got, dropped := SelectWithinBudget([]store.Chunk{chunk(0, 10_000)}, Budget{MaxChars: 100})
	if len(got) != 1 {
		t.Fatalf("kept %d chunks, want the oversized one truncated rather than dropped", len(got))
	}
	if len(got[0].Text) > 100 {
		t.Errorf("oversized chunk kept %d chars, want it cut to the budget", len(got[0].Text))
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0: the chunk was truncated, not dropped", dropped)
	}
}

// The default has to sit under the 272k-token short-context threshold with
// room to spare, because characters are only an estimate of tokens.
func TestDefaultBudgetStaysUnderShortContextThreshold(t *testing.T) {
	if got := DefaultBudget.EstimatedTokens(); got >= 272_000 {
		t.Errorf("estimated tokens = %d, want well under the 272k short-context threshold", got)
	}
}
