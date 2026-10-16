package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func batchItems(ids ...int64) []explain.BatchItem {
	var out []explain.BatchItem
	for _, id := range ids {
		out = append(out, explain.BatchItem{CustomID: "knowledge-" + itoa(id), Kind: explain.KindKnowledge, ObjectID: id, Hash: "h" + itoa(id)})
	}
	return out
}

func TestExplainBatchRecordsSurviveAReopenAndBlockResubmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordExplainBatch("msgbatch_1", "claude-haiku-5-5", "de", batchItems(1, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordExplainBatch("msgbatch_1", "m", "de", batchItems(3)); err == nil {
		t.Error("the same provider batch was recorded twice")
	}
	if _, err := st.RecordExplainBatch("", "m", "de", batchItems(3)); err == nil {
		t.Error("a batch without an id was recorded")
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	open, err := st.OpenExplainBatches()
	if err != nil || len(open) != 1 || open[0].ProviderID != "msgbatch_1" || open[0].Pending != 2 || open[0].Language != "de" || open[0].Model != "claude-haiku-5-5" {
		t.Fatalf("%+v %v", open, err)
	}
	blocked, _ := st.ExplainBatchBlocked("2000-01-01T00:00:00Z")
	if !blocked[explain.BatchKey(explain.KindKnowledge, 1, "h1")] || blocked[explain.BatchKey(explain.KindKnowledge, 1, "other")] || blocked[explain.BatchKey(explain.KindKnowledge, 3, "h3")] {
		t.Errorf("blocked %v", blocked)
	}
	if b, _ := st.ExplainBatched(explain.KindKnowledge, 2, "h2", "2000-01-01T00:00:00Z"); !b {
		t.Error("an object in an open batch is not reported as batched")
	}
}

func TestFinishingAnItemBooksItsUsageOnceAndFailuresBlockForAWhile(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.RecordExplainBatch("msgbatch_1", "m", "de", batchItems(1, 2))
	use := llmusage.Usage{Calls: 1, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CostMicroUSD: 321}
	if err := st.FinishExplainBatchItem(id, "knowledge-1", "saved", "", "2026-10-16", use); err != nil {
		t.Fatal(err)
	}
	// A replay (a run that died after booking) must not count again.
	if err := st.FinishExplainBatchItem(id, "knowledge-1", "saved", "", "2026-10-16", use); err != nil {
		t.Fatal(err)
	}
	fail := llmusage.Usage{Calls: 1, Failures: 1}
	if err := st.FinishExplainBatchItem(id, "knowledge-2", "failed", "boom", "2026-10-16", fail); err != nil {
		t.Fatal(err)
	}
	u, _ := st.LLMUsageOn(llmusage.JobExplain, "2026-10-16")
	if u.Calls != 2 || u.Failures != 1 || u.InputTokens != 100 || u.CacheReadTokens != 5 || u.CostMicroUSD != 321 {
		t.Errorf("usage %+v", u)
	}
	if err := st.FinishExplainBatchItem(id, "knowledge-1", "bogus", "", "2026-10-16", use); err == nil {
		t.Error("an unknown state was accepted")
	}
	if p, _ := st.PendingExplainBatchItems(id); len(p) != 0 {
		t.Errorf("pending %v", p)
	}
	if err := st.CloseExplainBatch(id, "collected"); err != nil {
		t.Fatal(err)
	}
	if open, _ := st.OpenExplainBatches(); len(open) != 0 {
		t.Errorf("a collected batch is still open: %+v", open)
	}
	if err := st.CloseExplainBatch(id, "open"); err == nil {
		t.Error("a batch was closed into the state open")
	}
	// Saved items are free to be sent again when they change; failed ones wait.
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if b, _ := st.ExplainBatched(explain.KindKnowledge, 2, "h2", recent); !b {
		t.Error("a failed item does not block")
	}
	if b, _ := st.ExplainBatched(explain.KindKnowledge, 1, "h1", recent); b {
		t.Error("a saved item blocks")
	}
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if b, _ := st.ExplainBatched(explain.KindKnowledge, 2, "h2", future); b {
		t.Error("a failure blocks for ever")
	}
}

func TestExplainCreatedAtReadsEveryKind(t *testing.T) {
	w := newPainWorld(t)
	kid, _ := w.st.InsertKnowledge(Knowledge{Type: "note", Title: "T", Body: "B", Scope: scope.Axes{Project: roleProject}})
	rid := w.request(t, "offen")
	for kind, id := range map[string]int64{explain.KindKnowledge: kid, explain.KindRequest: rid} {
		at, err := w.st.ExplainCreatedAt(kind, id)
		if err != nil || time.Since(at) > time.Minute || time.Since(at) < -time.Minute {
			t.Errorf("%s: %v %v", kind, at, err)
		}
	}
	if _, err := w.st.ExplainCreatedAt(explain.KindKnowledge, 99999); err == nil {
		t.Error("a missing object has a creation time")
	}
	if _, err := w.st.ExplainCreatedAt("nonsense", 1); err == nil {
		t.Error("an unknown kind has a creation time")
	}
}
