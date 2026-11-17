// Package workflow runs the workflows of the server (REQ-693, IDEA-45 stage
// E0). The Engine has two halves that Run drives in goroutines of their own:
// the dispatcher writes due schedule events, turns new events into runs and
// deletes full texts past their retention; the executor takes queued runs one
// after the other. A long run therefore never holds back the dispatcher: a
// merge becomes a queued run within the next dispatcher tick. What a run
// executes comes from a Resolver: in E0 the built-in blocks (ghosttree/echo),
// from E2 on the stored, versioned workflow definitions.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Input is what a block gets for one run: the run, the event that caused it
// and, when the server runs its model proxy, the way to call a model on the
// run's behalf (REQ-694). AI carries a token, never a provider key.
type Input struct {
	Run   store.WorkflowRun
	Event store.WorkflowEvent
	AI    *ModelAccess
	// Def is the definition of a stored workflow (REQ-695), nil for a block
	// that a trigger names directly; Values are its declared inputs.
	Def    *Def
	Values map[string]string
}

// Block is one thing a workflow can do. It reports how the run ended; the
// engine stores it. An error ends the run as failed with the error as reason.
type Block interface {
	Run(ctx context.Context, in Input) (store.WorkflowRunOutcome, error)
}

// BlockFunc makes a function a Block.
type BlockFunc func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error)

func (f BlockFunc) Run(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
	return f(ctx, in)
}

// ErrUnknownWorkflow: the resolver knows no workflow of that name. The run is
// skipped with the reason unknown_workflow.
var ErrUnknownWorkflow = errors.New("unknown workflow")

// Resolver finds what a run executes and in which version.
type Resolver interface {
	Resolve(ctx context.Context, run store.WorkflowRun) (Block, string, error)
}

// Builtins resolves a workflow by name to a block shipped in the binary. The
// version of a built-in block is the "@v1" in its name, else "builtin".
type Builtins map[string]Block

func (b Builtins) Resolve(_ context.Context, run store.WorkflowRun) (Block, string, error) {
	block, ok := b[run.Workflow]
	if !ok {
		return nil, "", ErrUnknownWorkflow
	}
	version := "builtin"
	if _, v, ok := strings.Cut(run.Workflow, "@"); ok {
		version = v
	}
	return block, version, nil
}

// EchoName is the built-in test block: it needs no model, costs nothing and
// echoes its event. With it the whole path from event to run page can be
// tested before stages E1 and E2 exist.
const EchoName = "ghosttree/echo"

// Echo is the block behind EchoName.
var Echo = BlockFunc(func(_ context.Context, in Input) (store.WorkflowRunOutcome, error) {
	inputs, _ := json.MarshalIndent(in.Event, "", "  ")
	result := in.Event.Kind
	switch in.Event.SubjectKind {
	case "pr":
		result += " PR #" + in.Event.SubjectID
	case "idea":
		result += " IDEA-" + in.Event.SubjectID
	case "schedule":
		result += " " + in.Event.Data["slot"]
	}
	return store.WorkflowRunOutcome{State: store.RunDone, Result: result, Inputs: string(inputs), Output: result}, nil
})

// DefaultBuiltins are the blocks every server has.
func DefaultBuiltins() Builtins { return Builtins{EchoName: Echo, AskName: Ask} }

// Engine dispatches events and executes runs.
type Engine struct {
	Store    *store.Store
	Resolver Resolver
	Log      *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// MaxRuns bounds how many runs one Execute call takes; 0 means 20.
	MaxRuns int
	// RunTimeout bounds one run; 0 means ten minutes.
	RunTimeout time.Duration
	// PurgeEvery is how often the retention is applied; 0 means hourly.
	PurgeEvery time.Duration
	// AIBaseURL is the address of the model proxy on loopback; empty means
	// runs get no model access.
	AIBaseURL string
	// StopGrace is how long a run that its context has cut off may take to
	// return before the engine ends it without its answer; 0 means ten seconds.
	// It is also how long Run waits for the executor at shutdown.
	StopGrace time.Duration

	lastPurge time.Time
	// wake tells the executor that the dispatcher queued runs; set by Run.
	wake chan struct{}
}

// Report counts what one tick did.
type Report struct {
	Schedules int
	store.WorkflowDispatchReport
	Executed int
	Purged   int
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) runTimeout() time.Duration {
	if e.RunTimeout > 0 {
		return e.RunTimeout
	}
	return 10 * time.Minute
}

func (e *Engine) stopGrace() time.Duration {
	if e.StopGrace > 0 {
		return e.StopGrace
	}
	return 10 * time.Second
}

func (e *Engine) maxRuns() int {
	if e.MaxRuns > 0 {
		return e.MaxRuns
	}
	return 20
}

