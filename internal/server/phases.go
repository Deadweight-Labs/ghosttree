package server

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// phaseSet collects the partial times of one request (PAIN-76). A hook route
// spends its time in several places — the knowledge search, the language gate,
// the writer queue, the rights check — and the total alone does not say which.
// The set is written into the http_request log line as the group "phases", in
// milliseconds, so a journal line answers where the time went.
//
// A phase costs two clock reads and one append; a route that does not ask for
// the set pays nothing. Names that repeat (a hint is looked up per word) add up.
type phaseSet struct {
	mu    sync.Mutex
	names []string
	total []time.Duration
}

// phases returns the set of the request, made on first use. The route is
// asked for it by the handlers that are measured; everything else logs no
// "phases" field.
func phases(r *http.Request) *phaseSet {
	record := requestRecordFromContext(r.Context())
	if record == nil {
		return nil
	}
	if record.phases == nil {
		record.phases = &phaseSet{}
	}
	return record.phases
}

// start opens a phase and returns the function that closes it. It is safe on a
// nil set, so a handler run outside the telemetry chain (tests) needs no check.
func (p *phaseSet) start(name string) func() {
	if p == nil {
		return func() {}
	}
	began := time.Now()
	return func() { p.add(name, time.Since(began)) }
}

// trackPool measures the wait for read connections of the whole request as the
// change of the pool's wait counters between now and the call of the returned
// function, which a handler defers. It takes no connection, so measuring adds no
// wait. The phase "pool_wait" is the time queries of the pool waited in that
// span: it lies inside the other phases rather than next to them, and it is an
// approximation, because the counters belong to the pool and the waits of
// requests that run at the same time are in it too (an upper bound under load).
// The phase takes its place at the start of the group, so the log reads the same
// whatever the route did.
func (p *phaseSet) trackPool(st *store.Store) func() {
	if p == nil {
		return func() {}
	}
	p.add("pool_wait", 0)
	mark := st.MarkPoolWait()
	return func() {
		d, _ := mark.Since()
		p.add("pool_wait", d)
	}
}

func (p *phaseSet) add(name string, d time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, n := range p.names {
		if n == name {
			p.total[i] += d
			return
		}
	}
	p.names = append(p.names, name)
	p.total = append(p.total, d)
}

// attr is the log attribute of the set: a group of milliseconds in the order the
// phases first ran. Nil when nothing was measured.
func (p *phaseSet) attr() *slog.Attr {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.names) == 0 {
		return nil
	}
	items := make([]any, len(p.names))
	for i, n := range p.names {
		items[i] = slog.Float64(n, float64(p.total[i].Microseconds())/1000)
	}
	a := slog.Group("phases", items...)
	return &a
}
