package store

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
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
	// held und waiting zählen je Konto, wie viele Plätze es hält und wie viele
	// Suchen es wartend hat (AcquireSearchSlotFor).
	mu      sync.Mutex
	held    map[string]int
	waitFor map[string]int
	// waiting is the number of foreground callers blocked in AcquireSearchSlot.
	// A background caller (AcquireBackgroundSlot) never takes a place while one
	// of them waits.
	waiting atomic.Int32
}

// backgroundPoll is how often a background caller looks again for a free place.
const backgroundPoll = 5 * time.Millisecond

func (g *searchGate) init(s *Store) {
	g.once.Do(func() {
		n := s.ReadPoolSize() - 1
		if s.ReadPoolSize() <= 0 {
			n = searchSlotsUnlimited
		}
		g.slots = make(chan struct{}, max(n, 1))
	})
}

// AcquireSearchSlot waits for a place among the searches of the server, at most
// wait; with wait <= 0 until ctx ends. ok is false when the time or ctx ran out
// first: the caller tells apart a busy server (ctx.Err() == nil) from a caller who
// has gone. A place is never given to a caller whose ctx has ended already.
// release gives the place back.
func (s *Store) AcquireSearchSlot(ctx context.Context, wait time.Duration) (release func(), ok bool) {
	g := &s.embedShared().gate
	g.init(s)
	if ctx.Err() != nil {
		return nil, false
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	default:
	}
	g.waiting.Add(1)
	defer g.waiting.Add(-1)
	var limit <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		limit = t.C
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	case <-ctx.Done():
		return nil, false
	case <-limit:
		return nil, false
	}
}

// AcquireBackgroundSlot is AcquireSearchSlot for work that nobody waits for (the
// usage job, REQ-645): it takes a place only while no search waits for one, so
// the foreground always goes first, and it gives up after wait (or when ctx ends)
// with ok=false. It polls instead of queueing, because a queued caller would
// stand in line with the searches.
func (s *Store) AcquireBackgroundSlot(ctx context.Context, wait time.Duration) (release func(), ok bool) {
	g := &s.embedShared().gate
	g.init(s)
	if wait <= 0 {
		wait = backgroundPoll
	}
	limit := time.NewTimer(wait)
	defer limit.Stop()
	tick := time.NewTicker(backgroundPoll)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		if g.waiting.Load() == 0 {
			select {
			case g.slots <- struct{}{}:
				return func() { <-g.slots }, true
			default:
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-limit.C:
			return nil, false
		case <-tick.C:
		}
	}
}

// AcquireSearchSlotFor ist AcquireSearchSlot für die Suche eines Kontos. Ein
// Konto darf höchstens die Hälfte der Plätze (mindestens einen) halten, solange
// ein anderes Konto wartet; ist sonst niemand da, darf es alle nehmen. So lässt
// ein Konto, das dauernd sucht, die anderen nicht in search_busy laufen.
func (s *Store) AcquireSearchSlotFor(ctx context.Context, account string, wait time.Duration) (release func(), ok bool) {
	if account == "" {
		return s.AcquireSearchSlot(ctx, wait)
	}
	g := &s.embedShared().gate
	g.init(s)
	if ctx.Err() != nil {
		return nil, false
	}
	if release, ok := g.tryTake(account); ok {
		return release, true
	}
	g.mu.Lock()
	if g.waitFor == nil {
		g.waitFor = map[string]int{}
	}
	g.waitFor[account]++
	g.mu.Unlock()
	g.waiting.Add(1)
	defer func() {
		g.waiting.Add(-1)
		g.mu.Lock()
		if g.waitFor[account]--; g.waitFor[account] <= 0 {
			delete(g.waitFor, account)
		}
		g.mu.Unlock()
	}()
	var limit <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		limit = t.C
	}
	tick := time.NewTicker(backgroundPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-limit:
			return nil, false
		case <-tick.C:
		}
		if release, ok := g.tryTake(account); ok {
			return release, true
		}
	}
}

// tryTake nimmt einen Platz für das Konto, wenn einer frei ist und das Konto
// seinen Anteil nicht überschreitet, während ein anderes wartet.
func (g *searchGate) tryTake(account string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	share := max(1, cap(g.slots)/2)
	if g.held[account] >= share {
		for other, n := range g.waitFor {
			if other != account && n > 0 {
				return nil, false
			}
		}
	}
	select {
	case g.slots <- struct{}{}:
	default:
		return nil, false
	}
	if g.held == nil {
		g.held = map[string]int{}
	}
	g.held[account]++
	return func() {
		<-g.slots
		g.mu.Lock()
		if g.held[account]--; g.held[account] <= 0 {
			delete(g.held, account)
		}
		g.mu.Unlock()
	}, true
}
