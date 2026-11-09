package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// orgMirrorEvery is how often the server takes over the names and pictures of
// the connected GitHub organizations. GitHub sends no webhook when an
// organization changes its name or picture, so the server asks.
const orgMirrorEvery = 24 * time.Hour

// startOrgMirror runs the daily mirroring of connected GitHub organizations
// (REQ-623) until ctx ends. Without a configured GitHub App it does nothing.
// The returned function waits for the loop to finish.
func startOrgMirror(ctx context.Context, st *store.Store, profiler forge.OrgProfiler, configured bool, logger *slog.Logger) func() {
	done := make(chan struct{})
	if !configured || profiler == nil {
		close(done)
		return func() { <-done }
	}
	go func() {
		defer close(done)
		wait := time.Minute
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = orgMirrorEvery
			mirrorOrgsOnce(ctx, st, profiler, logger)
		}
	}()
	return func() { <-done }
}

// mirrorOrgsOnce asks GitHub once for every connected organization.
func mirrorOrgsOnce(ctx context.Context, st *store.Store, profiler forge.OrgProfiler, logger *slog.Logger) {
	list, err := st.GitHubInstallations()
	if err != nil {
		logger.Error("org_mirror", "error", err.Error())
		return
	}
	for _, g := range list {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		p, err := profiler.OrgProfile(callCtx, g.InstallationID, g.Login)
		cancel()
		if err != nil {
			logger.Warn("org_mirror", "org", g.OrgID, "error", err.Error())
			continue
		}
		if _, err := st.MirrorGitHubOrg(g.OrgID, g.Login, p.Login, p.Name); err != nil {
			logger.Warn("org_mirror", "org", g.OrgID, "error", err.Error())
		}
	}
}
