package store

import (
	"strconv"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func idleSession(t *testing.T, s *Store, external, project, lastSeen string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: external,
		Scope: scope.Axes{Project: project}, StartedAt: lastSeen})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, lastSeen, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A session whose distillation is in flight must not be submitted again. The
// batch window is up to 24 hours and the timer runs hourly, so without this the
// same transcript is submitted — and paid for — a dozen times over before its
// first result ever lands.
func TestPendingDistillationExcludesSessionsInAnOpenBatch(t *testing.T) {
	s := openTest(t)
	id := idleSession(t, s, "a", "p", "2026-01-01T00:00:00Z")

	batchID, err := s.RecordDistillBatch("batch_1", "m", []DistillBatchItem{{CustomID: "s1", SessionID: id, Digest: "d", PromptVersion: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pending = %d, want 0: the session is already in flight", len(got))
	}

	// Closing the batch releases the session again. An item the model failed on
	// leaves no distillation row behind, and a permanently withheld session
	// would be lost silently rather than retried.
	if err := s.CloseDistillBatch(batchID, "collected"); err != nil {
		t.Fatal(err)
	}
	got, err = s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pending = %d, want 1: a collected batch must not withhold a session forever", len(got))
	}
}

// The first production run is deliberately confined to one project, so the
// selection has to be filterable rather than "whatever is oldest".
func TestPendingDistillationFiltersByProject(t *testing.T) {
	s := openTest(t)
	idleSession(t, s, "a", "github.com/x/example-project", "2026-01-01T00:00:00Z")
	idleSession(t, s, "b", "github.com/x/other", "2026-01-02T00:00:00Z")

	got, err := s.SessionsPendingDistillation(scope.Axes{Project: "github.com/x/example-project"}, "2026-06-01T00:00:00Z", "v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope.Project != "github.com/x/example-project" {
		t.Fatalf("pending = %+v, want only the example-project session", got)
	}
}

func TestDistillBatchRoundtripRecordsUsage(t *testing.T) {
	s := openTest(t)
	first := idleSession(t, s, "a", "p", "2026-01-01T00:00:00Z")
	second := idleSession(t, s, "b", "p", "2026-01-02T00:00:00Z")

	batchID, err := s.RecordDistillBatch("batch_1", "m", []DistillBatchItem{
		{CustomID: "s1", SessionID: first, Digest: "d1"},
		{CustomID: "s2", SessionID: second, Digest: "d2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenDistillBatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ProviderID != "batch_1" || open[0].ID != batchID {
		t.Fatalf("open batches = %+v, want the one just recorded", open)
	}
	items, err := s.DistillBatchItems(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SessionID != first || items[0].Digest != "d1" {
		t.Fatalf("items = %+v, want both sessions with their submission digests", items)
	}

	// The provider's own count is the only exact figure; it is what the cost
	// report is built from, so it has to survive the collect step.
	if err := s.RecordDistillBatchUsage(batchID, "s1", 120_000, 400); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDistillBatchUsage(batchID, "s2", 80_000, 200); err != nil {
		t.Fatal(err)
	}
	prompt, completion, err := s.DistillBatchUsage(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != 200_000 || completion != 600 {
		t.Fatalf("usage = %d/%d, want 200000/600", prompt, completion)
	}

	if err := s.CloseDistillBatch(batchID, "collected"); err != nil {
		t.Fatal(err)
	}
	open, err = s.OpenDistillBatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open batches after collect = %d, want 0", len(open))
	}
}

// Filtering after the LIMIT shrinks the window unpredictably — the same shape of
// bug that once pinned the distiller to the newest hundred sessions. Measured in
// production: the oldest sessions are mostly project-less, so a run asking for
// 100 skipped 98 of them in Go and submitted 2.
func TestPendingDistillationFillsTheLimitWithEligibleSessions(t *testing.T) {
	s := openTest(t)
	for i := range 5 {
		idleSession(t, s, "homeless"+strconv.Itoa(i), "", "2026-01-0"+strconv.Itoa(i+1)+"T00:00:00Z")
	}
	for i := range 3 {
		idleSession(t, s, "housed"+strconv.Itoa(i), "github.com/x/y", "2026-02-0"+strconv.Itoa(i+1)+"T00:00:00Z")
	}
	got, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "v1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("pending = %d, want the limit filled with sessions that can actually be distilled", len(got))
	}
	for _, session := range got {
		if session.Scope.Project == "" {
			t.Fatalf("selection returned a project-less session: %+v", session)
		}
	}
}

// Two modes read the same transcripts for different things. Keyed on the
// session alone, whichever ran first would take the whole archive off the
// other's queue: the first requests run found 1 eligible session in a project
// with 89, because the knowledge run had already been there.
func TestPendingDistillationIsPerPromptVersion(t *testing.T) {
	s := openTest(t)
	id := idleSession(t, s, "a", "p", "2026-01-01T00:00:00Z")
	if _, err := s.ApplySessionDistillation(id, "d", "v5", scope.Axes{Project: "p"}, nil); err != nil {
		t.Fatal(err)
	}
	done, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "v5", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("pending under its own version = %d, want 0", len(done))
	}
	other, err := s.SessionsPendingDistillation(scope.Axes{}, "2026-06-01T00:00:00Z", "req-v1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Fatalf("pending under the other mode = %d, want 1", len(other))
	}
}
