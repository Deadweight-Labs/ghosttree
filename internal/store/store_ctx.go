package store

import (
	"context"
	"database/sql"
	"sync/atomic"
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

// AwaitReadConn takes a read connection and gives it back at once. What it
// costs is the wait a read of this moment would have: the time a hook spends in
// the queue of the pool, or opening a connection after the pool ran dry. It is
// for the measurement of a route (PAIN-76), which cannot see inside database/sql
// where a query waited; the pool hands the connection it just released to the
// next query, so the probe does not cost a second connection.
func (s *Store) AwaitReadConn(ctx context.Context) {
	if s.reader != nil {
		s.reader.AwaitReadConn(ctx)
		return
	}
	if conn, err := s.db.Conn(ctx); err == nil {
		_ = conn.Close()
	}
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
