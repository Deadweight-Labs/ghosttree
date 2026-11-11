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

// Background work (the usage job) takes a place only while one is free and no
// search waits for one; it never queues in front of a search.
func TestBackgroundSlotGivesWayToSearches(t *testing.T) {
	st, err := OpenWithOptions(t.TempDir()+"/bg.db", OpenOptions{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// A free place is taken, and given back.
	release, ok := st.AcquireBackgroundSlot(context.Background(), time.Second)
	if !ok {
		t.Fatal("no place on an idle store")
	}
	// With the only place taken, the background gives up after its wait, and a
	// gone caller gets nothing.
	began := time.Now()
	if _, ok := st.AcquireBackgroundSlot(context.Background(), 30*time.Millisecond); ok {
		t.Fatal("the background got a place that was taken")
	}
	if d := time.Since(began); d < 25*time.Millisecond || d > time.Second {
		t.Errorf("the background waited %v, want about 30ms", d)
	}
	release()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := st.AcquireBackgroundSlot(gone, time.Second); ok {
		t.Fatal("a caller that has gone got a place")
	}

	// A search waits for the held place; the place that is freed goes to it, not
	// to the background caller that is looking at the same moment.
	held, ok := st.AcquireSearchSlot(context.Background(), 0)
	if !ok {
		t.Fatal("no place for the first search")
	}
	g := &st.embedShared().gate
	search := make(chan bool, 1)
	go func() {
		rel, ok := st.AcquireSearchSlot(context.Background(), 5*time.Second)
		if ok {
			defer rel()
			time.Sleep(100 * time.Millisecond) // holds it while the background looks
		}
		search <- ok
	}()
	for deadline := time.Now().Add(5 * time.Second); g.waiting.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the search never started to wait")
		}
		time.Sleep(time.Millisecond)
	}
	bg := make(chan bool, 1)
	go func() {
		rel, ok := st.AcquireBackgroundSlot(context.Background(), 60*time.Millisecond)
		if ok {
			rel()
		}
		bg <- ok
	}()
	time.Sleep(10 * time.Millisecond)
	held()
	if !<-search {
		t.Error("the waiting search did not get the freed place")
	}
	if <-bg {
		t.Error("the background took a place in front of a waiting search")
	}
}
