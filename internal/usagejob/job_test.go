package usagejob

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeStore struct {
	mu    sync.Mutex
	calls []store.JudgeOptions
	times []time.Time
	reply func(n int) (store.JudgeResult, error)
}

func (f *fakeStore) JudgeDeliveries(ctx context.Context, opt store.JudgeOptions) (store.JudgeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opt)
	f.times = append(f.times, time.Now())
	n := len(f.calls)
	f.mu.Unlock()
	return f.reply(n)
}

func (f *fakeStore) snapshot() ([]store.JudgeOptions, []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.JudgeOptions{}, f.calls...), append([]time.Time{}, f.times...)
}

func run(t *testing.T, f *fakeStore, cfg Config) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); New(f, cfg, nil).Run(ctx) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the job did not stop after its context was canceled")
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// The job works step after step while more is due, with a pause between steps,
// and hands the cap per step to the store.
func TestJobStepsWhileMoreIsDueThenWaitsIdle(t *testing.T) {
	f := &fakeStore{reply: func(n int) (store.JudgeResult, error) {
		return store.JudgeResult{Sessions: 1, Rows: 2, More: n < 3}, nil
	}}
	stop := run(t, f, Config{StartDelay: -1, Pause: 5 * time.Millisecond, Idle: time.Hour, Sessions: 7})
	defer stop()
	waitFor(t, "three steps", func() bool { c, _ := f.snapshot(); return len(c) >= 3 })
	time.Sleep(30 * time.Millisecond)
	calls, times := f.snapshot()
	if len(calls) != 3 {
		t.Fatalf("%d steps; after the last one nothing was due and the job should idle", len(calls))
	}
	for _, c := range calls {
		if c.MaxSessions != 7 {
			t.Errorf("options %+v", c)
		}
	}
	if times[1].Sub(times[0]) < 5*time.Millisecond || times[2].Sub(times[1]) < 5*time.Millisecond {
		t.Errorf("steps ran without the pause: %v", times)
	}
}

// After a failure the job waits longer each time, and recovers when a step
// succeeds.
func TestJobBacksOffAfterErrorsAndRecovers(t *testing.T) {
	f := &fakeStore{reply: func(n int) (store.JudgeResult, error) {
		if n <= 3 {
			return store.JudgeResult{}, errors.New("database is locked")
		}
		return store.JudgeResult{}, nil
	}}
	stop := run(t, f, Config{StartDelay: -1, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 25 * time.Millisecond, Idle: time.Hour})
	defer stop()
	waitFor(t, "four steps", func() bool { c, _ := f.snapshot(); return len(c) >= 4 })
	_, times := f.snapshot()
	gaps := []time.Duration{times[1].Sub(times[0]), times[2].Sub(times[1]), times[3].Sub(times[2])}
	if gaps[0] < 10*time.Millisecond || gaps[1] < 20*time.Millisecond || gaps[2] < 25*time.Millisecond {
		t.Errorf("backoff gaps %v, want at least 10ms, 20ms, 25ms (capped)", gaps)
	}
	time.Sleep(40 * time.Millisecond)
	if c, _ := f.snapshot(); len(c) != 4 {
		t.Errorf("%d steps after recovery, the job should be idle", len(c))
	}
}

// The first step waits for the start delay; a cancel before it means no step.
func TestJobStartsLateAndStopsPromptly(t *testing.T) {
	f := &fakeStore{reply: func(int) (store.JudgeResult, error) { return store.JudgeResult{}, nil }}
	stop := run(t, f, Config{StartDelay: time.Hour})
	time.Sleep(20 * time.Millisecond)
	stop()
	if c, _ := f.snapshot(); len(c) != 0 {
		t.Errorf("%d steps before the start delay", len(c))
	}
}

// A step that is running when the context is canceled ends the job without a
// further step, and the step's context carries the cancel.
func TestJobStopsInsideAStep(t *testing.T) {
	started := make(chan struct{})
	g := &blockingStore{started: started}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); New(g, Config{StartDelay: -1}, nil).Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not stop while a step was running")
	}
}

type blockingStore struct{ started chan struct{} }

func (b *blockingStore) JudgeDeliveries(ctx context.Context, _ store.JudgeOptions) (store.JudgeResult, error) {
	close(b.started)
	<-ctx.Done()
	return store.JudgeResult{}, ctx.Err()
}

// The step gets a time budget of half its timeout, so a slow session ends the
// step cleanly (progress written) before the timeout cuts it; a step that
// yielded because the searches were busy is not a failure and takes the short
// pause, not the back-off.
func TestJobGivesTheStepABudgetAndDoesNotBackOffWhenYielding(t *testing.T) {
	f := &fakeStore{reply: func(n int) (store.JudgeResult, error) {
		if n < 3 {
			return store.JudgeResult{Yielded: true, More: true}, nil
		}
		return store.JudgeResult{}, nil
	}}
	stop := run(t, f, Config{StartDelay: -1, Pause: 5 * time.Millisecond, Idle: time.Hour,
		InitialBackoff: time.Hour, StepTimeout: 40 * time.Second})
	defer stop()
	waitFor(t, "three steps", func() bool { c, _ := f.snapshot(); return len(c) >= 3 })
	calls, _ := f.snapshot()
	for _, c := range calls {
		if c.Budget != 20*time.Second {
			t.Errorf("budget = %v, want half of the 40s timeout", c.Budget)
		}
	}
}
