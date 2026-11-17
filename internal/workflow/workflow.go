// Package workflow runs the workflows of the server (REQ-693, IDEA-45 stage
// E0). One tick of the Engine writes due schedule events, dispatches new
// events into runs, executes queued runs one after the other and deletes full
// texts past their retention. What a run executes comes from a Resolver: in
// E0 the built-in blocks (ghosttree/echo), from E2 on the stored, versioned
// workflow definitions.
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
	// MaxRuns bounds how many runs one tick executes; 0 means 20.
	MaxRuns int
	// RunTimeout bounds one run; 0 means ten minutes.
	RunTimeout time.Duration
	// PurgeEvery is how often the retention is applied; 0 means hourly.
	PurgeEvery time.Duration
	// AIBaseURL is the address of the model proxy on loopback; empty means
	// runs get no model access.
	AIBaseURL string

	lastPurge time.Time
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

// Tick does one round of work. Without pending events or queued runs it
// only reads.
func (e *Engine) Tick(ctx context.Context) (Report, error) {
	var rep Report
	at := e.now()
	n, err := e.Store.EmitWorkflowSchedules(at)
	if err != nil {
		return rep, fmt.Errorf("schedules: %w", err)
	}
	rep.Schedules = n
	events, _ := e.Store.WorkflowWorkPending()
	if events {
		d, err := e.Store.DispatchWorkflowEvents(200)
		if err != nil {
			return rep, fmt.Errorf("dispatch: %w", err)
		}
		rep.WorkflowDispatchReport = d
	}
	limit := e.MaxRuns
	if limit <= 0 {
		limit = 20
	}
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
		if err := e.Store.FinishWorkflowRun(run.ID, e.execute(ctx, run, ev)); err != nil {
			return rep, fmt.Errorf("finish run %d: %w", run.ID, err)
		}
		rep.Executed++
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

// execute runs one claimed run and never panics: a failing or panicking block
// ends the run as failed.
func (e *Engine) execute(ctx context.Context, run store.WorkflowRun, ev store.WorkflowEvent) (out store.WorkflowRunOutcome) {
	block, version, err := e.Resolver.Resolve(ctx, run)
	if errors.Is(err, ErrUnknownWorkflow) {
		return store.WorkflowRunOutcome{State: store.RunSkipped, Reason: "unknown_workflow"}
	}
	if err != nil {
		return store.WorkflowRunOutcome{State: store.RunFailed, Reason: err.Error()}
	}
	timeout := e.RunTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in := Input{Run: run, Event: ev}
	if e.AIBaseURL != "" {
		token, err := e.Store.IssueAIRunToken(run.ID)
		if err != nil {
			return store.WorkflowRunOutcome{State: store.RunFailed, Reason: "model access: " + err.Error(), WorkflowVersion: version}
		}
		in.AI = &ModelAccess{BaseURL: e.AIBaseURL, Token: token}
		// The token ends with the block, before the run is recorded, so that
		// nothing the block started can call a model afterwards.
		defer func() { _ = e.Store.RevokeAIRunTokens(run.ID) }()
		defer func() { out = withModelUsage(e.Store, run.ID, out) }()
	}
	out, err = safeRun(rctx, block, in)
	if out.WorkflowVersion == "" {
		out.WorkflowVersion = version
	}
	if err != nil {
		out.State, out.Reason = store.RunFailed, err.Error()
	}
	switch out.State {
	case store.RunDone, store.RunFailed, store.RunBudget, store.RunSkipped:
	default:
		out.State, out.Reason = store.RunFailed, "block ended without a state"
	}
	return out
}

// safeRun runs a block; a panic ends the run as failed.
func safeRun(ctx context.Context, block Block, in Input) (out store.WorkflowRunOutcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = store.WorkflowRunOutcome{}, fmt.Errorf("panic: %v", p)
		}
	}()
	return block.Run(ctx, in)
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

// Run ticks every interval until ctx ends. Errors are logged, not fatal: the
// next tick tries again.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if n, err := e.Store.AbandonWorkflowRuns(); err != nil && e.Log != nil {
		e.Log.Error("workflows", "error", err.Error())
	} else if n > 0 && e.Log != nil {
		e.Log.Info("workflows", "interrupted", n)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		rep, err := e.Tick(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if e.Log != nil {
				e.Log.Error("workflows", "error", err.Error())
			}
		case rep.Queued+rep.Skipped+rep.Executed+rep.Purged > 0 && e.Log != nil:
			e.Log.Info("workflows", "events", rep.Events, "queued", rep.Queued, "skipped", rep.Skipped, "executed", rep.Executed, "purged", rep.Purged)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
