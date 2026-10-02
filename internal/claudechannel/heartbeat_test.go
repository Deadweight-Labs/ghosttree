package claudechannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type beatSource struct {
	*fakeServer
	beats int
	fail  error
}

func (b *beatSource) Heartbeat(string) error {
	b.beats++
	return b.fail
}

func TestPollerHeartbeatIsThrottledAndRetriedOnFailure(t *testing.T) {
	f := newFakeServer(store.CoordRoom{Key: "project:x", Kind: store.RoomProject})
	src := &beatSource{fakeServer: f}
	clock := testNow
	p := &Poller{Self: "me", Source: src, Notifier: &fakeNotifier{f: f}, Budget: openBudget{}, Now: func() time.Time { return clock }}
	poll := func() {
		if _, err := p.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Hundreds of polls inside one interval send exactly one heartbeat.
	for i := 0; i < 200; i++ {
		poll()
		clock = clock.Add(100 * time.Millisecond)
	}
	if src.beats != 1 {
		t.Fatalf("beats within one interval = %d, want 1", src.beats)
	}
	clock = testNow.Add(store.HeartbeatInterval + time.Second)
	poll()
	if src.beats != 2 {
		t.Fatalf("beats after the interval = %d, want 2", src.beats)
	}
	// A failed heartbeat does not count as sent: the next poll tries again,
	// and a failure does not abort the poll.
	src.fail = errors.New("server down")
	clock = clock.Add(store.HeartbeatInterval + time.Second)
	poll()
	poll()
	if src.beats != 4 {
		t.Fatalf("failed heartbeat must be retried, beats=%d", src.beats)
	}
}

func TestPollerSendsNoHeartbeatWhileNotReady(t *testing.T) {
	f := newFakeServer(store.CoordRoom{Key: "project:x", Kind: store.RoomProject})
	src := &beatSource{fakeServer: f}
	p := &Poller{Self: "me", Source: src, Notifier: &fakeNotifier{f: f, notUp: true}, Budget: openBudget{}, Now: func() time.Time { return testNow }}
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.beats != 0 {
		t.Fatal("a channel that cannot receive must not report itself as polling")
	}
}
