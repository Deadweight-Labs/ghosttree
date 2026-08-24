package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func distillableSession(t *testing.T, s *Store, external, text string) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: external,
		Scope: scope.Axes{Project: "p"}, StartedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Role: "assistant", Text: text, Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	return id
}

// The prompt is the one control this system has over quality, and the only way
// to tell a better prompt from a worse one is to run both over the same
// sessions. Keyed on the transcript alone, a session was retired against
// whichever prompt happened to touch it first — the archive froze at the worst
// prompt it ever saw.
func TestDistillationIsRecordedPerPromptVersion(t *testing.T) {
	s := openTest(t)
	id := distillableSession(t, s, "a", "SQLite was chosen because one writer is sufficient.")
	items := []SessionDistilledItem{{Type: "decision", Title: "Use SQLite", Body: "b", ChunkSeq: 1, Quote: "SQLite was chosen"}}

	if _, err := s.ApplySessionDistillation(id, "digest", "v1", scope.Axes{Project: "p"}, items); err != nil {
		t.Fatal(err)
	}
	// Same transcript, same prompt: still a no-op.
	n, err := s.ApplySessionDistillation(id, "digest", "v1", scope.Axes{Project: "p"}, items)
	if err != nil || n != 0 {
		t.Fatalf("rerun with the same prompt inserted %d items (err %v), want a no-op", n, err)
	}
	exists, err := s.SessionDistillationExists(id, "digest", "v1")
	if err != nil || !exists {
		t.Fatalf("v1 distillation not recorded: %v", err)
	}
	// A different prompt version is different work on the same transcript.
	if exists, _ := s.SessionDistillationExists(id, "digest", "v2"); exists {
		t.Fatal("a v2 distillation was reported before one ran")
	}
}

// Reprocessing must not leave two generations of the same finding side by side.
// The earlier items were never reviewed — they are quarantined — so retiring
// them costs nothing; an item somebody already approved is left alone, because
// a prompt change is no reason to discard a human decision.
func TestReprocessingArchivesTheQuarantinedItemsOfTheEarlierRun(t *testing.T) {
	s := openTest(t)
	id := distillableSession(t, s, "a", "SQLite was chosen because one writer is sufficient.")
	ax := scope.Axes{Project: "p"}
	if _, err := s.ApplySessionDistillation(id, "digest", "v1", ax, []SessionDistilledItem{
		{Type: "decision", Title: "Old and weak", Body: "b", ChunkSeq: 1, Quote: "SQLite was chosen"},
		{Type: "note", Title: "Old but approved", Body: "b", ChunkSeq: 1, Quote: "one writer"},
	}); err != nil {
		t.Fatal(err)
	}
	var approved int64
	if err := s.db.QueryRow(`SELECT id FROM knowledge WHERE title='Old but approved'`).Scan(&approved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE knowledge SET confidence='trusted' WHERE id=?`, approved); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplySessionDistillation(id, "digest", "v2", ax, []SessionDistilledItem{
		{Type: "decision", Title: "New and better", Body: "b", ChunkSeq: 1, Quote: "one writer is sufficient"},
	}); err != nil {
		t.Fatal(err)
	}

	var weakStatus, approvedStatus string
	if err := s.db.QueryRow(`SELECT status FROM knowledge WHERE title='Old and weak'`).Scan(&weakStatus); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT status FROM knowledge WHERE id=?`, approved).Scan(&approvedStatus); err != nil {
		t.Fatal(err)
	}
	if weakStatus != "archived" {
		t.Errorf("unreviewed item from the earlier run is %q, want archived", weakStatus)
	}
	if approvedStatus != "active" {
		t.Errorf("approved item from the earlier run is %q, want it left alone", approvedStatus)
	}
}

// Releasing has to be a deliberate act with a visible price. Bumping the prompt
// version must not silently put 1819 sessions back in the queue.
func TestReleaseDistillationsIsScopedAndCounted(t *testing.T) {
	s := openTest(t)
	mine := distillableSession(t, s, "mine", "text one")
	other, err := s.UpsertSession(Session{Harness: "codex", ExternalID: "other",
		Scope: scope.Axes{Project: "q"}, StartedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(other, []Chunk{{Seq: 1, Role: "assistant", Text: "text two", Raw: "{}"}}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		id int64
		ax scope.Axes
	}{{mine, scope.Axes{Project: "p"}}, {other, scope.Axes{Project: "q"}}} {
		if _, err := s.ApplySessionDistillation(pair.id, "digest", "v1", pair.ax, nil); err != nil {
			t.Fatal(err)
		}
	}

	released, err := s.ReleaseDistillations("v1", scope.Axes{Project: "p"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("dry run reported %d sessions, want the one in project p", released)
	}
	if exists, _ := s.SessionDistillationExists(mine, "digest", "v1"); !exists {
		t.Fatal("dry run deleted the row it was only meant to count")
	}

	released, err = s.ReleaseDistillations("v1", scope.Axes{Project: "p"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("release reported %d sessions, want 1", released)
	}
	if exists, _ := s.SessionDistillationExists(mine, "digest", "v1"); exists {
		t.Fatal("released session still carries its v1 distillation")
	}
	if exists, _ := s.SessionDistillationExists(other, "digest", "v1"); !exists {
		t.Fatal("release reached outside the project it was scoped to")
	}
	pending, err := s.SessionsPendingDistillation(scope.Axes{Project: "p"}, "2030-01-01T00:00:00Z", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != mine {
		t.Fatalf("pending = %+v, want the released session back in the queue", pending)
	}
}

// Releasing deletes the session's distillation row, so anything that looked for
// an earlier run in that table would find none in exactly the case reprocessing
// exists for — and the old items would sit beside the new ones forever.
func TestReleasedSessionStillArchivesItsEarlierItems(t *testing.T) {
	s := openTest(t)
	id := distillableSession(t, s, "a", "SQLite was chosen because one writer is sufficient.")
	ax := scope.Axes{Project: "p"}
	if _, err := s.ApplySessionDistillation(id, "digest", "v1", ax, []SessionDistilledItem{
		{Type: "decision", Title: "Weak v1 finding", Body: "b", ChunkSeq: 1, Quote: "SQLite was chosen"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseDistillations("v1", ax, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionDistillation(id, "digest", "v2", ax, []SessionDistilledItem{
		{Type: "decision", Title: "Sharper v2 finding", Body: "b", ChunkSeq: 1, Quote: "one writer is sufficient"},
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM knowledge WHERE title='Weak v1 finding'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Fatalf("item of the released run is %q, want archived", status)
	}
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE status='active' AND origin='distilled'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("%d active distilled items after reprocessing, want only the new one", active)
	}
}
