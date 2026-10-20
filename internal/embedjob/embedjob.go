// Package embedjob keeps the vectors of knowledge entries up to date in the
// background. It looks for active entries without a current vector (new, edited,
// made by another model or text format), asks the embedding server for them in
// small batches, one request at a time, and stores the result. All its state is
// in the database, so a restart resumes where the last run stopped, and it
// never holds up the server: it runs in a goroutine of its own, starts late,
// pauses between batches and backs off when the embedding server fails.
package embedjob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Store is the part of the store the job uses.
type Store interface {
	KnowledgeEmbeddingDue(m store.EmbedModel, limit int, at time.Time, full bool) (store.EmbedDue, error)
	UpsertKnowledgeEmbedding(e store.KnowledgeEmbedding) error
	TouchKnowledgeEmbeddings(ids []int64, readAt string) error
	RecordEmbeddingFailure(f store.EmbeddingFailure) error
	SetEmbedJobState(st store.EmbedJobState) error
}

// Embedder is the part of the embedding client the job uses.
type Embedder interface {
	Model() string
	PrefixVersion() string
	Documents(ctx context.Context, texts []string) ([][]float32, error)
}

// Config sets the pace of the job. Zero values mean the defaults.
type Config struct {
	// Batch is the most documents in one request. Default 8.
	Batch int
	// Pause is the wait between two batches. Default 2s.
	Pause time.Duration
	// Idle is the wait before the next look when nothing is due. Default 30s.
	Idle time.Duration
	// StartDelay is the wait before the first look, so a starting server is not
	// slowed by it. Default 10s; a negative value means no wait.
	StartDelay time.Duration
	// InitialBackoff is the wait after the first failure; it doubles with every
	// further one up to MaxBackoff. Defaults 5s and 5m.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Park is how long an entry is left alone after the embedding server failed
	// on its text, and it doubles with every further failure up to MaxPark.
	// Defaults 5m and 6h.
	Park, MaxPark time.Duration
	// Timeout bounds one request. Default embed.DefaultBatchTimeout.
	Timeout time.Duration
}

// Defaults for the settings above.
const (
	DefaultBatch          = 8
	DefaultPause          = 2 * time.Second
	DefaultIdle           = 30 * time.Second
	DefaultStartDelay     = 10 * time.Second
	DefaultInitialBackoff = 5 * time.Second
	DefaultMaxBackoff     = 5 * time.Minute
	DefaultPark           = 5 * time.Minute
	DefaultMaxPark        = 6 * time.Hour
)

func (c Config) withDefaults() Config {
	if c.Batch <= 0 {
		c.Batch = DefaultBatch
	}
	if c.Pause <= 0 {
		c.Pause = DefaultPause
	}
	if c.Idle <= 0 {
		c.Idle = DefaultIdle
	}
	if c.StartDelay < 0 {
		c.StartDelay = 0
	} else if c.StartDelay == 0 {
		c.StartDelay = DefaultStartDelay
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = DefaultInitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultMaxBackoff
	}
	if c.MaxBackoff < c.InitialBackoff {
		c.MaxBackoff = c.InitialBackoff
	}
	if c.Park <= 0 {
		c.Park = DefaultPark
	}
	if c.MaxPark <= 0 {
		c.MaxPark = DefaultMaxPark
	}
	if c.MaxPark < c.Park {
		c.MaxPark = c.Park
	}
	if c.Timeout <= 0 {
		c.Timeout = embed.DefaultBatchTimeout
	}
	return c
}

// Job is the background job. Create it with New and start Run in a goroutine.
type Job struct {
	st    Store
	emb   Embedder
	cfg   Config
	log   *slog.Logger
	model store.EmbedModel
	sleep func(ctx context.Context, d time.Duration) bool
	now   func() time.Time

	// full asks the first looks for every entry with a vector, not only for the
	// ones edited since: a text can change in a way updated_at does not show.
	full bool
	// suspect is the batch that failed with an error that may be the server's or
	// one text's. The next attempt at the same batch sends the texts one by one,
	// to find the one that makes it fail.
	suspect string

	failures    int
	lastOK      string
	lastPersist time.Time
	lastLogged  time.Time
}

