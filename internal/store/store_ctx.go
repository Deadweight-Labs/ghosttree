package store

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"
)

// WithContext returns a view of the store for one piece of work that may be
// given up: the reads of the search paths (requests, knowledge, ideas, pains,
// tasks, sessions, facts and roles) end when ctx ends, and so does the wait for
// the vector of a query. The view shares the database, the writer and every
// setting with s; writes still run to their end on the writer. It is for reads:
// a view is never closed, and its lazily made parts (device and join flows) are
// not shared with s.
func (s *Store) WithContext(ctx context.Context) *Store {
	v := &Store{db: s.db, path: s.path, snapshotFault: s.snapshotFault, writer: s.writer, bookkeeper: s.bookkeeper,
		acfg: s.accessCfg(), esh: s.embedShared(), chunkMap: s.chunkMapState(), qctx: ctx}
	if s.reader != nil {
		v.reader = s.reader.WithContext(ctx)
	}
	return v
}

// ctx is the context of the reads of this store: the one of WithContext, or none.
func (s *Store) ctx() context.Context {
	if s.qctx != nil {
		return s.qctx
	}
	return context.Background()
}

// ReadPoolSize is how many connections the reads of the store may hold at once;
// 0 means no limit.
func (s *Store) ReadPoolSize() int {
	if s.reader != nil {
		return s.reader.ReadPoolSize()
	}
	return s.db.Stats().MaxOpenConnections
}

// PoolWaitMark is the state of the wait counters of the read pool at one moment.
type PoolWaitMark struct {
	db       *sql.DB
	count    int64
	duration time.Duration
}

// MarkPoolWait notes the wait counters of the read pool. It takes no connection
// and queues nowhere (db.Stats only reads the counters), so a request that
// measures its wait does not add a wait of its own, which a probe that borrowed
// a connection did when the pool was full: the connection it gave back went to
// another waiter, and the request waited a second time (PAIN-76, KN-4518).
func (s *Store) MarkPoolWait() PoolWaitMark {
	if s.reader != nil {
		return s.reader.MarkPoolWait()
	}
	st := s.db.Stats()
	return PoolWaitMark{db: s.db, count: st.WaitCount, duration: st.WaitDuration}
}

// Since is how long queries of the pool have waited for a connection since the
// mark, and in how many waits. It is an approximation for one request: the
// counters belong to the pool, so the waits of requests that run at the same time
// are in it too, and a wait that has not ended yet is not counted until it does.
// With few requests at once it is the request's own wait; under load it is an
// upper bound that grows with the queue, which is the thing it is read for.
func (m PoolWaitMark) Since() (time.Duration, int64) {
	if m.db == nil {
		return 0, 0
	}
	st := m.db.Stats()
	return st.WaitDuration - m.duration, st.WaitCount - m.count
}

// chunkMapState remembers whether the database has chunk_index_chunk. A store
// opened read-only on a database no server has migrated yet lacks it; once seen
// it stays.
type chunkMapState struct{ seen atomic.Bool }

func (s *Store) chunkMapState() *chunkMapState {
	s.chunkMapOnce.Do(func() {
		if s.chunkMap == nil {
			s.chunkMap = &chunkMapState{}
		}
	})
	return s.chunkMap
}

// hasChunkMap says whether the narrow index from chunks to sessions exists. The
// searches name it with INDEXED BY, as SQLite would otherwise read the wide rows
// of chunk_index by their rowid; without it they join plainly.
func (s *Store) hasChunkMap() bool {
	m := s.chunkMapState()
	if m.seen.Load() {
		return true
	}
	var n int
	if err := s.db.QueryRowContext(s.ctx(), `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='chunk_index_chunk'`).Scan(&n); err != nil && err != sql.ErrNoRows {
		return false
	}
	if n > 0 {
		m.seen.Store(true)
	}
	return n > 0
}
