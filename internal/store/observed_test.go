package store

import (
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// distillOneFinding records a single finding against a session that started at
// startedAt, which is the whole point of these tests: the run happens now, the
// observation happened then.
func distillOneFinding(t *testing.T, s *Store, external, startedAt, typ, title, digest string) int64 {
	t.Helper()
	sessionID, err := s.UpsertSession(Session{
		Harness: "codex", ExternalID: external,
		Scope: scope.Axes{Project: "p"}, StartedAt: startedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(sessionID, []Chunk{{Seq: 1, Role: "assistant", Text: "the finding itself"}}); err != nil {
		t.Fatal(err)
	}
	item := SessionDistilledItem{Type: typ, Title: title, Body: "b", Quote: "the finding", ChunkSeq: 1}
	if _, err := s.ApplySessionDistillation(sessionID, digest, "v1", scope.Axes{Project: "p"}, []SessionDistilledItem{item}); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func observationOf(t *testing.T, s *Store, title string) (observed, created string) {
	t.Helper()
	if err := s.DB().QueryRow(`SELECT observed_at, created_at FROM knowledge WHERE title=?`, title).
		Scan(&observed, &created); err != nil {
		t.Fatal(err)
	}
	return observed, created
}

func TestDistilledEntryIsDatedByItsSessionNotByTheRun(t *testing.T) {
	s := openTest(t)
	distillOneFinding(t, s, "june", "2026-06-15T09:00:00Z", "decision", "old finding", "d1")

	observed, created := observationOf(t, s, "old finding")
	if observed != "2026-06-15T09:00:00Z" {
		t.Errorf("observed_at = %q, want the session start 2026-06-15T09:00:00Z", observed)
	}
	// Both times survive and mean different things: when it was seen, and when
	// we wrote it down. Collapsing them is the bug this guards.
	if created == "" {
		t.Error("created_at was lost")
	}
	if created == observed {
		t.Errorf("created_at %q equals observed_at, so the run time was not recorded separately", created)
	}
}

func TestHandWrittenEntryIsObservedWhenItIsWritten(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "by hand", Body: "b", Scope: scope.Axes{Project: "p"}}); err != nil {
		t.Fatal(err)
	}
	observed, created := observationOf(t, s, "by hand")
	if observed != created {
		t.Errorf("observed_at %q != created_at %q; without evidence the two are the same moment", observed, created)
	}
}

func TestCorroborationFromAnOlderSessionMovesTheObservationBack(t *testing.T) {
	s := openTest(t)
	distillOneFinding(t, s, "august", "2026-08-20T09:00:00Z", "pitfall", "same defect", "d1")
	// The distiller works the backlog in whatever order it gets to it, so the
	// second session to be processed may well be the earlier one.
	distillOneFinding(t, s, "june", "2026-06-15T09:00:00Z", "pitfall", "same defect", "d2")

	observed, _ := observationOf(t, s, "same defect")
	if observed != "2026-06-15T09:00:00Z" {
		t.Errorf("observed_at = %q, want the earliest evidence 2026-06-15T09:00:00Z", observed)
	}
}

func TestDeliveryRanksByObservationNotByInsertion(t *testing.T) {
	s := openTest(t)
	// Inserted in reverse: the recent observation is written down first, the
	// old one second. Ranking by created_at would put the June entry on top.
	distillOneFinding(t, s, "august", "2026-08-20T09:00:00Z", "decision", "recent", "d1")
	distillOneFinding(t, s, "june", "2026-06-15T09:00:00Z", "decision", "ancient", "d2")
	for _, title := range []string{"recent", "ancient"} {
		var id int64
		if err := s.DB().QueryRow(`SELECT id FROM knowledge WHERE title=?`, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateKnowledge(id, map[string]string{"confidence": "trusted"}); err != nil {
			t.Fatal(err)
		}
	}

	ks, err := s.KnowledgeForContext(scope.Axes{Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 2 {
		t.Fatalf("delivered %d entries, want 2", len(ks))
	}
	if ks[0].Title != "recent" {
		t.Errorf("first entry = %q, want %q: the tiebreaker must be when it was observed", ks[0].Title, "recent")
	}
	if ks[0].ObservedAt != "2026-08-20T09:00:00Z" {
		t.Errorf("ObservedAt = %q, want it carried out to the reader", ks[0].ObservedAt)
	}
}

func TestBackfillDerivesObservationFromEarliestEvidence(t *testing.T) {
	s := openTest(t)
	distillOneFinding(t, s, "june", "2026-06-15T09:00:00Z", "decision", "from a session", "d1")
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "from a hand", Body: "b", Scope: scope.Axes{Project: "p"}}); err != nil {
		t.Fatal(err)
	}
	// The state of an existing database: the column is there, nothing filled it.
	if _, err := s.DB().Exec(`UPDATE knowledge SET observed_at=''`); err != nil {
		t.Fatal(err)
	}

	n, err := s.BackfillObservedAt()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("backfilled %d entries, want 2", n)
	}
	observed, created := observationOf(t, s, "from a session")
	if observed != "2026-06-15T09:00:00Z" {
		t.Errorf("observed_at = %q, want 2026-06-15T09:00:00Z", observed)
	}
	if created == observed {
		t.Error("the backfill overwrote created_at; it is a separate fact")
	}
	observed, created = observationOf(t, s, "from a hand")
	if observed != created {
		t.Errorf("without evidence the fallback is created_at, got %q vs %q", observed, created)
	}

	again, err := s.BackfillObservedAt()
	if err != nil || again != 0 {
		t.Errorf("second run touched %d entries (err=%v), want an idempotent no-op", again, err)
	}
}

func TestPlansAgeFromWhenTheyWereObserved(t *testing.T) {
	s := openTest(t)
	distillOneFinding(t, s, "january", "2026-01-10T09:00:00Z", "plan", "old plan", "d1")
	distillOneFinding(t, s, "august", "2026-08-20T09:00:00Z", "plan", "fresh plan", "d2")

	at := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := s.ApplyStaleness(at, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("marked %d plans stale, want exactly the January one", n)
	}
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM knowledge WHERE title='old plan'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "stale" {
		t.Errorf("January plan status = %q, want stale", status)
	}
}

func TestReconfirmingAnEntryMovesItsObservationForward(t *testing.T) {
	s := openTest(t)
	distillOneFinding(t, s, "june", "2026-06-15T09:00:00Z", "plan", "revisited plan", "d1")
	var id int64
	if err := s.DB().QueryRow(`SELECT id FROM knowledge WHERE title='revisited plan'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Editing the text is not the same as vouching for it still being true, so
	// staying fresh has to be said out loud.
	if err := s.UpdateKnowledge(id, map[string]string{"observed_at": "2026-08-23T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ApplyStaleness(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("marked %d plans stale, want 0: the entry was reconfirmed yesterday", n)
	}
}