// New returns a job for the entries of st that e should embed.
func New(st Store, e Embedder, cfg Config, logger *slog.Logger) *Job {
	if logger == nil {
		logger = slog.Default()
	}
	return &Job{
		st: st, emb: e, cfg: cfg.withDefaults(), log: logger,
		model: store.EmbedModel{Model: e.Model(), PrefixVersion: e.PrefixVersion()},
		sleep: sleepContext,
		now:   time.Now,
		full:  true,
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Result is what one step did.
type Result struct {
	// Embedded is the number of vectors stored.
	Embedded int
	// Skipped is the number of entries put aside: the server refused the text for
	// good, or failed on it while it answered the other texts.
	Skipped int
}

// Step embeds one batch. It returns an error when the embedding server fails or
// the vectors cannot be stored; the entries stay due and the next step tries
// them again.
func (j *Job) Step(ctx context.Context) (Result, error) {
	var res Result
	at := j.now()
	due, err := j.st.KnowledgeEmbeddingDue(j.model, j.cfg.Batch, at, j.full)
	if err != nil {
		return res, err
	}
	if len(due.Items) < j.cfg.Batch {
		j.full = false // the look went through every entry there is
	}
	if len(due.Unchanged) > 0 {
		if err := j.st.TouchKnowledgeEmbeddings(due.Unchanged, due.ReadAt); err != nil {
			return res, err
		}
	}
	items := due.Items
	if len(items) == 0 {
		return res, nil
	}
	sig := signature(items)
	if sig == j.suspect {
		return j.oneByOne(ctx, items, due.ReadAt, at)
	}
	vecs, err := j.documents(ctx, texts(items))
	switch {
	case err == nil:
		j.suspect = ""
		for i, d := range items {
			if err := j.store(d, vecs[i], due.ReadAt); err != nil {
				return res, err
			}
			res.Embedded++
		}
		return res, nil
	case embed.Permanent(err) && len(items) > 1:
		return j.oneByOne(ctx, items, due.ReadAt, at)
	case embed.Permanent(err):
		j.park(items[0], err, true, at)
		res.Skipped++
		return res, nil
	case len(items) > 1:
		// Maybe the server, maybe one text. Say so, and the next attempt at this
		// batch sends the texts one by one.
		j.suspect = sig
	}
	return res, err
}

// oneByOne sends the texts of a batch separately, to find out which of them the
// server fails on. If the server answers none of them, it is the server: the
// error goes back and nothing is parked. If it answers some, the ones it does
// not answer are put aside and the rest is stored.
func (j *Job) oneByOne(ctx context.Context, items []store.EmbedItem, readAt string, at time.Time) (Result, error) {
	var res Result
	type failed struct {
		item store.EmbedItem
		err  error
	}
	var bad []failed
	for _, d := range items {
		vecs, err := j.documents(ctx, []string{d.Text})
		if err == nil {
			if err := j.store(d, vecs[0], readAt); err != nil {
				return res, err
			}
			res.Embedded++
			continue
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		bad = append(bad, failed{d, err})
	}
	allRefused := len(bad) > 0
	for _, f := range bad {
		allRefused = allRefused && embed.Permanent(f.err)
	}
	if res.Embedded == 0 && len(bad) > 0 && !allRefused {
		return res, bad[len(bad)-1].err
	}
	for _, f := range bad {
		j.park(f.item, f.err, embed.Permanent(f.err), at)
		res.Skipped++
	}
	j.suspect = ""
	return res, nil
}

func texts(items []store.EmbedItem) []string {
	out := make([]string, len(items))
	for i, d := range items {
		out[i] = d.Text
	}
	return out
}

func signature(items []store.EmbedItem) string {
	var b strings.Builder
	for _, d := range items {
		b.WriteString(strconv.FormatInt(d.ID, 10))
		b.WriteByte(':')
		b.WriteString(d.Hash)
		b.WriteByte(';')
	}
	return b.String()
}

func (j *Job) documents(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, j.cfg.Timeout)
	defer cancel()
	return j.emb.Documents(ctx, texts)
}

func (j *Job) store(d store.EmbedItem, vec []float32, readAt string) error {
	q, scale, err := embed.Quantize(vec)
	if err != nil {
		return fmt.Errorf("entry %d: %w", d.ID, err)
	}
	return j.st.UpsertKnowledgeEmbedding(store.KnowledgeEmbedding{
		KnowledgeID: d.ID, Model: j.model.Model, PrefixVersion: j.model.PrefixVersion,
		TextHash: d.Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: readAt,
	})
}

// park puts an entry aside, for good when the server refuses its text, else for a
// time that doubles with every failure.
func (j *Job) park(d store.EmbedItem, err error, permanent bool, at time.Time) {
	attempts := d.Attempts + 1
	wait := j.cfg.ParkFor(attempts)
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if perr := j.st.RecordEmbeddingFailure(store.EmbeddingFailure{
		KnowledgeID: d.ID, Model: j.model.Model, PrefixVersion: j.model.PrefixVersion, TextHash: d.Hash,
		Attempts: attempts, Permanent: permanent, RetryAt: at.Add(wait).UTC().Format(time.RFC3339), Error: msg,
	}); perr != nil {
		j.log.Warn("embed_entry_parked", "id", d.ID, "error", "not recorded: "+perr.Error())
		return
	}
	if permanent {
		j.log.Warn("embed_entry_parked", "id", d.ID, "error", msg, "attempts", attempts,
			"note", "the embedding server refuses this text; it is left alone until the entry changes")
		return
	}
	j.log.Warn("embed_entry_parked", "id", d.ID, "error", msg, "attempts", attempts, "retry_in", wait.String(),
		"note", "the embedding server fails on this text while it answers the others")
}

// ParkFor is how long an entry is left alone after its n-th failure.
func (c Config) ParkFor(n int) time.Duration {
	c = c.withDefaults()
	d := c.Park
	for i := 1; i < n && d < c.MaxPark; i++ {
		d *= 2
	}
	return min(d, c.MaxPark)
}

// Backoff is the wait after the n-th failure in a row (n >= 1).
func (c Config) Backoff(n int) time.Duration {
	c = c.withDefaults()
	d := c.InitialBackoff
	for i := 1; i < n && d < c.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, c.MaxBackoff)
}

// Run steps until ctx ends: after each batch it waits Pause, when nothing is
// due Idle, after a failure the backoff.
func (j *Job) Run(ctx context.Context) {
	if !j.sleep(ctx, j.cfg.StartDelay) {
		return
	}
	for {
		res, err := j.Step(ctx)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		switch {
		case err != nil:
			j.failures++
			wait = j.cfg.Backoff(j.failures)
			j.noteFailure(err, wait)
		case res.Embedded+res.Skipped > 0:
			j.noteProgress(res)
			wait = j.cfg.Pause
		default:
			wait = j.cfg.Idle
		}
		if !j.sleep(ctx, wait) {
			return
		}
	}
}

const stateEvery = time.Minute

func (j *Job) stamp() string { return j.now().UTC().Format(time.RFC3339) }

func (j *Job) noteFailure(err error, wait time.Duration) {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300])
	}
	if j.failures == 1 || j.now().Sub(j.lastLogged) >= stateEvery {
		j.lastLogged = j.now()
		j.log.Warn("embed_job_failed", "error", msg, "failures", j.failures, "retry_in", wait.String())
	}
	if j.failures == 1 || j.now().Sub(j.lastPersist) >= stateEvery {
		j.persist(store.EmbedJobState{LastOKAt: j.lastOK, LastError: msg, LastErrorAt: j.stamp(), Failures: j.failures})
	}
}

func (j *Job) noteProgress(res Result) {
	recovered := j.failures > 0
	j.failures = 0
	if res.Embedded > 0 {
		j.lastOK = j.stamp()
	}
	if recovered {
		j.log.Info("embed_job_recovered")
	}
	if recovered || j.now().Sub(j.lastPersist) >= stateEvery {
		j.persist(store.EmbedJobState{LastOKAt: j.lastOK})
	}
}

func (j *Job) persist(st store.EmbedJobState) {
	j.lastPersist = j.now()
	if err := j.st.SetEmbedJobState(st); err != nil && !errors.Is(err, context.Canceled) {
		j.log.Warn("embed_job_state", "error", err.Error())
	}
}
