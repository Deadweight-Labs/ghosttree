package store

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// modernc.org/sqlite translates the text of a query again on every call, and on
// the hook routes that was a third of the processor time: a prompt asks the same
// few fixed questions of the index for every word (PAIN-76). A prepared
// statement is translated once per connection and run many times, so the hot,
// fixed queries go through this cache. It is shared by a store, its reader and
// its views (embedShared), keyed by the database the statement belongs to and the
// text of the query, and closed with the store.
//
// Only queries whose text does not depend on the caller belong here: every
// distinct text is a statement that stays prepared. The cache refuses new texts
// beyond stmtCacheMax and the caller then runs the query unprepared, as before.
//
// Preparing takes a connection and can wait for one, so it happens outside the
// mutex and under the context of the caller: a caller that has given up does not
// hold up the others, and is not blamed for a failure it caused itself. A text
// that fails to prepare (a table this database lacks) is not tried again for
// stmtRetryAfter, so a query that can never be prepared costs one attempt, not
// one per call.
const (
	stmtCacheMax   = 256
	stmtRetryAfter = 30 * time.Second
)

type stmtKey struct {
	db    *sql.DB
	query string
}

type stmtCache struct {
	mu     sync.Mutex
	stmts  map[stmtKey]*sql.Stmt
	failed map[stmtKey]time.Time
	closed bool
}

// get returns the prepared statement for the query, preparing it on first use,
// or nil when the caller should run the query unprepared.
func (c *stmtCache) get(ctx context.Context, db *sql.DB, query string) *sql.Stmt {
	key := stmtKey{db, query}
	c.mu.Lock()
	if st, ok := c.stmts[key]; ok {
		c.mu.Unlock()
		return st
	}
	if c.closed || len(c.stmts) >= stmtCacheMax {
		c.mu.Unlock()
		return nil
	}
	if at, ok := c.failed[key]; ok && time.Since(at) < stmtRetryAfter {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	st, err := db.PrepareContext(ctx, query)
	if err != nil {
		// A caller whose context ended is not a verdict on the statement.
		if ctx.Err() != nil {
			return nil
		}
		c.mu.Lock()
		if c.failed == nil || len(c.failed) >= stmtCacheMax {
			c.failed = make(map[stmtKey]time.Time)
		}
		c.failed[key] = time.Now()
		c.mu.Unlock()
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Two callers can prepare the same text at once; the first to get here keeps
	// its statement and the other closes its own.
	if kept, ok := c.stmts[key]; ok {
		_ = st.Close()
		return kept
	}
	if c.closed || len(c.stmts) >= stmtCacheMax {
		_ = st.Close()
		return nil
	}
	if c.stmts == nil {
		c.stmts = make(map[stmtKey]*sql.Stmt)
	}
	c.stmts[key] = st
	return st
}

func (c *stmtCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, st := range c.stmts {
		_ = st.Close()
	}
	c.stmts = nil
	c.failed = nil
}

// queryRow is db.QueryRow through the cache of prepared statements. The query
// text must be fixed (see above).
func (s *Store) queryRow(query string, args ...any) *sql.Row {
	if st := s.embedShared().stmts.get(s.ctx(), s.db, query); st != nil {
		return st.QueryRowContext(s.ctx(), args...)
	}
	return s.db.QueryRowContext(s.ctx(), query, args...)
}
