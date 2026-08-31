package storebench

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	execute func(context.Context, Operation) error
	verify  func(context.Context, Expected) error
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Execute(ctx context.Context, operation Operation) error {
	return f.execute(ctx, operation)
}
func (f *fakeBackend) Verify(ctx context.Context, expected Expected) error {
	if f.verify != nil {
		return f.verify(ctx, expected)
	}
	return nil
}
func (f *fakeBackend) Stats() BackendStats { return BackendStats{} }
func (f *fakeBackend) Close() error        { return nil }

func TestRunHonorsConcurrencyLimit(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{}, 4)
	backend := &fakeBackend{execute: func(_ context.Context, operation Operation) error {
		started <- operation.ID
		<-release
		return nil
	}}
	workload := Workload{Operations: []Operation{
		{ID: "a", Kind: GhostPut}, {ID: "b", Kind: GhostPut},
		{ID: "c", Kind: GhostPut}, {ID: "d", Kind: GhostPut},
	}}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), backend, workload, Expected{}, RunConfig{Concurrency: 2, ArrivalMultiplier: 1, RunID: "limit"})
		done <- err
	}()
	<-started
	<-started
	select {
	case id := <-started:
		t.Fatalf("third operation %s started above concurrency limit", id)
	default:
	}
	release <- struct{}{}
	release <- struct{}{}
	<-started
	<-started
	release <- struct{}{}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunStartsDependentOperationOnlyAfterPredecessorCompletes(t *testing.T) {
	started := make(chan string, 3)
	releaseA := make(chan struct{})
	backend := &fakeBackend{execute: func(_ context.Context, operation Operation) error {
		started <- operation.ID
		if operation.ID == "a" {
			<-releaseA
		}
		return nil
	}}
	workload := Workload{Operations: []Operation{
		{ID: "a", Kind: GhostPut},
		{ID: "b", Kind: GhostRead, DependsOn: []string{"a"}},
		{ID: "c", Kind: GhostPut},
	}}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), backend, workload, Expected{}, RunConfig{Concurrency: 2, ArrivalMultiplier: 1, RunID: "dependencies"})
		done <- err
	}()
	first, second := <-started, <-started
	if first == "b" || second == "b" {
		t.Fatalf("dependent b started before a completed: %s,%s", first, second)
	}
	select {
	case id := <-started:
		t.Fatalf("operation %s started while a remained blocked", id)
	default:
	}
	close(releaseA)
	if id := <-started; id != "b" {
		t.Fatalf("next operation = %s, want b", id)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunCancelsWaitingWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	backend := &fakeBackend{execute: func(ctx context.Context, _ Operation) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, backend, Workload{Operations: []Operation{{ID: "a", Kind: GhostPut}}}, Expected{},
			RunConfig{Concurrency: 1, ArrivalMultiplier: 1, RunID: "cancel"})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled run succeeded")
	}
}

func TestNearestRankPercentiles(t *testing.T) {
	samples := []int64{9, 1, 5, 3, 7, 2, 4, 6, 8, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	got := summarizeDurations(samples)
	if got.Count != 20 || got.P50NS != 10 || got.P95NS != 19 || got.P99NS != 20 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestRunNeverExceedsLimitUnderRepeatedScheduling(t *testing.T) {
	var mu sync.Mutex
	active, maximum := 0, 0
	backend := &fakeBackend{execute: func(_ context.Context, _ Operation) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}}
	operations := make([]Operation, 100)
	for i := range operations {
		operations[i] = Operation{ID: string(rune(i + 1)), Kind: GhostPut}
	}
	if _, err := Run(context.Background(), backend, Workload{Operations: operations}, Expected{},
		RunConfig{Concurrency: 4, ArrivalMultiplier: 1, RunID: "stress"}); err != nil {
		t.Fatal(err)
	}
	if maximum > 4 {
		t.Fatalf("maximum concurrency = %d", maximum)
	}
}
