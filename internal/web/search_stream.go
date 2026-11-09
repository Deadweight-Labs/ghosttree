package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The live search page: /ui/search?live=1 is sent at once with an empty slot per
// kind; /ui/search/stream searches the kinds at the same time and sends each
// group as soon as it is done. The kinds, lines and rights are those of the page
// without live (planSearch, searchKind), so the stream shows a viewer exactly what
// the page would.
//
// Each kind has a time budget. A kind that does not finish within it is given up
// (its context ends, which interrupts its SQL) and the page offers to load it
// again; /ui/search/stream?only=<kind> does that with the longer budget.
// Leaving the page or typing a new search closes the stream, which ends the
// context of every kind still running: each kind reads through a view of the
// store bound to it (store.WithContext). An account has one search stream at a
// time, and one stream per kind loaded again; a new search of the account ends
// all of them. The kinds of all searches share the read connections of the
// server, one less than there are, so that pages keep a connection (searchLimiter).

var (
	searchKindBudget     = 4 * time.Second
	searchKindFullBudget = 60 * time.Second
	searchKeepalive      = 15 * time.Second
)

// searchLimiter is the number of kinds searched at once on the server: one less
// than the read connections of the store, at least one.
type searchLimiter struct {
	once  sync.Once
	slots chan struct{}
}

// searchSlotsUnlimited is the number of kinds at once when the store sets no
// limit on its connections.
const searchSlotsUnlimited = 4

func (l *searchLimiter) acquire(ctx context.Context, st *store.Store) (release func(), ok bool) {
	l.once.Do(func() {
		n := st.ReadPoolSize() - 1
		if st.ReadPoolSize() <= 0 {
			n = searchSlotsUnlimited
		}
		l.slots = make(chan struct{}, max(n, 1))
	})
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// searchStreams are the running search streams by account: the search itself
// under the key of the account, a kind loaded again under the account and the
// kind.
type searchStreams struct {
	mu sync.Mutex
	m  map[string]*searchStreamEntry
}

type searchStreamEntry struct{ cancel context.CancelFunc }

// start registers a stream and ends the ones it replaces: a search ends every
// stream of the account, a kind loaded again the one of the same kind. The
// returned function takes the stream out again.
func (s *searchStreams) start(account, only string, cancel context.CancelFunc) func() {
	key := account + "\x00" + only
	e := &searchStreamEntry{cancel: cancel}
	s.mu.Lock()
	if s.m == nil {
		s.m = map[string]*searchStreamEntry{}
	}
	for k, old := range s.m {
		if k == key || (only == "" && strings.HasPrefix(k, account+"\x00")) {
			old.cancel()
			delete(s.m, k)
		}
	}
	s.m[key] = e
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.m[key] == e {
			delete(s.m, key)
		}
		s.mu.Unlock()
	}
}

// searchKindLabel is the message key of the heading of each kind.
var searchKindLabel = map[string]string{"requests": "nav.requests", "ideas": "issues.seg.ideas", "knowledge": "nav.knowledge",
	"sessions": "nav.sessions", "tasks": "nav.tab_tasks", "pains": "nav.tab_pains"}

// searchLive is the frame of the live page.
type searchLive struct {
	// Stream is the address of the stream, Sync the page that searches before it
	// is sent (for a browser without the page script).
	Stream, Sync string
	Slots        []searchSlot
	Total        int
}

// searchSlot is the place of one kind on the live page.
type searchSlot struct {
	Key, Label, Mark string
	// Show: the hits of this kind are shown (the page is not narrowed to another).
	Show bool
}

// liveView builds the frame for the plan.
func (p *searchPlan) liveView(r *http.Request) *searchLive {
	qs := r.URL.Query()
	qs.Del("live")
	kind := qs.Get("kind")
	if !slices.Contains(p.kinds, kind) {
		kind = ""
		qs.Del("kind")
	}
	lv := &searchLive{Stream: "/ui/search/stream?" + qs.Encode(), Sync: "/ui/search?" + qs.Encode(), Total: len(p.kinds)}
	for _, k := range p.kinds {
		lv.Slots = append(lv.Slots, searchSlot{Key: k, Label: p.l.msg(searchKindLabel[k]), Mark: searchKindMark[k], Show: kind == "" || kind == k})
	}
	return lv
}

// searchEvent is the data of one message of the stream. Side is the row of the
// kind in the list of kinds, Body its group of hits; both are empty for a kind
// without hits. A kind that ran out of time has neither and Slow set.
type searchEvent struct {
	Key  string `json:"key"`
	Side string `json:"side,omitempty"`
	Body string `json:"body,omitempty"`
	Slow bool   `json:"slow,omitempty"`
}

func (a *app) searchStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	p := a.planSearch(r)
	kinds, budget := p.kinds, searchKindBudget
	only := r.URL.Query().Get("only")
	if only != "" {
		kinds, budget = nil, searchKindFullBudget
		if slices.Contains(p.kinds, only) {
			kinds = []string{only}
		}
	}
	if !p.runnable() {
		kinds = nil
	}
	current := r.URL.Query().Get("kind")
	if !slices.Contains(p.kinds, current) {
		current = ""
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if len(kinds) > 0 {
		defer a.searchStreams.start(browserPrincipal(r).ID, only, cancel)()
	}
	type result struct {
		key  string
		g    searchGroup
		slow bool
	}
	// Room for the answer and the timer of every kind: no goroutine waits on a
	// page that is gone.
	results := make(chan result, 2*len(kinds))
	for _, key := range kinds {
		kctx, kcancel := context.WithTimeout(ctx, budget)
		go func() {
			defer kcancel()
			g := a.searchKind(kctx, r, p, key)
			results <- result{key: key, g: g, slow: kctx.Err() != nil}
		}()
		timer := time.AfterFunc(budget, func() { results <- result{key: key, slow: true} })
		defer timer.Stop()
	}
	send := func(name string, ev any) bool {
		raw, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	keepalive := time.NewTicker(searchKeepalive)
	defer keepalive.Stop()
	finished := map[string]bool{}
	for len(finished) < len(kinds) {
		var res result
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			// A proxy closes a stream that stays silent; a comment keeps it open.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			continue
		case res = <-results:
		}
		if finished[res.key] {
			continue
		}
		finished[res.key] = true
		if ctx.Err() != nil {
			return
		}
		ev := searchEvent{Key: res.key, Slow: res.slow}
		if !res.slow && len(res.g.Hits) > 0 {
			res.g.On = res.g.Key == current
			ev.Side, ev.Body = a.searchFragment(p.l, "searchkind", res.g), a.searchFragment(p.l, "searchgroup", res.g)
		}
		if !send("group", ev) {
			return
		}
	}
	send("done", struct{}{})
}

// searchFragment renders one part of the search page.
func (a *app) searchFragment(l lang, name string, g searchGroup) string {
	var b bytes.Buffer
	if err := l.page().ExecuteTemplate(&b, name, g); err != nil {
		a.log().Error("search_fragment", "template", name, "kind", g.Key, "error", err.Error())
		return ""
	}
	return b.String()
}
