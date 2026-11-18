package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Findings of the review of E0 (REQ-693, PR 280).

func trigger(t *testing.T, st *store.Store, name string) {
	t.Helper()
	if _, err := st.SetWorkflowTrigger("person:1", store.WorkflowTrigger{Project: project, Workflow: name, EventKind: store.EventPRMerged, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func runStates(t *testing.T, st *store.Store, pr string) map[string]string {
	t.Helper()
	runs, err := st.WorkflowRunsIn(project, store.WorkflowRunFilter{SubjectKind: "pr", SubjectID: pr})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range runs {
		out[r.Workflow] = r.State + " " + r.Reason
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

// Finding 2: a merge becomes a queued run within the next dispatcher tick
// even while a long run is running. Before the dispatcher and the executor
// were apart, the second merge waited until the long run had ended.
func TestAMergeBecomesAQueuedRunWhileALongRunIsRunning(t *testing.T) {
	st := engineStore(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	blocks := Builtins{
		"test/slow": BlockFunc(func(ctx context.Context, _ Input) (store.WorkflowRunOutcome, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return store.WorkflowRunOutcome{State: store.RunDone, Result: "slow done"}, nil
		}),
	}
	trigger(t, st, "test/slow")
	e := &Engine{Store: st, Resolver: blocks}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx, 20*time.Millisecond) }()
	defer func() { cancel(); <-done }()

	merged(t, st, "1")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow run never started")
	}
	// The slow run is running now; a merge arrives.
	merged(t, st, "2")
	waitFor(t, "the second merge to become a queued run", func() bool {
		return runStates(t, st, "2")["test/slow"] == "queued "
	})
	if got := runStates(t, st, "1")["test/slow"]; got != "running " {
		t.Fatalf("the first run should still be running: %q", got)
	}
	// Let the first run go: the executor then takes the queued one.
	release <- struct{}{}
	close(release)
	waitFor(t, "both runs to finish", func() bool {
		return runStates(t, st, "1")["test/slow"] == "done " && runStates(t, st, "2")["test/slow"] == "done "
	})
}

// Finding 4: a stop of the server in the middle of a run ends the run as
// interrupted instead of leaving it running.
func TestStoppingTheServerInTheMiddleOfARunEndsItInterrupted(t *testing.T) {
	st := engineStore(t)
	started := make(chan struct{}, 1)
	blocks := Builtins{"test/waits": BlockFunc(func(ctx context.Context, _ Input) (store.WorkflowRunOutcome, error) {
		started <- struct{}{}
		<-ctx.Done()
		return store.WorkflowRunOutcome{CostUSD: 0.25, Inputs: "partial"}, ctx.Err()
	})}
	trigger(t, st, "test/waits")
	merged(t, st, "1")
	e := &Engine{Store: st, Resolver: blocks}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx, 20*time.Millisecond) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	runs, _ := st.WorkflowRunsIn(project, store.WorkflowRunFilter{})
	if len(runs) != 1 || runs[0].State != store.RunFailed || runs[0].Reason != "interrupted" || runs[0].CostUSD != 0.25 {
		t.Fatalf("the cut-off run keeps its cost and says why: %+v", runs)
	}
}

// Finding 4: a block that ignores its context neither holds the run forever
// (timeout) nor the shutdown of the server.
func TestABlockThatIgnoresItsContextEndsAfterTheGrace(t *testing.T) {
	st := engineStore(t)
	stuck := make(chan struct{})
	defer close(stuck)
	blocks := Builtins{"test/stuck": BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) {
		<-stuck
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})}
	trigger(t, st, "test/stuck")
	merged(t, st, "1")
	e := &Engine{Store: st, Resolver: blocks, RunTimeout: 50 * time.Millisecond, StopGrace: 50 * time.Millisecond}
	if rep, err := e.Tick(context.Background()); err != nil || rep.Executed != 1 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	if got := runStates(t, st, "1")["test/stuck"]; got != "failed timeout" {
		t.Fatalf("run of a block that ignores its context: %q", got)
	}

	// At shutdown, Run returns after the grace even though the block is stuck.
	merged(t, st, "2")
	started := make(chan struct{}, 1)
	blocks["test/stuck"] = BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) {
		started <- struct{}{}
		<-stuck
		return store.WorkflowRunOutcome{State: store.RunDone}, nil
	})
	e = &Engine{Store: st, Resolver: blocks, StopGrace: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx, 20*time.Millisecond) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown waits for a block that ignores its context")
	}
	waitFor(t, "the cut-off run to be recorded", func() bool { return runStates(t, st, "2")["test/stuck"] == "failed interrupted" })
}

