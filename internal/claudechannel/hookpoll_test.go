package claudechannel

import (
	"context"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A poller with a remembered position must still respect the shared cursor:
// coord_inbox advances it, and what the agent read there is not handed over
// again.
func TestKnownPositionDoesNotDeliverWhatTheInboxAlreadyShowed(t *testing.T) {
	f := newFakeServer(directRoom)
	f.msgs[directRoom.Key] = []store.CoordMessage{{ID: 1, SenderExternalID: "peer", Body: "one"}}
	n := &fakeNotifier{f: f}
	p := newPoller(f, n)
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	for id := int64(2); id <= 4; id++ {
		f.msgs[directRoom.Key] = append(f.msgs[directRoom.Key], store.CoordMessage{ID: id, SenderExternalID: "peer", Body: "later"})
	}
	f.cursor[directRoom.Key] = 4 // coord_inbox read up to 4
	f.msgs[directRoom.Key] = append(f.msgs[directRoom.Key], store.CoordMessage{ID: 5, SenderExternalID: "peer", Body: "fresh"})
	f.mu.Unlock()
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 2 || n.got[1].Content != "fresh" {
		t.Fatalf("delivered %d notifications, want the first and only the fresh one: %+v", len(n.got), n.got)
	}
}

// slowServer answers every mention lookup slowly, like a congested server.
type slowServer struct {
	*fakeServer
	delay time.Duration
}

func (s slowServer) Mentions(self string, id int64) ([]string, error) {
	time.Sleep(s.delay)
	return s.fakeServer.Mentions(self, id)
}

// A backlog behind a slow server ends within the caller's deadline, keeps the
// progress it made, and the next run continues from there.
func TestBacklogStopsAtTheDeadlineAndTheNextRunContinues(t *testing.T) {
	room := store.CoordRoom{Key: "machine:box", Kind: store.RoomMachine}
	f := newFakeServer(room)
	for id := int64(1); id <= 100; id++ {
		f.msgs[room.Key] = append(f.msgs[room.Key], store.CoordMessage{ID: id, SenderExternalID: "peer", Body: "chatter"})
	}
	p := &Poller{Self: "me", Source: slowServer{f, 5 * time.Millisecond}, Notifier: &fakeNotifier{f: f}, Budget: openBudget{}, Now: func() time.Time { return testNow }}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = p.Poll(ctx)
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("the run took %v, far past its 100ms deadline", took)
	}
	first := p.Positions()[room.Key]
	if first <= 0 || first >= 100 {
		t.Fatalf("position after the first run = %d, want partial progress", first)
	}
	q := &Poller{Self: "me", Source: slowServer{f, 5 * time.Millisecond}, Notifier: &fakeNotifier{f: f}, Budget: openBudget{}, Now: func() time.Time { return testNow }}
	q.SetPositions(p.Positions())
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	_, _ = q.Poll(ctx2)
	if second := q.Positions()[room.Key]; second <= first {
		t.Fatalf("second run made no progress: %d -> %d", first, second)
	}
}
