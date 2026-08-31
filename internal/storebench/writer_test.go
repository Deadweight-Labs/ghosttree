package storebench

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWriterRejectsWhenOperationCapacityIsReserved(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	w := newWriter(QueueConfig{MaxOperations: 1, MaxBytes: 100, MaxBatch: 1}, func(_ context.Context, operations []Operation) []error {
		close(started)
		<-release
		return make([]error, len(operations))
	})
	defer w.Close()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- w.Submit(context.Background(), Operation{ID: "first", Kind: GhostPut, PayloadBytes: 10})
	}()
	<-started
	if err := w.Submit(context.Background(), Operation{ID: "second", Kind: GhostPut, PayloadBytes: 10}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second submit = %v, want ErrQueueFull", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestWriterRejectsWhenByteCapacityIsReserved(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	w := newWriter(QueueConfig{MaxOperations: 2, MaxBytes: 10, MaxBatch: 1}, func(_ context.Context, operations []Operation) []error {
		close(started)
		<-release
		return make([]error, len(operations))
	})
	defer w.Close()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- w.Submit(context.Background(), Operation{ID: "first", Kind: GhostPut, PayloadBytes: 8})
	}()
	<-started
	if err := w.Submit(context.Background(), Operation{ID: "second", Kind: GhostPut, PayloadBytes: 3}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second submit = %v, want ErrQueueFull", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestWriterReturnsExecutorResultOnlyAfterExecution(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	want := errors.New("commit failed")
	w := newWriter(QueueConfig{MaxOperations: 1, MaxBytes: 100, MaxBatch: 1}, func(_ context.Context, operations []Operation) []error {
		close(started)
		<-release
		return []error{want}
	})
	defer w.Close()
	done := make(chan error, 1)
	go func() { done <- w.Submit(context.Background(), Operation{ID: "one", Kind: GhostPut}) }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("submit returned before execution completed: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("submit = %v, want %v", err, want)
	}
}

func TestWriterCoalescesAdjacentChunkOperations(t *testing.T) {
	batches := make(chan []Operation, 1)
	w := newWriter(QueueConfig{MaxOperations: 4, MaxBytes: 1000, MaxBatch: 4, GatherWindow: 20 * time.Millisecond}, func(_ context.Context, operations []Operation) []error {
		batches <- append([]Operation(nil), operations...)
		return make([]error, len(operations))
	})
	defer w.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := w.Submit(context.Background(), Operation{ID: string(rune('a' + n)), Kind: ChunksAppend}); err != nil {
				t.Errorf("submit: %v", err)
			}
		}()
	}
	close(start)
	got := <-batches
	if len(got) != 3 {
		t.Fatalf("batch size = %d, want 3", len(got))
	}
	wg.Wait()
	stats := w.stats()
	if stats.BatchSize.Count == 0 || stats.BatchSize.Max < 3 {
		t.Fatalf("batch size stats = %+v", stats.BatchSize)
	}
}

func TestWriterRecordsAdmissionWaitAndMaximumDepth(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	w := newWriter(QueueConfig{MaxOperations: 3, MaxBytes: 100, MaxBatch: 1}, func(_ context.Context, operations []Operation) []error {
		if operations[0].ID == "first" {
			close(started)
			<-release
		}
		return make([]error, len(operations))
	})
	defer w.Close()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	thirdDone := make(chan error, 1)
	go func() {
		firstDone <- w.Submit(context.Background(), Operation{ID: "first", Kind: GhostPut, PayloadBytes: 10})
	}()
	<-started
	go func() {
		secondDone <- w.Submit(context.Background(), Operation{ID: "second", Kind: GhostPut, PayloadBytes: 20})
	}()
	go func() {
		thirdDone <- w.Submit(context.Background(), Operation{ID: "third", Kind: GhostPut, PayloadBytes: 30})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		w.mu.Lock()
		reserved := w.reservedOperations
		w.mu.Unlock()
		if reserved == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second operation was not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if err := <-thirdDone; err != nil {
		t.Fatal(err)
	}
	stats := w.stats()
	if stats.QueueWait.Count != 3 || stats.QueueWait.MaxNS <= 0 || stats.MaximumDepth != 2 ||
		stats.MaximumBytes != 50 || stats.MaximumOutstanding != 3 || stats.MaximumOutstandingBytes != 60 {
		t.Fatalf("writer stats = %+v", stats)
	}
}

func TestWriterWithZeroGatherWindowExecutesLoneChunkImmediately(t *testing.T) {
	started := make(chan struct{})
	w := newWriter(QueueConfig{MaxOperations: 1, MaxBytes: 100, MaxBatch: 8, GatherWindow: 0}, func(_ context.Context, operations []Operation) []error {
		close(started)
		return make([]error, len(operations))
	})
	defer w.Close()
	done := make(chan error, 1)
	go func() { done <- w.Submit(context.Background(), Operation{ID: "chunk", Kind: ChunksAppend}) }()
	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("zero gather window waited for another submission")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWriterCloseDrainsAcceptedAndRejectsNew(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	w := newWriter(QueueConfig{MaxOperations: 2, MaxBytes: 100, MaxBatch: 1}, func(_ context.Context, operations []Operation) []error {
		close(started)
		<-release
		return make([]error, len(operations))
	})
	done := make(chan error, 1)
	go func() { done <- w.Submit(context.Background(), Operation{ID: "accepted", Kind: GhostPut}) }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := w.Submit(context.Background(), Operation{ID: "late", Kind: GhostPut}); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("late submit = %v, want ErrWriterClosed", err)
	}
}
