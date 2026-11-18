package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
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
func startWorkflows(ctx context.Context, st *store.Store, logger *slog.Logger, ai aiRuntime) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		resolver := workflow.NewDefResolver(st)
		// A pull request's diff comes from the GitHub App where it is set up
		// (REQ-696); without it the changed files stand in.
		if app := forge.NewGitHubApp(forge.AppConfigFromOS()); app.Configured() {
			resolver.PRDiff = func(ctx context.Context, project string, pr int64) (string, error) {
				inst, ok := st.ProjectInstallation(project)
				if !ok {
					return "", forge.ErrPermissionMissing
				}
				return app.PullRequestDiff(ctx, inst, project, pr, 80_000)
			}
		}
		// Research on the net only where the operator named a search
		// service (REQ-698); without it no workflow searches.
		if web, err := workflow.ResearchFromEnv(); err != nil {
			logger.Error("workflows", "research", err.Error())
		} else if web != nil {
			resolver.Web, resolver.Research = web, web.Researcher()
		}
		e := &workflow.Engine{Store: st, Resolver: resolver, Log: logger, AIBaseURL: ai.BaseURL, AIEndRun: ai.EndRun}
		e.Run(ctx, workflowTick)
	}()
	return func() { <-done }
}
