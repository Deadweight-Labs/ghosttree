package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fatChunks(n int) []Chunk {
	var out []Chunk
	for i := 0; i < n; i++ {
		out = append(out, Chunk{Seq: i, Raw: userLine("2026-10-01T10:00:00Z", fmt.Sprintf("prompt %d %s", i, strings.Repeat("x", 1000)))})
	}
	return out
}

func indexedCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM chunk_index`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOnDemandIndexingStaysWithinTheByteBudgetOfAStep(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, fatChunks(200))
	resetBackfill(t, st)
	oldBytes := maxStepBytes
	maxStepBytes = 20 << 10
	defer func() { maxStepBytes = oldBytes }()
	n, more, err := st.indexSessionBatch(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !more || n == 0 || n > 40 {
		t.Fatalf("one step indexed %d chunks (more=%v) with a 20 KiB budget over 1 KiB chunks", n, more)
	}
	if got := indexedCount(t, st); got != n {
		t.Errorf("indexed %d, step said %d", got, n)
	}
}

func TestOnDemandIndexingStaysWithinTheTimeBudgetOfAStep(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, numbered(120))
	resetBackfill(t, st)
	old := backfillStepBudget
	backfillStepBudget = time.Nanosecond
	defer func() { backfillStepBudget = old }()
	n, more, err := st.indexSessionBatch(s.ID)
	if err != nil || !more || n != backfillGroup {
		t.Fatalf("a step with no time left indexed %d (more=%v, err=%v), want one group of %d", n, more, err, backfillGroup)
	}
}

func TestOnDemandIndexingGivesUpAfterItsBudgetAndLetsThePageRender(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, numbered(400))
	resetBackfill(t, st)
	oldStep, oldTotal := backfillStepBudget, onDemandIndexBudget
	backfillStepBudget, onDemandIndexBudget = time.Nanosecond, time.Nanosecond
	defer func() { backfillStepBudget, onDemandIndexBudget = oldStep, oldTotal }()
	began := time.Now()
	if err := st.IndexSessionContext(context.Background(), s.ID); err != nil {
		t.Fatalf("running out of budget is not an error: %v", err)
	}
	if time.Since(began) > 3*time.Second {
		t.Errorf("took %v", time.Since(began))
	}
	if n := indexedCount(t, st); n == 0 || n >= 400 {
		t.Errorf("indexed %d of 400, want a part", n)
	}
	// With time again the rest follows.
	backfillStepBudget, onDemandIndexBudget = oldStep, time.Minute
	if err := st.IndexSessionContext(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if n := indexedCount(t, st); n != 400 {
		t.Errorf("indexed %d of 400", n)
	}
}

func TestBackfillReadsOnlyWhatItProcesses(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, fatChunks(300))
	resetBackfill(t, st)
	old := backfillStepBudget
	backfillStepBudget = time.Nanosecond
	read := 0
	chunksRead = func(n int) { read += n }
	defer func() { backfillStepBudget, chunksRead = old, nil }()
	if _, err := st.IndexBackfillStep(300); err != nil {
		t.Fatal(err)
	}
	if processed := indexedCount(t, st); read > processed+backfillGroup {
		t.Errorf("a step read %d chunks and processed %d", read, processed)
	}
}

func TestBackfillFinishesAtTheLastChunkAndSetsTheBoundThere(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	legacyChunks(t, st, s.ID, numbered(25))
	resetBackfill(t, st)
	var done bool
	var err error
	for i := 0; !done; i++ {
		if i > 50 {
			t.Fatal("no progress")
		}
		if done, err = st.IndexBackfillStep(10); err != nil {
			t.Fatal(err)
		}
	}
	var bound, cursor, max int64
	_ = st.db.QueryRow(`SELECT val FROM index_state WHERE key='backfill_bound'`).Scan(&bound)
	_ = st.db.QueryRow(`SELECT val FROM index_state WHERE key='backfill_cursor'`).Scan(&cursor)
	_ = st.db.QueryRow(`SELECT MAX(id) FROM session_chunks`).Scan(&max)
	if bound != max || cursor != max {
		t.Errorf("bound=%d cursor=%d, want both %d", bound, cursor, max)
	}
	if p := st.IndexProgress(); !p.Done || p.Percent != 100 {
		t.Errorf("progress = %+v", p)
	}
}

// The byte budget bounds a step inside a group of chunks, not only between
// groups: a group of fat chunks must not read far past the budget.
func TestByteBudgetCutsInsideAGroup(t *testing.T) {
	st := orgStore(t, "robin")
	s := addSession(t, st, "old", 1, "", "m", "", nil)
	var fat []Chunk
	for i := 0; i < 40; i++ {
		fat = append(fat, Chunk{Seq: i, Raw: userLine("2026-10-01T10:00:00Z", strings.Repeat("y", 10<<10))})
	}
	legacyChunks(t, st, s.ID, fat)
	resetBackfill(t, st)
	oldBytes := maxStepBytes
	maxStepBytes = 35 << 10
	defer func() { maxStepBytes = oldBytes }()
	read := 0
	chunksRead = func(n int) { read += n }
	defer func() { chunksRead = nil }()
	n, more, err := st.indexSessionBatch(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !more || n < 1 || n > 4 || read != n {
		t.Fatalf("indexed %d (read %d, more=%v) with a 35 KiB budget over 10 KiB chunks, want at most 4", n, read, more)
	}
	// The rest still follows, nothing is lost.
	if err := st.IndexSessionContext(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if got := indexedCount(t, st); got != 40 {
		t.Errorf("indexed %d of 40", got)
	}
}

func TestBackfillRidesOutTransientWriterErrors(t *testing.T) {
	oldMax := backfillRetryMax
	backfillRetryMax = 5 * time.Millisecond
	defer func() { backfillRetryMax = oldMax }()
	failures := []error{ErrWriterOperationsFull, ErrWriterBytesFull, fmt.Errorf("step: %w", ErrWriterOperationsFull),
		fmt.Errorf("database is locked (5) (SQLITE_BUSY)")}
	calls := 0
	step := func(int) (bool, error) {
		calls++
		if calls <= len(failures) {
			return false, failures[calls-1]
		}
		return calls >= len(failures)+3, nil
	}
	if err := runIndexBackfill(context.Background(), BackfillOptions{Pause: time.Millisecond}, step); err != nil {
		t.Fatalf("transient errors must not end the run: %v", err)
	}
	if calls != len(failures)+3 {
		t.Errorf("calls = %d", calls)
	}
	// A real error still ends it, and a canceled context ends a retry loop.
	boom := fmt.Errorf("disk on fire")
	if err := runIndexBackfill(context.Background(), BackfillOptions{}, func(int) (bool, error) { return false, boom }); err != boom {
		t.Errorf("hard error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runIndexBackfill(ctx, BackfillOptions{}, func(int) (bool, error) { return false, ErrWriterBytesFull }); err == nil {
		t.Error("canceled context must end the run")
	}
}