// Tick does one round of both halves, dispatching first and then executing.
// Without pending events, queued runs or due schedules it only reads.
func (e *Engine) Tick(ctx context.Context) (Report, error) {
	rep, err := e.Dispatch(ctx)
	if err != nil {
		return rep, err
	}
	ex, err := e.Execute(ctx)
	rep.Executed = ex.Executed
	return rep, err
}

// Dispatch is the dispatcher's round: schedules that are due become events,
// new events become queued (or skipped) runs, runs cut off long ago are ended
// as interrupted, and full texts past their retention are deleted. It never
// executes a run, so it takes as long as its writes and no longer. A panic
// ends the round with an error.
func (e *Engine) Dispatch(ctx context.Context) (rep Report, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("dispatch panic: %v", p)
		}
	}()
	at := e.now()
	if _, err := e.sweep(at); err != nil {
		return rep, fmt.Errorf("abandon: %w", err)
	}
	if e.Store.WorkflowSchedulesDue(at) {
		n, err := e.Store.EmitWorkflowSchedules(at)
		if err != nil {
			return rep, fmt.Errorf("schedules: %w", err)
		}
		rep.Schedules = n
	}
	if events, _ := e.Store.WorkflowWorkPending(); events {
		d, err := e.Store.DispatchWorkflowEvents(200)
		if err != nil {
			return rep, fmt.Errorf("dispatch: %w", err)
		}
		rep.WorkflowDispatchReport = d
		if d.Queued > 0 && e.wake != nil {
			select {
			case e.wake <- struct{}{}:
			default:
			}
		}
	}
	every := e.PurgeEvery
	if every <= 0 {
		every = time.Hour
	}
	if e.lastPurge.IsZero() || at.Sub(e.lastPurge) >= every {
		n, err := e.Store.PurgeWorkflowRunTexts(at)
		if err != nil {
			return rep, fmt.Errorf("purge: %w", err)
		}
		rep.Purged, e.lastPurge = n, at
	}
	return rep, nil
}

// sweep ends the runs that began before a run could still be running: longer
// ago than the run timeout, the grace and a minute. Runs of another process
// that are still within their time stay.
func (e *Engine) sweep(at time.Time) (int, error) {
	cut := at.Add(-e.runTimeout() - e.stopGrace() - time.Minute)
	if e.Store.StaleWorkflowRuns(cut) == 0 {
		return 0, nil
	}
	return e.Store.AbandonStaleWorkflowRuns(cut)
}

// Execute is the executor's round: it takes queued runs one after the other,
// up to MaxRuns, and ends each with the outcome of its block. A run that
// cannot be recorded (the writer fails three times in a row) is logged and
// left to the sweep of interrupted runs; it does not hold back the others. A
// panic ends the round with an error.
func (e *Engine) Execute(ctx context.Context) (rep Report, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("execute panic: %v", p)
		}
	}()
	limit := e.maxRuns()
	var firstErr error
	for rep.Executed < limit && ctx.Err() == nil {
		if _, runs := e.Store.WorkflowWorkPending(); !runs {
			break
		}
		run, ev, ok, err := e.Store.ClaimWorkflowRun()
		if err != nil {
			return rep, fmt.Errorf("claim: %w", err)
		}
		if !ok {
			break
		}
		if err := e.finish(run.ID, e.execute(ctx, run, ev)); err != nil {
			if e.Log != nil {
				e.Log.Error("workflows", "finish_run", run.ID, "error", err.Error())
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("finish run %d: %w", run.ID, err)
			}
		}
		rep.Executed++
	}
	return rep, firstErr
}

// finishBackoff is the pause before a second and third attempt to record how a
// run ended. A variable so that tests can shorten it.
var finishBackoff = 200 * time.Millisecond

// finish records the outcome. A busy or failing writer gets three attempts;
// a run that is not running any more (the sweep ended it) is not retried.
func (e *Engine) finish(id int64, out store.WorkflowRunOutcome) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * finishBackoff)
		}
		if err = e.Store.FinishWorkflowRun(id, out); err == nil || errors.Is(err, store.ErrWorkflowRun) {
			return err
		}
	}
	return err
}

// blockResult is what a block goroutine hands back.
type blockResult struct {
	out store.WorkflowRunOutcome
	err error
}

