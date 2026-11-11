package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/deliverylog"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Tests of the review of PR #249: the job takes a search place, one broken
// session does not stop the backlog, a transcript that was not read to its end is
// not judged as 0.

// reviewSession is a finished session of account 7 with one delivered entry and
// the given assistant lines; the delivery is at base, the lines after it.
func reviewSession(t *testing.T, s *Store, key string, base time.Time, entry int64, texts ...string) int64 {
	t.Helper()
	sid, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: key, Scope: scope.Axes{Project: logProject}, AccountID: 7})
	if err != nil {
		t.Fatal(err)
	}
	var lines, roles []string
	for i, text := range texts {
		lines = append(lines, useLine(base.Add(time.Duration(5+i)*time.Second), "assistant", useText(text)))
		roles = append(roles, "assistant")
	}
	useAppend(t, s, sid, false, lines, roles)
	serve(t, s, 7, key, entry)
	if err := s.RecordContextLog(ContextLogBatch{Project: logProject, SessionRef: key, Harness: "claude", Events: []DeliveryEvent{{
		Call: "call" + strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, key), At: base.Format(time.RFC3339),
		Parts: []deliverylog.Part{{Via: deliverylog.ViaBootstrap, Kind: deliverylog.KindKnowledge, ID: entry, Chars: 50}}}}}, 7); err != nil {
		t.Fatal(err)
	}
	return sid
}

