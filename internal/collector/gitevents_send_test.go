package collector

import (
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Antworten des Servers auf Stapel von Git-Vorgängen (Review von PR #242): ein
// unbrauchbarer Vorgang kostet nur sich selbst, nicht bis zu 500 andere.

// scriptedUp antwortet nach einer Funktion über den Stapel.
type scriptedUp struct {
	fakeUp
	answer   func(batch []store.SessionGitEvent) (store.SessionGitResult, error)
	accepted []store.SessionGitEvent
	calls    int
	sizes    []int
}

func (u *scriptedUp) RecordSessionGitEvents(ev []store.SessionGitEvent) (store.SessionGitResult, error) {
	u.calls++
	u.sizes = append(u.sizes, len(ev))
	res, err := u.answer(ev)
	if err == nil {
		u.accepted = append(u.accepted, ev...)
	}
	return res, err
}

func queueOf(n int) *gitScan {
	g := newGitScan("claude-code", "s-1")
	for i := 0; i < n; i++ {
		g.queue = append(g.queue, store.SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-1", Seq: i, Kind: "commit", StartedAt: "2026-11-08T10:00:00Z", OK: true})
	}
	return g
}

func TestAnUnusableEventIsIsolatedAndTheRestIsSent(t *testing.T) {
	resetGitSupport(t)
	for _, bad := range []int{0, 6, 499} {
		g := queueOf(500)
		up := &scriptedUp{answer: func(b []store.SessionGitEvent) (store.SessionGitResult, error) {
			for _, e := range b {
				if e.Seq == bad {
					return store.SessionGitResult{}, statusErr{400}
				}
			}
			return store.SessionGitResult{Recorded: len(b)}, nil
		}}
		g.send(up)
		if len(g.queue) != 0 {
			t.Fatalf("bad=%d: %d events still wait", bad, len(g.queue))
		}
		if len(up.accepted) != 499 {
			t.Fatalf("bad=%d: the server got %d events, want the 499 usable ones", bad, len(up.accepted))
		}
		for _, e := range up.accepted {
			if e.Seq == bad {
				t.Fatalf("bad=%d: the unusable event was accepted", bad)
			}
		}
		if up.calls > 40 {
			t.Fatalf("bad=%d: %d requests to find one event", bad, up.calls)
		}
	}
}

func TestTooLargeABatchIsSplitInsteadOfDropped(t *testing.T) {
	resetGitSupport(t)
	g := queueOf(100)
	up := &scriptedUp{answer: func(b []store.SessionGitEvent) (store.SessionGitResult, error) {
		if len(b) > 30 {
			return store.SessionGitResult{}, statusErr{413}
		}
		return store.SessionGitResult{Recorded: len(b)}, nil
	}}
	g.send(up)
	if len(g.queue) != 0 || len(up.accepted) != 100 {
		t.Fatalf("queue %d, accepted %d", len(g.queue), len(up.accepted))
	}
}

func TestARefusalOfTheSessionKeepsTheEventsAndPausesTheRetries(t *testing.T) {
	resetGitSupport(t)
	for _, code := range []int{401, 403, 409, 429} {
		g := queueOf(10)
		answer := statusErr{code}
		var fail = true
		up := &scriptedUp{answer: func(b []store.SessionGitEvent) (store.SessionGitResult, error) {
			if fail {
				return store.SessionGitResult{}, answer
			}
			return store.SessionGitResult{Recorded: len(b)}, nil
		}}
		g.send(up)
		if len(g.queue) != 10 || up.calls != 1 {
			t.Fatalf("%d: %d events wait after %d calls; nothing may be dropped", code, len(g.queue), up.calls)
		}
		// Innerhalb der Pause fragt der Collector nicht erneut.
		g.send(up)
		if up.calls != 1 {
			t.Fatalf("%d: asked again during the pause (%d calls)", code, up.calls)
		}
		// Nach der Pause: wieder fragen; die Pause wächst.
		first := g.holdFor
		g.holdUntil = time.Now().Add(-time.Second)
		g.send(up)
		if up.calls != 2 || g.holdFor <= first {
			t.Fatalf("%d: calls %d, pause %v after %v", code, up.calls, g.holdFor, first)
		}
		// Ist der Grund behoben, geht alles und die Pause ist vergessen.
		fail = false
		g.holdUntil = time.Now().Add(-time.Second)
		g.send(up)
		if len(g.queue) != 0 || len(up.accepted) != 10 || g.holdFor != 0 {
			t.Fatalf("%d: queue %d accepted %d pause %v", code, len(g.queue), len(up.accepted), g.holdFor)
		}
	}
}

func TestTheServerSaysWhatItDroppedAndTheQueueStillEmpties(t *testing.T) {
	resetGitSupport(t)
	g := queueOf(5)
	up := &scriptedUp{answer: func(b []store.SessionGitEvent) (store.SessionGitResult, error) {
		return store.SessionGitResult{Recorded: 3, Rejected: 2, Reason: "started_at lies in the future"}, nil
	}}
	g.send(up)
	if len(g.queue) != 0 || up.calls != 1 {
		t.Fatalf("queue %d, calls %d", len(g.queue), up.calls)
	}
}
