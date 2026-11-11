package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// openSingleConn opens a store on a file and narrows its read pool, the pool the
// hook routes read through, to one connection, so that one held connection
// saturates it. The pool is returned.
func openSingleConn(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pool := s.db
	if s.reader != nil {
		pool = s.reader.db
	}
	pool.SetMaxOpenConns(1)
	return s, pool
}

func waitForWaitCount(t *testing.T, pool *sql.DB, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pool.Stats().WaitCount < n {
		if time.Now().After(deadline) {
			t.Fatalf("wait count stays at %d, want %d", pool.Stats().WaitCount, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// Measuring the wait of a request must not take a connection. On a pool that is
// full, a mark that borrowed one would block, queue as a waiter and hand the
// connection it gave back to somebody else (PAIN-76, KN-4518).
func TestMarkPoolWaitTakesNoConnection(t *testing.T) {
	s, pool := openSingleConn(t)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := pool.Stats().WaitCount
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := s.MarkPoolWait()
		m.Since()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the mark blocked on a full pool")
	}
	if got := pool.Stats().WaitCount; got != before {
		t.Errorf("the mark queued for a connection: waits %d -> %d", before, got)
	}
	_ = held.Close()
}

// A request that needs one connection waits once for it, with a competitor in
// the queue ahead of it: the count of waits since the mark is exactly its own,
// where a probe would have made two (one for the probe, one for the query) and
// lost the connection to the competitor in between. The mark of the store is the
// one of its read pool, the pool the hook routes read through.
func TestHookRequestWaitsOnceOnASaturatedPool(t *testing.T) {
	s, pool := openSingleConn(t)
	ctx := context.Background()
	held, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	query := func() {
		defer wg.Done()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil {
			t.Error(err)
		}
	}
	wg.Add(1)
	go query() // the competitor, queued first
	waitForWaitCount(t, pool, 1)

	mark := s.MarkPoolWait()
	wg.Add(1)
	go query() // the request: one query, one connection
	waitForWaitCount(t, pool, 2)
	time.Sleep(20 * time.Millisecond)
	_ = held.Close()
	wg.Wait()

	d, n := mark.Since()
	if n != 1 {
		t.Errorf("waits since the mark = %d, want 1 (the request's own)", n)
	}
	if d < 20*time.Millisecond {
		t.Errorf("wait since the mark = %v, want at least the 20ms the pool was held", d)
	}
}

func TestPoolWaitMarkOfNoDatabaseIsZero(t *testing.T) {
	var m PoolWaitMark
	if d, n := m.Since(); d != 0 || n != 0 {
		t.Fatalf("zero mark: %v, %d", d, n)
	}
}
