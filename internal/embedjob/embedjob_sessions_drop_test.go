package embedjob

import (
	"context"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func (f fixture) sessionVectors(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM session_embeddings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A vector that has no text any more (the answer that the session outgrew) is
// found by the look at the session, which only reads, and dropped by the job, in a
// write of its own; the session's other vectors stay, and the new answer is made
// when it has stood long enough.
func TestJobDropsTheVectorOfAnAnswerTheSessionOutgrew(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", "the prompt that starts the turn, about the cache layer")
	id, err := f.st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(seq int, text string) {
		t.Helper()
		line := `{"type":"assistant","timestamp":"2026-10-01T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
		if err := f.st.AppendChunks(id, []store.Chunk{{Seq: seq, Role: "assistant", Text: text, Raw: line}}); err != nil {
			t.Fatal(err)
		}
	}
	answer(1, "the first answer, after which the session goes on")
	clk := newStepClock()
	clk.add(2 * store.SessionSettle)
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, clk)
	drain(t, j)
	if f.sessionVectors(t) != 2 {
		t.Fatalf("%d vectors", f.sessionVectors(t))
	}

	answer(2, "the answer that really ends the turn")
	// Right after the chunk it has not stood long enough: the old answer goes, the new
	// one waits.
	clk.t = time.Now().UTC().Truncate(time.Second)
	if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
		t.Fatalf("%+v %v", res, err)
	}
	if f.sessionVectors(t) != 1 {
		t.Fatalf("the vector of the outgrown answer is still there: %d vectors", f.sessionVectors(t))
	}
	clk.add(2 * store.SessionSettle)
	if res, err := j.Step(context.Background()); err != nil || res.Embedded != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if f.sessionVectors(t) != 2 {
		t.Fatalf("%d vectors", f.sessionVectors(t))
	}
}

// What a database without foreign keys left behind goes in the idle purge, at any
// hour: it is a few statements on keys and no look at the sessions, so it has no
// window.
func TestJobPurgesWhatIsGoneOfTheSessionsAtAnyHour(t *testing.T) {
	f := newFixture(t)
	f.addSession(t, "s", numbered("some", 3)...)
	clk := newStepClock()
	always := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, clk)
	drain(t, always)
	if f.sessionVectors(t) != 3 {
		t.Fatalf("%d vectors", f.sessionVectors(t))
	}
	db := f.st.DB()
	for _, q := range []string{`PRAGMA foreign_keys = OFF`, `DELETE FROM session_chunks WHERE seq = 2`, `PRAGMA foreign_keys = ON`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if f.sessionVectors(t) != 3 {
		t.Fatalf("without foreign keys the vector stays: %d", f.sessionVectors(t))
	}
	// A job whose window is closed at this hour.
	closed := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Start: 0, End: 1}}, &stepClock{t: time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)})
	if _, err := closed.Step(context.Background()); err != nil || f.sessionVectors(t) != 2 {
		t.Fatalf("%d vectors after the purge of a job with a closed window: %v", f.sessionVectors(t), err)
	}
}

// A text that the server refuses for good is parked, and its session is not kept
// waiting for it: the other texts are embedded, the session is marked, and no step
// reads it again.
func TestJobDoesNotLookAgainAtASessionWithATextTheServerRefuses(t *testing.T) {
	f := newFixture(t)
	f.srv.Reject.Store(80)
	long := "this prompt is much longer than the eighty characters that the fake server accepts, so it refuses it for good"
	f.addSession(t, "s", "a short prompt of a session, about s1 s2", long, "another short prompt of it, about s3 s4 too")
	j := f.job(Config{Batch: 8, Kinds: []string{store.KindSession}, SessionWindow: Window{Always: true}}, nil)
	steps := drain(t, j)
	embedded, skipped := 0, 0
	for _, s := range steps {
		embedded += s.Embedded
		skipped += s.Skipped
	}
	if embedded != 2 || skipped != 1 {
		t.Fatalf("%+v", steps)
	}
	var marks int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM session_embedding_sync`).Scan(&marks); err != nil || marks != 1 {
		t.Fatalf("a session whose only open text is refused for good is complete: %d marks, %v", marks, err)
	}
	if s := f.sessionStatus(t); s.Current != 2 || s.Parked != 1 {
		t.Fatalf("%+v", s)
	}
	calls := f.srv.Calls()
	for range 3 {
		if res, err := j.Step(context.Background()); err != nil || res != (Result{}) {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if f.srv.Calls() != calls {
		t.Fatal("the refused text was sent again")
	}
}
