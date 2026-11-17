package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// workflowTick is how often the server looks for new events and queued runs.
// A merge becomes a run within one tick (REQ-693 promises five minutes).
const workflowTick = 15 * time.Second

// startWorkflows runs the dispatcher and executor of workflows in the
// background of the server, each in a goroutine of its own. A round without
// due schedules, new events or queued runs only reads. The returned function
// waits for them to stop, for a bounded time.
func startWorkflows(ctx context.Context, st *store.Store, logger *slog.Logger) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		e := &workflow.Engine{Store: st, Resolver: workflow.DefaultBuiltins(), Log: logger}
		e.Run(ctx, workflowTick)
	}()
	return func() { <-done }
}