func reviewEntry(t *testing.T, s *Store, title string) int64 {
	t.Helper()
	id, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: title, Body: "Text des Eintrags.", Person: "robin"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rowOf(t *testing.T, s *Store, key string) (used int, judgedAt string) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT used, judged_at FROM context_delivery_log WHERE session_key=?`, key).Scan(&used, &judgedAt); err != nil {
		t.Fatal(err)
	}
	return
}

func judgeLater(t *testing.T, s *Store, opt JudgeOptions) JudgeResult {
	t.Helper()
	opt.Now = time.Now().Add(2 * time.Hour)
	res, err := s.JudgeDeliveries(context.Background(), opt)
	if err != nil {
		t.Fatalf("step failed: %v (result %+v)", err, res)
	}
	return res
}

// A session that cannot be read does not stop the backlog: its rows are closed
// without a verdict, the sessions around it are judged, and the next step goes on.
// Before, the error aborted the step before anything was written, and since the
// oldest deliveries come first the next step chose the same session again.
func TestJudgeDeliveriesBrokenSessionDoesNotBlockTheBacklog(t *testing.T) {
	setup := func(t *testing.T) *Store {
		s := openTest(t)
		base := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
		entry := reviewEntry(t, s, "Eintrag für die Giftpille")
		bad := reviewSession(t, s, "broken", base, entry, "irgendetwas")
		// A frame no decoder can open, and no derived text next to it.
		if _, err := s.db.Exec(`UPDATE session_chunks SET text = '', raw = X'ffff' WHERE session_id = ?`, bad); err != nil {
			t.Fatal(err)
		}
		reviewSession(t, s, "healthy", base.Add(time.Hour), entry, fmt.Sprintf("Siehe KN-%d.", entry))
		return s
	}
	t.Run("one step", func(t *testing.T) {
		s := setup(t)
		res := judgeLater(t, s, JudgeOptions{})
		if res.Failed != 1 || res.Sessions != 2 || res.PickedUp != 1 {
			t.Errorf("result = %+v, want one failed session and one pick-up", res)
		}
		if used, at := rowOf(t, s, "broken"); used != UsedPending || at == "" {
			t.Errorf("broken session: used=%d judged_at=%q, want closed without a verdict", used, at)
		}
		if used, _ := rowOf(t, s, "healthy"); used != UsedPickedUp {
			t.Errorf("healthy session: used=%d, want 1", used)
		}
	})
	t.Run("the next step gets past it", func(t *testing.T) {
		s := setup(t)
		// One session per step: the broken one is the oldest and comes first.
		res := judgeLater(t, s, JudgeOptions{MaxSessions: 1})
		if res.Failed != 1 || !res.More {
			t.Fatalf("first step = %+v, want the failure and more to do", res)
		}
		res = judgeLater(t, s, JudgeOptions{MaxSessions: 1})
		if res.Failed != 0 || res.PickedUp != 1 {
			t.Errorf("second step = %+v, want the healthy session judged", res)
		}
		if n := logCount(t, s, `SELECT COUNT(*) FROM context_delivery_log WHERE judged_at = ''`); n != 0 {
			t.Errorf("%d rows still open", n)
		}
	})
}

// A step that runs out of its time budget stops without an error and says that
// more is due; what it did is kept (every session is written on its own).
func TestJudgeDeliveriesStopsAtItsBudgetWithoutAnError(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	entry := reviewEntry(t, s, "Eintrag für das Budget")
	reviewSession(t, s, "slow-1", base, entry, "nichts")
	res := judgeLater(t, s, JudgeOptions{Budget: time.Nanosecond})
	if res.Sessions != 0 || !res.More {
		t.Errorf("a spent budget: %+v, want no session and more to do", res)
	}
	if used, at := rowOf(t, s, "slow-1"); used != UsedPending || at != "" {
		t.Errorf("row touched without a session read: used=%d judged_at=%q", used, at)
	}
	if res := judgeLater(t, s, JudgeOptions{}); res.Sessions != 1 || res.Rows != 1 {
		t.Errorf("the next step: %+v", res)
	}
}

// Each session is written when it is done: a failure of a later one does not take
// the earlier verdicts with it, whichever way the step ends.
func TestJudgeDeliveriesWritesEachSessionAsItGoes(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	entry := reviewEntry(t, s, "Eintrag für das Schreiben")
	reviewSession(t, s, "first", base, entry, fmt.Sprintf("KN-%d", entry))
	late := reviewSession(t, s, "second", base.Add(time.Hour), entry, "x")
	if _, err := s.db.Exec(`UPDATE session_chunks SET text = '', raw = X'ffff' WHERE session_id = ?`, late); err != nil {
		t.Fatal(err)
	}
	judgeLater(t, s, JudgeOptions{})
	if used, at := rowOf(t, s, "first"); used != UsedPickedUp || at == "" {
		t.Errorf("first session: used=%d judged_at=%q", used, at)
	}
}

// The job takes a search place for a session and gives the step up when none is
// free: nothing is read, nothing is closed, no error.
func TestJudgeDeliveriesYieldsWhenNoSearchPlaceIsFree(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	entry := reviewEntry(t, s, "Eintrag für den Suchplatz")
	reviewSession(t, s, "needs-a-place", base, entry, fmt.Sprintf("KN-%d", entry))
	var held []func()
	for {
		release, ok := s.AcquireSearchSlot(context.Background(), time.Millisecond)
		if !ok {
			break
		}
		held = append(held, release)
	}
	if len(held) == 0 {
		t.Fatal("no search place to take")
	}
	res := judgeLater(t, s, JudgeOptions{SlotWait: 20 * time.Millisecond})
	if !res.Yielded || !res.More || res.Sessions != 0 || res.Rows != 0 {
		t.Errorf("with every place taken: %+v, want a yielded step", res)
	}
	if used, at := rowOf(t, s, "needs-a-place"); used != UsedPending || at != "" {
		t.Errorf("row changed while the step yielded: used=%d judged_at=%q", used, at)
	}
	for _, release := range held {
		release()
	}
	if res := judgeLater(t, s, JudgeOptions{}); res.Yielded || res.PickedUp != 1 {
		t.Errorf("with the places free again: %+v", res)
	}
	// The step gives its place back: a search finds one.
	release, ok := s.AcquireSearchSlot(context.Background(), time.Second)
	if !ok {
		t.Fatal("the job kept a search place")
	}
	release()
}

// A transcript longer than the cap is not read to its end: lines without a find
// are closed as not judged, never as 0; a find before the cap stands.
func TestJudgeDeliveriesDoesNotCountAnUnreadRestAsNothing(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	entry := reviewEntry(t, s, "Eintrag für die Obergrenze")
	hit := reviewEntry(t, s, "Eintrag mit Fund")
	reviewSession(t, s, "long-miss", base, entry, "a", "b", "c", "d", fmt.Sprintf("KN-%d erst nach der Grenze", entry))
	reviewSession(t, s, "long-hit", base, hit, fmt.Sprintf("KN-%d", hit), "b", "c", "d", "e")
	reviewSession(t, s, "short", base, entry, "a", "b")
	res := judgeLater(t, s, JudgeOptions{LineCap: 4})
	if used, at := rowOf(t, s, "long-miss"); used != UsedPending || at == "" {
		t.Errorf("long transcript without a find: used=%d judged_at=%q, want closed as not judged", used, at)
	}
	if used, _ := rowOf(t, s, "long-hit"); used != UsedPickedUp {
		t.Errorf("long transcript with a find in its first lines: used=%d, want 1", used)
	}
	if used, at := rowOf(t, s, "short"); used != UsedNone || at == "" {
		t.Errorf("transcript within the cap: used=%d judged_at=%q, want 0", used, at)
	}
	if res.Unjudged != 1 {
		t.Errorf("unjudged = %d, want 1 (%+v)", res.Unjudged, res)
	}
}

// Lines the index has no time for cannot be placed before or after the delivery:
// they do not count, and a session with nothing else is not judged as 0.
func TestJudgeDeliveriesNeedsTheTimeOfAChunk(t *testing.T) {
	s := openTest(t)
	f := newUsageFixture(t, s, false)
	if _, err := s.db.Exec(`UPDATE chunk_index SET ts = '' WHERE session_id = ?`, f.sid); err != nil {
		t.Fatal(err)
	}
	res := f.judge(t)
	for _, id := range []int64{f.picked, f.none, f.followed} {
		if used, at := f.used(t, id); used != UsedPending || at == "" {
			t.Errorf("entry %d: used=%d judged_at=%q, want closed as not judged", id, used, at)
		}
	}
	if res.PickedUp != 0 || res.Followed != 0 || res.Unjudged != 3 {
		t.Errorf("result = %+v", res)
	}
}
