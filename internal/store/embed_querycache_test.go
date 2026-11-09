package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The kinds of one search ask for the vector of the same text at the same time:
// the server is asked once, and asked anew by the next search.
func TestQueryVectorIsAskedOnceForSearchesAtTheSameTime(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, c := newFakeEmbedder(t)
	srv.SetDelay(150 * time.Millisecond)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: EmbedKinds()})
	var wg sync.WaitGroup
	vecs := make([][]float32, 4)
	for i := range vecs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vecs[i], _, _ = st.queryVector("zebra crossing", SearchAuto, KindKnowledge)
		}()
	}
	wg.Wait()
	for i, v := range vecs {
		if len(v) == 0 {
			t.Fatalf("search %d got no vector", i)
		}
	}
	if n := srv.Calls(); n != 1 {
		t.Fatalf("%d calls for four searches at the same time, want 1", n)
	}
	if v, _, _ := st.queryVector("zebra crossing", SearchAuto, KindKnowledge); len(v) == 0 || srv.Calls() != 2 {
		t.Fatalf("the next search: %d calls, want 2", srv.Calls())
	}
	// A caller that gives up stops waiting at once; the others still get the vector.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := st.embedShared().queries.vector(ctx, st.embedShared().cfg.Load(), "walrus")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the caller that gave up: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("the caller that gave up still waits")
	}
}
