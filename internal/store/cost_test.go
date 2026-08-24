package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func billedSession(t *testing.T, s *Store, external, project, lastSeen string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: external,
		Scope: scope.Axes{Project: project}, StartedAt: lastSeen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, lastSeen, id); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Role: "assistant", Text: "some transcript text", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, lastSeen, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A recurring spend without a running total is the kind of cost line that gets
// noticed when the bill arrives. Each collect run reports its own batch; nothing
// added them up, and the journal that held them rotates away.
func TestDistillCostAggregatesAndSplits(t *testing.T) {
	s := openTest(t)
	first := billedSession(t, s, "a", "github.com/x/one", "2026-08-01T00:00:00Z")
	second := billedSession(t, s, "b", "github.com/x/two", "2026-08-01T00:00:00Z")

	batch, err := s.RecordDistillBatch("batch_1", "test-model", []DistillBatchItem{
		{CustomID: "s1", SessionID: first, Digest: "d1"},
		{CustomID: "s2", SessionID: second, Digest: "d2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// "one" has more input, "two" has enough more output to cost more overall:
	// output is six times the price, so ordering by input tokens would rank
	// them the wrong way round. Measured in production, where a project with
	// 891k input sorted above one with 770k input and a bigger bill.
	if err := s.RecordDistillBatchUsage(batch, "s1", 100_000, 1_000); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistillBatchUsage(batch, "s2", 40_000, 40_000); err != nil {
		t.Fatal(err)
	}

	total, err := s.DistillCost("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(total) != 1 || total[0].PromptTokens != 140_000 || total[0].CompletionTokens != 41_000 {
		t.Fatalf("total = %+v, want one row summing both items", total)
	}
	if total[0].Batches != 1 || total[0].Sessions != 2 {
		t.Fatalf("total = %+v, want 1 batch over 2 sessions", total[0])
	}

	byProject, err := s.DistillCost("project", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byProject) != 2 {
		t.Fatalf("by project = %+v, want one row per project", byProject)
	}
	// Sorted by spend, not by input volume: "two" has less input and a larger
	// bill, so it belongs first.
	if byProject[0].Group != "github.com/x/two" {
		t.Fatalf("by project = %+v, want the costlier project first", byProject)
	}

	byModel, err := s.DistillCost("model", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byModel) != 1 || byModel[0].Group != "test-model" {
		t.Fatalf("by model = %+v, want the model recorded on the batch", byModel)
	}
}

// An item that was submitted but never came back has no usage recorded, and it
// must not be counted as a free session: that would understate the average and
// with it every forecast built on it.
func TestDistillCostCountsOnlyBilledItems(t *testing.T) {
	s := openTest(t)
	billed := billedSession(t, s, "a", "p", "2026-08-01T00:00:00Z")
	lost := billedSession(t, s, "b", "p", "2026-08-01T00:00:00Z")
	batch, err := s.RecordDistillBatch("batch_1", "m", []DistillBatchItem{
		{CustomID: "s1", SessionID: billed, Digest: "d1"},
		{CustomID: "s2", SessionID: lost, Digest: "d2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistillBatchUsage(batch, "s1", 1000, 10); err != nil {
		t.Fatal(err)
	}

	total, err := s.DistillCost("", "")
	if err != nil {
		t.Fatal(err)
	}
	if total[0].Sessions != 1 {
		t.Fatalf("sessions = %d, want only the one that was billed", total[0].Sessions)
	}
}

// The forecast is the number an operator actually wants: not what has been
// spent, but what finishing the backlog will cost.
func TestPendingDistillationSizeReportsWhatIsLeft(t *testing.T) {
	s := openTest(t)
	billedSession(t, s, "a", "github.com/x/one", "2026-08-01T00:00:00Z")
	billedSession(t, s, "b", "github.com/x/two", "2026-08-01T00:00:00Z")
	// A session outside a repository is not going to be distilled, so it must
	// not appear in the forecast either.
	billedSession(t, s, "c", "", "2026-08-01T00:00:00Z")

	sessions, chars, err := s.PendingDistillationSize(scope.Axes{}, "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 2 {
		t.Fatalf("pending sessions = %d, want the two with a project", sessions)
	}
	if chars != 2*len("some transcript text") {
		t.Fatalf("pending chars = %d, want the transcript length of both", chars)
	}
}

// The forecast divides transcript characters by the tokens they actually cost.
// Using the pre-flight estimator instead would overstate what is left to pay,
// because that one guesses low on purpose so a prompt is never larger than
// planned.
func TestBilledTranscriptCharsCoversOnlyBilledSessions(t *testing.T) {
	s := openTest(t)
	billed := billedSession(t, s, "a", "p", "2026-08-01T00:00:00Z")
	lost := billedSession(t, s, "b", "p", "2026-08-01T00:00:00Z")
	batch, err := s.RecordDistillBatch("batch_1", "m", []DistillBatchItem{
		{CustomID: "s1", SessionID: billed, Digest: "d1"},
		{CustomID: "s2", SessionID: lost, Digest: "d2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistillBatchUsage(batch, "s1", 1000, 10); err != nil {
		t.Fatal(err)
	}
	chars, err := s.BilledTranscriptChars("")
	if err != nil {
		t.Fatal(err)
	}
	if chars != len("some transcript text") {
		t.Fatalf("billed chars = %d, want the one session that was billed", chars)
	}
}

// Releasing a session for reprocessing deletes its distillation row. If the
// version attribution lived only there, the spend of the released generation
// would vanish from the report — exactly when comparing two prompts is the
// reason the money was spent.
func TestCostByVersionSurvivesRelease(t *testing.T) {
	s := openTest(t)
	id := billedSession(t, s, "a", "p", "2026-08-01T00:00:00Z")
	batch, err := s.RecordDistillBatch("batch_v1", "m", []DistillBatchItem{
		{CustomID: "s1", SessionID: id, Digest: "d1", PromptVersion: "v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistillBatchUsage(batch, "s1", 5000, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionDistillation(id, "d1", "v1", scope.Axes{Project: "p"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseDistillations("v1", scope.Axes{Project: "p"}, false); err != nil {
		t.Fatal(err)
	}

	rows, err := s.DistillCost("version", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Group != "v1" || rows[0].PromptTokens != 5000 {
		t.Fatalf("by version after release = %+v, want the v1 spend still attributed", rows)
	}
}