// execute runs one claimed run and never panics: a failing or panicking block,
// and a resolver that panics, end the run as failed. A block that does not
// return when its context ends is not waited for beyond StopGrace: the run
// ends as failed with the reason timeout (or interrupted, when the server is
// stopping) and the block's goroutine is left to finish by itself.
func (e *Engine) execute(ctx context.Context, run store.WorkflowRun, ev store.WorkflowEvent) (out store.WorkflowRunOutcome) {
	var version string
	defer func() {
		if p := recover(); p != nil {
			out = store.WorkflowRunOutcome{State: store.RunFailed, Reason: fmt.Sprintf("panic: %v", p), WorkflowVersion: version}
		}
	}()
	block, version, err := e.Resolver.Resolve(ctx, run)
	if errors.Is(err, ErrUnknownWorkflow) {
		return store.WorkflowRunOutcome{State: store.RunSkipped, Reason: "unknown_workflow"}
	}
	var skip skipError
	if errors.As(err, &skip) {
		return store.WorkflowRunOutcome{State: store.RunSkipped, Reason: skip.reason, WorkflowVersion: version}
	}
	if err != nil {
		return store.WorkflowRunOutcome{State: store.RunFailed, Reason: err.Error()}
	}
	rctx, cancel := context.WithTimeout(ctx, e.runTimeout())
	defer cancel()
	in := Input{Run: run, Event: ev}
	if e.AIBaseURL != "" {
		token, err := e.Store.IssueAIRunToken(run.ID)
		if err != nil {
			return store.WorkflowRunOutcome{State: store.RunFailed, Reason: "model access: " + err.Error(), WorkflowVersion: version}
		}
		in.AI = &ModelAccess{BaseURL: e.AIBaseURL, Token: token}
		// The token ends with execute, before the run is recorded, so that
		// nothing the block started (even a block still running after the
		// grace) can call a model afterwards.
		defer func() { _ = e.Store.RevokeAIRunTokens(run.ID) }()
		defer func() { out = withModelUsage(e.Store, run.ID, out) }()
	}
	done := make(chan blockResult, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- blockResult{err: fmt.Errorf("panic: %v", p)}
			}
		}()
		o, err := block.Run(rctx, in)
		done <- blockResult{o, err}
	}()
	var res blockResult
	select {
	case res = <-done:
	case <-rctx.Done():
		select {
		case res = <-done:
		case <-time.After(e.stopGrace()):
			res = blockResult{err: errors.New(stopReason(ctx))}
		}
	}
	out = res.out
	if out.WorkflowVersion == "" {
		out.WorkflowVersion = version
	}
	if res.err != nil {
		out.State, out.Reason = store.RunFailed, res.err.Error()
		if rctx.Err() != nil && (errors.Is(res.err, context.Canceled) || errors.Is(res.err, context.DeadlineExceeded)) {
			out.Reason = stopReason(ctx)
		}
	}
	switch out.State {
	case store.RunDone, store.RunFailed, store.RunBudget, store.RunSkipped:
	default:
		out.State, out.Reason = store.RunFailed, "block ended without a state"
	}
	return out
}

// withModelUsage puts what the proxy booked on the run into its outcome: the
// connections and models it called, its cost, and the cap it reached, which
// ends it in the state budget whatever the block said.
func withModelUsage(st *store.Store, runID int64, out store.WorkflowRunOutcome) store.WorkflowRunOutcome {
	u, err := st.AIRunUsageOf(runID)
	if err != nil {
		return out
	}
	if u.Calls > 0 {
		out.Connection, out.Model = strings.Join(u.Connections, ", "), strings.Join(u.Models, ", ")
		out.CostUSD = u.CostUSD
	}
	if u.Cap != "" {
		out.State, out.Reason = store.RunBudget, u.Cap
	}
	return out
}

// stopReason says why a run's context ended: the server is stopping
// (interrupted) or the run took longer than RunTimeout (timeout).
func stopReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return "interrupted"
	}
	return "timeout"
}

// Run drives both halves until ctx ends: the dispatcher in the calling
// goroutine, the executor in one of its own, so that a run that takes minutes
// does not hold back events, schedules or the purge. Errors are logged, not
// fatal: the next round tries again. After ctx ends, Run waits for the
// executor at most StopGrace.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if n, err := e.sweep(e.now()); err != nil && e.Log != nil {
		e.Log.Error("workflows", "error", err.Error())
	} else if n > 0 && e.Log != nil {
		e.Log.Info("workflows", "interrupted", n)
	}
	e.wake = make(chan struct{}, 1)
	executorDone := make(chan struct{})
	go func() {
		defer close(executorDone)
		e.loop(ctx, interval, e.wake, e.Execute)
	}()
	e.loop(ctx, interval, nil, e.Dispatch)
	select {
	case <-executorDone:
	case <-time.After(e.stopGrace()):
		if e.Log != nil {
			e.Log.Error("workflows", "error", "executor did not stop in time")
		}
	}
}

// loop calls round every interval (and on wake) until ctx ends and logs what
// it did. An executor round that used its whole MaxRuns goes on at once.
func (e *Engine) loop(ctx context.Context, interval time.Duration, wake <-chan struct{}, round func(context.Context) (Report, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		rep, err := round(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if e.Log != nil {
				e.Log.Error("workflows", "error", err.Error())
			}
		case rep.Queued+rep.Skipped+rep.Executed+rep.Purged > 0 && e.Log != nil:
			e.Log.Info("workflows", "events", rep.Events, "queued", rep.Queued, "skipped", rep.Skipped, "executed", rep.Executed, "purged", rep.Purged)
		}
		if err == nil && rep.Executed >= e.maxRuns() && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}
