package store

import (
	"context"
	"sync"
)

// Searches are the long reads of the server, and they share its read connections
// with everything else, the calls of the hooks first of all. A hook waits for a
// connection, and a search can hold one for seconds: measured with 12 searchers
// and three connections, a light read of a hook went from 0.46 ms to 26 ms with
// searches that do not touch sessions, to 3.4 s with ones that do (IDEA-22).
//
// So searches wait for a place of their own, one less than there are read
// connections, and one connection stays free for the rest. The places belong to
// the store and are shared by the API and the web interface, because both read
// through the same connections.

// searchSlotsUnlimited is the number of searches at once when the store sets no
// limit on its connections.
const searchSlotsUnlimited = 4

type searchGate struct {
	once  sync.Once
	slots chan struct{}
}

// AcquireSearchSlot waits for a place among the searches of the server. ok is
// false when ctx ended first. release gives the place back.
func (s *Store) AcquireSearchSlot(ctx context.Context) (release func(), ok bool) {
	g := &s.embedShared().gate
	g.once.Do(func() {
		n := s.ReadPoolSize() - 1
		if s.ReadPoolSize() <= 0 {
			n = searchSlotsUnlimited
		}
		g.slots = make(chan struct{}, max(n, 1))
	})
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	case <-ctx.Done():
		return nil, false
	}
}
