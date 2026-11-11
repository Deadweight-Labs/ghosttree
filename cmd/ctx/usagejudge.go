package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/usagejob"
)

// envUsageJudge switches the background judgement of delivered context off
// (REQ-645): "off", "0", "false" or "no". It is on by default; it reads the
// transcripts of finished sessions of the account that owns the delivery rows and
// stores numbers only.
const envUsageJudge = "GHOSTTREE_USAGE_JUDGE"

func usageJudgeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envUsageJudge))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// startUsageJudge starts the job that records which delivered entries the agent
// used. It returns a function that waits for the job to end after ctx is
// canceled.
func startUsageJudge(ctx context.Context, st *store.Store, logger *slog.Logger) func() {
	done := make(chan struct{})
	if !usageJudgeEnabled() {
		logger.Info("usage_judge", "state", "off", "note", envUsageJudge+" switches the judgement of delivered context off")
		close(done)
		return func() { <-done }
	}
	go func() {
		defer close(done)
		usagejob.New(st, usagejob.Config{}, logger).Run(ctx)
	}()
	return func() { <-done }
}
