package sessiondistill

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeBatch struct {
	submitted []llm.BatchRequest
	status    llm.BatchStatusReport
	results   map[string]llm.BatchResult
	submits   int
}

func (f *fakeBatch) SubmitBatch(_ context.Context, reqs []llm.BatchRequest) (string, error) {
	f.submitted = append(f.submitted, reqs...)
	f.submits++
	return "batch_fake", nil
}

func (f *fakeBatch) BatchStatus(context.Context, string) (llm.BatchStatusReport, error) {
	return f.status, nil
}

func (f *fakeBatch) CollectBatch(context.Context, string) (map[string]llm.BatchResult, error) {
	return f.results, nil
}

func seedSession(t *testing.T, s *store.Store, external, project, text string) int64 {
	t.Helper()
	id, err := s.UpsertSession(store.Session{Harness: "codex", ExternalID: external,
		Scope: scope.Axes{Project: project}, StartedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []store.Chunk{{Seq: 7, Role: "assistant", Text: text, Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The whole point of the two-phase path is that submit and collect run in
// different processes, hours apart. A submit that does not leave a durable
// record behind would resubmit everything on the next tick.
func TestSubmitThenCollectAppliesResultsExactlyOnce(t *testing.T) {
	st := openStore(t)
	id := seedSession(t, st, "a", "p", "SQLite was chosen because one writer is sufficient.")

	client := &fakeBatch{}
	report, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != 1 || report.ProviderID != "batch_fake" {
		t.Fatalf("submit report = %+v, want one session in batch_fake", report)
	}
	if len(client.submitted) != 1 || !strings.Contains(client.submitted[0].User, "SQLite was chosen") {
		t.Fatalf("submitted request = %+v, want the transcript", client.submitted)
	}

	// A second submit before the result lands must find nothing to do.
	second, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sessions != 0 || client.submits != 1 {
		t.Fatalf("resubmitted an in-flight session: %+v after %d submits", second, client.submits)
	}

	client.status = llm.BatchStatusReport{Done: true, Total: 1, Completed: 1}
	client.results = map[string]llm.BatchResult{
		client.submitted[0].CustomID: {
			Content:      `{"items":[{"type":"decision","title":"Use SQLite","body":"One writer is enough.","chunk_seq":7,"quote":"SQLite was chosen"}]}`,
			PromptTokens: 4321, CompletionTokens: 120,
		},
	}
	collected, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Sessions != 1 || collected.Items != 1 {
		t.Fatalf("collect report = %+v, want one session and one item", collected)
	}
	if collected.PromptTokens != 4321 || collected.CompletionTokens != 120 {
		t.Fatalf("usage = %d/%d, want the provider's own count 4321/120",
			collected.PromptTokens, collected.CompletionTokens)
	}
	if exists, _ := st.SessionDistillationExists(id, Digest(mustChunks(t, st, id))); !exists {
		t.Fatal("collected session was not marked as distilled")
	}

	// Collecting again must be a no-op, not a second insert.
	again, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if again.Batches != 0 {
		t.Fatalf("collect found %d batches after closing them", again.Batches)
	}
}

// A batch still running must leave its sessions in flight rather than closed:
// closing early would drop the paid-for result on the floor.
func TestCollectLeavesRunningBatchOpen(t *testing.T) {
	st := openStore(t)
	seedSession(t, st, "a", "p", "some transcript")
	client := &fakeBatch{}
	if _, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget}); err != nil {
		t.Fatal(err)
	}
	client.status = llm.BatchStatusReport{Total: 1, Completed: 0}
	report, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pending != 1 || report.Sessions != 0 {
		t.Fatalf("collect report = %+v, want the batch reported as still pending", report)
	}
	open, _ := st.OpenDistillBatches()
	if len(open) != 1 {
		t.Fatalf("open batches = %d, want the running batch kept", len(open))
	}
}

// One unusable answer must not cost its siblings their result, and it must not
// mark its own session as done — otherwise a transient model failure silently
// removes that session from the backlog forever.
func TestCollectIsolatesFailedItems(t *testing.T) {
	st := openStore(t)
	good := seedSession(t, st, "good", "p", "SQLite was chosen because one writer is sufficient.")
	bad := seedSession(t, st, "bad", "p", "unrelated transcript")

	client := &fakeBatch{}
	if _, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget}); err != nil {
		t.Fatal(err)
	}
	byID := map[int64]string{}
	for _, r := range client.submitted {
		if strings.Contains(r.User, "SQLite was chosen") {
			byID[good] = r.CustomID
		} else {
			byID[bad] = r.CustomID
		}
	}
	client.status = llm.BatchStatusReport{Done: true, Total: 2, Completed: 1, FailedN: 1}
	client.results = map[string]llm.BatchResult{
		byID[good]: {Content: `{"items":[{"type":"decision","title":"Use SQLite","body":"One writer is enough.","chunk_seq":7,"quote":"SQLite was chosen"}]}`},
		byID[bad]:  {Error: "model overloaded"},
	}
	report, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != 1 || report.Failed != 1 {
		t.Fatalf("collect report = %+v, want one applied and one failed", report)
	}
	pending, err := st.SessionsPendingDistillation(scope.Axes{}, "2030-01-01T00:00:00Z", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != bad {
		t.Fatalf("pending = %+v, want the failed session back in the backlog", pending)
	}
}

// --dry-run has to answer "what would this cost" without spending anything.
func TestSubmitDryRunSendsNothing(t *testing.T) {
	st := openStore(t)
	seedSession(t, st, "a", "p", "some transcript")
	client := &fakeBatch{}
	report, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if client.submits != 0 || report.ProviderID != "" {
		t.Fatalf("dry run submitted anyway: %d submits, provider %q", client.submits, report.ProviderID)
	}
	if report.Sessions != 1 || report.EstimatedTokens == 0 {
		t.Fatalf("dry run report = %+v, want a cost estimate for one session", report)
	}
	open, _ := st.OpenDistillBatches()
	if len(open) != 0 {
		t.Fatalf("dry run recorded %d batches", len(open))
	}
}

func mustChunks(t *testing.T, st *store.Store, id int64) []store.Chunk {
	t.Helper()
	chunks, err := st.ReadSession(id, 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

// A reply cut off at the output cap is a fragment, and a fragment of JSON does
// not decode. Recording it as "this session had nothing to say" would retire
// the transcript permanently on the strength of a configuration limit — and it
// is the transcripts with the most to say that hit the cap.
func TestCollectKeepsTruncatedSessionsInTheBacklog(t *testing.T) {
	st := openStore(t)
	id := seedSession(t, st, "long", "p", "SQLite was chosen because one writer is sufficient.")
	client := &fakeBatch{}
	if _, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget}); err != nil {
		t.Fatal(err)
	}
	client.status = llm.BatchStatusReport{Done: true, Total: 1, Completed: 1}
	client.results = map[string]llm.BatchResult{
		client.submitted[0].CustomID: {Content: `{"items":[{"type":"decision","tit`, Truncated: true},
	}
	report, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if report.Truncated != 1 || report.Sessions != 0 {
		t.Fatalf("collect report = %+v, want the truncation counted and no session applied", report)
	}
	if len(report.Failures) != 1 || !strings.Contains(report.Failures[0], "truncated") {
		t.Fatalf("failures = %v, want a reason naming the truncation", report.Failures)
	}
	pending, err := st.SessionsPendingDistillation(scope.Axes{}, "2030-01-01T00:00:00Z", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending = %+v, want the truncated session retryable", pending)
	}
}

// Ten of fifty sessions failed on the first production run and the report said
// only "10 failed". A count without a cause cannot be acted on.
func TestCollectNamesTheReasonAnItemFailed(t *testing.T) {
	st := openStore(t)
	seedSession(t, st, "bad", "p", "some transcript")
	client := &fakeBatch{}
	if _, err := SubmitBatch(context.Background(), st, client, SubmitOptions{
		IdleBefore: "2030-01-01T00:00:00Z", Limit: 10, Budget: DefaultBudget}); err != nil {
		t.Fatal(err)
	}
	client.status = llm.BatchStatusReport{Done: true, Total: 1, Completed: 1}
	client.results = map[string]llm.BatchResult{
		client.submitted[0].CustomID: {Content: `{"items":[{"type":"note","title":"x","body":"y","chunk_seq":7,"quote":"never said this"}]}`},
	}
	report, err := CollectBatches(context.Background(), st, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 1 || !strings.Contains(report.Failures[0], "quote their chunk") {
		t.Fatalf("failures = %v, want the grounding failure named", report.Failures)
	}
}
