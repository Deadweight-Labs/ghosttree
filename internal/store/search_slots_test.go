package store

import (
	"context"
	"testing"
	"time"
)

// Searches get one place less than there are read connections, whichever view of
// the store asks: the connection that is left is the hooks'.
func TestSearchSlotsLeaveAReadConnectionFree(t *testing.T) {
	st, err := OpenWithOptions(t.TempDir()+"/pool.db", OpenOptions{MaxOpenConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var releases []func()
	for i := range 2 {
		// The second place is taken through a view, as a request with a budget does.
		s := st
		if i == 1 {
			s = st.WithContext(context.Background())
		}
		release, ok := s.AcquireSearchSlot(context.Background(), 0)
		if !ok {
			t.Fatalf("no place for search %d", i+1)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := st.AcquireSearchSlot(ctx, 0); ok {
		t.Fatal("a third search got a place with three connections")
	}
	releases[0]()
	release, ok := st.AcquireSearchSlot(context.Background(), 0)
	if !ok {
		t.Fatal("a freed place is not given again")
	}
	release()
	releases[1]()
}

// A search does not wait for ever: with every place taken it gives up after the
// time it was given, and says so by ok=false while its context is still alive, so
// that the caller can answer "busy". A caller who has gone gets no place, even
// when one is free.
func TestSearchSlotWaitHasALimit(t *testing.T) {
	st, err := OpenWithOptions(t.TempDir()+"/wait.db", OpenOptions{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	release, ok := st.AcquireSearchSlot(context.Background(), time.Second)
	if !ok {
		t.Fatal("no place on an idle store")
	}

	ctx := context.Background()
	began := time.Now()
	if _, ok := st.AcquireSearchSlot(ctx, 80*time.Millisecond); ok {
		t.Fatal("a second search got the only place")
	}
	if took := time.Since(began); took < 70*time.Millisecond || took > 2*time.Second || ctx.Err() != nil {
		t.Fatalf("the wait took %v", took)
	}

	// A place that is freed during the wait is given.
	go func() { time.Sleep(30 * time.Millisecond); release() }()
	again, ok := st.AcquireSearchSlot(ctx, 2*time.Second)
	if !ok {
		t.Fatal("a place freed within the wait was not given")
	}

	// A caller who has gone: no place and no wait.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	again()
	if _, ok := st.AcquireSearchSlot(gone, time.Second); ok {
		t.Fatal("a free place was given to a caller whose context had ended")
	}
	// And the one who waits without a limit leaves with its context.
	hold, ok := st.AcquireSearchSlot(ctx, 0)
	if !ok {
		t.Fatal("no place")
	}
	defer hold()
	short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, ok := st.AcquireSearchSlot(short, 0); ok {
		t.Fatal("a search without a limit got a place that is taken")
	}
}
