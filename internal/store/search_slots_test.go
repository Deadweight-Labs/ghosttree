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
		release, ok := s.AcquireSearchSlot(context.Background())
		if !ok {
			t.Fatalf("no place for search %d", i+1)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := st.AcquireSearchSlot(ctx); ok {
		t.Fatal("a third search got a place with three connections")
	}
	releases[0]()
	release, ok := st.AcquireSearchSlot(context.Background())
	if !ok {
		t.Fatal("a freed place is not given again")
	}
	release()
	releases[1]()
}
