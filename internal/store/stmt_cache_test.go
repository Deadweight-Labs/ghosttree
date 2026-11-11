package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

func openStmtDB(t *testing.T, conns int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(conns)
	t.Cleanup(func() { db.Close() })
	return db
}

// Two callers that prepare the same text at once end with one statement in the
// cache; the one that lost the race closes its own and uses the kept one.
func TestStmtCacheKeepsOneStatementWhenPreparesRace(t *testing.T) {
	db := openStmtDB(t, 8)
	var c stmtCache
	t.Cleanup(c.close)
	const callers = 16
	got := make([]*sql.Stmt, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i] = c.get(context.Background(), db, `SELECT 1`)
		}()
	}
	close(start)
	wg.Wait()
	for i, st := range got {
		if st == nil || st != got[0] {
			t.Fatalf("caller %d got %p, caller 0 got %p: the callers must share the kept statement", i, st, got[0])
		}
	}
	if len(c.stmts) != 1 {
		t.Fatalf("cache holds %d statements, want 1", len(c.stmts))
	}
	var n int
	if err := got[0].QueryRow().Scan(&n); err != nil || n != 1 {
		t.Fatalf("kept statement: %d, %v", n, err)
	}
}

// A text that cannot be prepared is not tried again at once, and is tried again
// after the pause.
func TestStmtCacheDoesNotRetryAFailureEndlessly(t *testing.T) {
	db := openStmtDB(t, 1)
	var c stmtCache
	t.Cleanup(c.close)
	const q = `SELECT x FROM later`
	if st := c.get(context.Background(), db, q); st != nil {
		t.Fatal("a statement on a missing table was prepared")
	}
	key := stmtKey{db, q}
	first, ok := c.failed[key]
	if !ok {
		t.Fatal("the failure was not noted")
	}
	if _, err := db.Exec(`CREATE TABLE later(x)`); err != nil {
		t.Fatal(err)
	}
	if st := c.get(context.Background(), db, q); st != nil {
		t.Fatal("the text was tried again within the pause")
	}
	if c.failed[key] != first {
		t.Fatal("the pause was restarted by a call that did not try")
	}
	c.failed[key] = time.Now().Add(-stmtRetryAfter - time.Second)
	if st := c.get(context.Background(), db, q); st == nil {
		t.Fatal("the text was not tried again after the pause")
	}
	if _, ok := c.failed[key]; ok && c.stmts[key] == nil {
		t.Fatal("neither kept nor noted")
	}
}

// Preparing waits for a connection outside the mutex and under the context of
// the caller: a caller stuck on a full pool does not hold up a cached statement,
// and giving up is not noted as a failure of the text.
func TestStmtCachePreparesOutsideItsLockWithTheContextOfTheCaller(t *testing.T) {
	db := openStmtDB(t, 1)
	var c stmtCache
	t.Cleanup(c.close)
	cached := c.get(context.Background(), db, `SELECT 1`)
	if cached == nil {
		t.Fatal("not prepared")
	}
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan *sql.Stmt, 1)
	go func() { blocked <- c.get(ctx, db, `SELECT 2`) }()
	waitDeadline := time.Now().Add(5 * time.Second)
	for db.Stats().WaitCount < 1 {
		if time.Now().After(waitDeadline) {
			t.Fatal("the prepare never queued for a connection")
		}
		time.Sleep(time.Millisecond)
	}

	hit := make(chan *sql.Stmt, 1)
	go func() { hit <- c.get(context.Background(), db, `SELECT 1`) }()
	select {
	case st := <-hit:
		if st != cached {
			t.Fatal("a cached statement was not returned")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cached statement waited behind a prepare that is stuck on the pool")
	}

	cancel()
	select {
	case st := <-blocked:
		if st != nil {
			t.Fatal("a cancelled prepare returned a statement")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the prepare ignored the context of the caller")
	}
	c.mu.Lock()
	_, noted := c.failed[stmtKey{db, `SELECT 2`}]
	c.mu.Unlock()
	if noted {
		t.Fatal("a caller that gave up was noted as a failure of the statement")
	}
}

func TestStmtCacheRefusesAfterClose(t *testing.T) {
	db := openStmtDB(t, 1)
	var c stmtCache
	c.close()
	if st := c.get(context.Background(), db, `SELECT 1`); st != nil {
		t.Fatal("a closed cache prepared a statement")
	}
}