type panickingResolver struct{}

func (panickingResolver) Resolve(context.Context, store.WorkflowRun) (Block, string, error) {
	panic("resolver boom")
}

// Finding 4: a panic in the resolver ends the run, not the engine, and a
// panic anywhere in a round ends that round with an error.
func TestAPanickingResolverEndsTheRunNotTheEngine(t *testing.T) {
	st := engineStore(t)
	trigger(t, st, EchoName)
	merged(t, st, "1")
	e := &Engine{Store: st, Resolver: panickingResolver{}}
	if rep, err := e.Tick(context.Background()); err != nil || rep.Executed != 1 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	if got := runStates(t, st, "1")[EchoName]; got != "failed panic: resolver boom" {
		t.Fatalf("run: %q", got)
	}
	if _, err := (&Engine{}).Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("a round that panics returns an error: %v", err)
	}
}

// Finding 4: a run that cannot be finished (it was ended by the sweep in the
// meantime) is logged and does not hold back the runs behind it.
func TestARunThatCannotBeFinishedDoesNotHoldBackTheOthers(t *testing.T) {
	st := engineStore(t)
	blocks := Builtins{
		"a/sweeps-itself": BlockFunc(func(context.Context, Input) (store.WorkflowRunOutcome, error) {
			// Another process's sweep ends this run while it is still running.
			if _, err := st.AbandonWorkflowRuns(); err != nil {
				return store.WorkflowRunOutcome{}, err
			}
			return store.WorkflowRunOutcome{State: store.RunDone}, nil
		}),
		EchoName: Echo,
	}
	trigger(t, st, "a/sweeps-itself")
	trigger(t, st, EchoName)
	merged(t, st, "1")
	e := &Engine{Store: st, Resolver: blocks}
	rep, err := e.Tick(context.Background())
	if err == nil || !errors.Is(err, store.ErrWorkflowRun) {
		t.Fatalf("the failed finish is reported: %v", err)
	}
	if rep.Executed != 2 {
		t.Fatalf("the second run is still executed: %+v", rep)
	}
	got := runStates(t, st, "1")
	if got["a/sweeps-itself"] != "failed interrupted" || got[EchoName] != "done " {
		t.Fatalf("runs: %v", got)
	}
}

// Finding 4: a writer that fails gets a second and third attempt.
func TestFinishIsTriedAgainWhenTheWriterFails(t *testing.T) {
	old := finishBackoff
	finishBackoff = 20 * time.Millisecond
	defer func() { finishBackoff = old }()
	st := engineStore(t)
	st.Close()
	e := &Engine{Store: st}
	start := time.Now()
	err := e.finish(1, store.WorkflowRunOutcome{State: store.RunDone})
	if err == nil {
		t.Fatal("finishing on a closed store")
	}
	if took := time.Since(start); took < 55*time.Millisecond {
		t.Fatalf("no retries: failed after %v", took)
	}
}

// Finding 7: a round with nothing due does nothing (and, with the schedule
// check read-only, goes through the writer for nothing).
func TestAnIdleDispatchDoesNothing(t *testing.T) {
	st := engineStore(t)
	e := &Engine{Store: st, Resolver: DefaultBuiltins(), PurgeEvery: 24 * time.Hour}
	if _, err := e.Dispatch(context.Background()); err != nil { // the first round purges once
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if rep, err := e.Dispatch(context.Background()); err != nil || rep != (Report{}) {
			t.Fatalf("idle round: %+v %v", rep, err)
		}
	}
}
