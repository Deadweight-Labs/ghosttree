package store

import (
	"database/sql"
	"sync"
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
const stmtCacheMax = 256

type stmtKey struct {
	db    *sql.DB
	query string
}

type stmtCache struct {
	mu     sync.Mutex
	stmts  map[stmtKey]*sql.Stmt
	closed bool
}

func (c *stmtCache) get(db *sql.DB, query string) *sql.Stmt {
	key := stmtKey{db, query}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.stmts[key]; ok {
		return st
	}
	if c.closed || len(c.stmts) >= stmtCacheMax {
		return nil
	}
	st, err := db.Prepare(query)
	if err != nil {
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
}

// queryRow is db.QueryRow through the cache of prepared statements. The query
// text must be fixed (see above).
func (s *Store) queryRow(query string, args ...any) *sql.Row {
	if st := s.embedShared().stmts.get(s.db, query); st != nil {
		return st.QueryRowContext(s.ctx(), args...)
	}
	return s.db.QueryRowContext(s.ctx(), query, args...)
}
