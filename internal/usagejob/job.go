// Package usagejob works through the delivery log in the background and records
// which delivered entries the agent used afterwards (REQ-645, P3 of the concept
// "Schlankes Profil und Kontext, der hilft").
//
// The job never holds anything up. It starts late, takes a few sessions per
// step, pauses between steps and waits longer when nothing is due or the store
// reports an error. Each step reads through the store's read pool and holds the
// write lock only for the short write at its end (store.JudgeDeliveries). All
// its state is in the database: a row is judged once, so a restart, or two
// servers by mistake, never count a delivery twice.
package usagejob

import (
	"context"
	"log/slog"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Store is the part of the store the job uses.
type Store interface {
	JudgeDeliveries(ctx context.Context, opt store.JudgeOptions) (store.JudgeResult, error)
}

// Config sets the pace. Zero values mean the defaults.
type Config struct {
	// StartDelay is the wait before the first step, so a starting server is not
	// slowed by it. Default 2m; a negative value means no wait.
	StartDelay time.Duration
	// Pause is the wait between two steps while more is due. Default 2s.
	Pause time.Duration
	// Idle is the wait when nothing was due. Default 5m.
	Idle time.Duration
	// InitialBackoff is the wait after a failed step; it doubles with every
	// further failure up to MaxBackoff. Defaults 10s and 10m.
	InitialBackoff, MaxBackoff time.Duration
	// StepTimeout bounds one step. Default 1m.
	StepTimeout time.Duration
	// Sessions is the most sessions one step reads. Default store.DefaultJudgeSessions.
	Sessions int
}

// Defaults of the pace.
const (
	DefaultStartDelay     = 2 * time.Minute
	DefaultPause          = 2 * time.Second
	DefaultIdle           = 5 * time.Minute
	DefaultInitialBackoff = 10 * time.Second
	DefaultMaxBackoff     = 10 * time.Minute
	DefaultStepTimeout    = time.Minute
)

func orDefault(v, d time.Duration) time.Duration {
	if v <= 0 {
		return d
	}
	return v
}

// Job is the background worker.
type Job struct {
	st  Store
	cfg Config
	log *slog.Logger
}

// New returns a job; Run starts it.
func New(st Store, cfg Config, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Job{st: st, cfg: cfg, log: logger}
}

// Run works until ctx is canceled and returns then.
func (j *Job) Run(ctx context.Context) {
	if !sleep(ctx, startDelay(j.cfg.StartDelay)) {
		return
	}
	pause := orDefault(j.cfg.Pause, DefaultPause)
	idle := orDefault(j.cfg.Idle, DefaultIdle)
	backoff := orDefault(j.cfg.InitialBackoff, DefaultInitialBackoff)
	maxBackoff := orDefault(j.cfg.MaxBackoff, DefaultMaxBackoff)
	wait := time.Duration(0)
	failing := backoff
	for {
		if !sleep(ctx, wait) {
			return
		}
		res, err := j.step(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			j.log.Error("usage_judge", "error", err.Error(), "retry_in", failing.String())
			wait = failing
			failing = min(failing*2, maxBackoff)
		case res.More:
			failing = backoff
			wait = pause
		default:
			failing = backoff
			wait = idle
		}
	}
}

func (j *Job) step(ctx context.Context) (store.JudgeResult, error) {
	sctx, cancel := context.WithTimeout(ctx, orDefault(j.cfg.StepTimeout, DefaultStepTimeout))
	defer cancel()
	started := time.Now()
	res, err := j.st.JudgeDeliveries(sctx, store.JudgeOptions{MaxSessions: j.cfg.Sessions})
	if err == nil && (res.Rows > 0 || res.GaveUp > 0) {
		j.log.Info("usage_judge", "sessions", res.Sessions, "rows", res.Rows, "picked_up", res.PickedUp,
			"followed", res.Followed, "gave_up", res.GaveUp, "more", res.More, "ms", time.Since(started).Milliseconds())
	}
	return res, err
}

func startDelay(d time.Duration) time.Duration {
	switch {
	case d < 0:
		return 0
	case d == 0:
		return DefaultStartDelay
	}
	return d
}

// sleep waits for d and says whether the wait ran to its end (false: canceled).
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
