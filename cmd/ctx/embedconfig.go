package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The embedding server of hybrid search is configured the same way for serve,
// "ctx embed status" and "ctx eval search": a flag, or the environment variable
// next to it. Without a URL nothing about embeddings runs and search is the
// full-text search it always was.
const (
	envEmbedURL           = "GHOSTTREE_EMBED_URL"
	envEmbedModel         = "GHOSTTREE_EMBED_MODEL"
	envEmbedMinSimilarity = "GHOSTTREE_EMBED_MIN_SIMILARITY"
	envEmbedQueryTimeout  = "GHOSTTREE_EMBED_QUERY_TIMEOUT"
	envEmbedBatch         = "GHOSTTREE_EMBED_BATCH"
	envEmbedPause         = "GHOSTTREE_EMBED_PAUSE"
	envEmbedIdle          = "GHOSTTREE_EMBED_IDLE"
	envEmbedMaxBackoff    = "GHOSTTREE_EMBED_MAX_BACKOFF"
)

// embedSettings is the validated configuration.
type embedSettings struct {
	// URL is empty when embeddings are off.
	URL          string
	Model        string
	QueryTimeout time.Duration
	// MinSimilarity is the cosine an entry needs to enter the embedding ranking.
	MinSimilarity float32
	Job           embedjob.Config
}

func (e embedSettings) enabled() bool { return e.URL != "" }

// client builds the client for the configured server.
func (e embedSettings) client() (*embed.Client, error) {
	return embed.New(embed.Config{URL: e.URL, Model: e.Model})
}

// embedFlags holds the raw flag values; resolve parses and checks them.
type embedFlags struct {
	url, model, minSimilarity, queryTimeout, batch, pause, idle, maxBackoff *string
	job                                                                     bool
}

// addEmbedFlags registers --embed-url and --embed-model, and with job also the
// settings of the background job and of the query timeout, which only the
// server needs.
func addEmbedFlags(fs *flag.FlagSet, job bool) *embedFlags {
	f := &embedFlags{job: job}
	f.url = fs.String("embed-url", os.Getenv(envEmbedURL), "base URL of the embedding server, e.g. http://127.0.0.1:8475; empty turns hybrid search off (env "+envEmbedURL+")")
	f.model = fs.String("embed-model", os.Getenv(envEmbedModel), "model id sent to the embedding server and stored with every vector (default "+embed.DefaultModel+", env "+envEmbedModel+")")
	f.minSimilarity = fs.String("embed-min-similarity", os.Getenv(envEmbedMinSimilarity), "cosine an entry needs to enter the embedding ranking: 0 for no minimum up to below 1 (default "+strconv.FormatFloat(embed.DefaultMinSimilarity, 'f', -1, 64)+", measured for the default model; env "+envEmbedMinSimilarity+")")
	if job {
		f.queryTimeout = fs.String("embed-query-timeout", os.Getenv(envEmbedQueryTimeout), "how long a search waits for the query vector before it answers from full text alone (default "+embed.DefaultQueryTimeout.String()+", env "+envEmbedQueryTimeout+")")
		f.batch = fs.String("embed-batch", os.Getenv(envEmbedBatch), "most documents in one request of the background job (default "+strconv.Itoa(embedjob.DefaultBatch)+", env "+envEmbedBatch+")")
		f.pause = fs.String("embed-pause", os.Getenv(envEmbedPause), "wait between two batches (default "+embedjob.DefaultPause.String()+", env "+envEmbedPause+")")
		f.idle = fs.String("embed-idle", os.Getenv(envEmbedIdle), "wait before the next look when nothing is due (default "+embedjob.DefaultIdle.String()+", env "+envEmbedIdle+")")
		f.maxBackoff = fs.String("embed-max-backoff", os.Getenv(envEmbedMaxBackoff), "longest wait after repeated failures; it starts at "+embedjob.DefaultInitialBackoff.String()+" and doubles (default "+embedjob.DefaultMaxBackoff.String()+", env "+envEmbedMaxBackoff+")")
	}
	return f
}

func (f *embedFlags) resolve() (embedSettings, error) {
	var s embedSettings
	s.Model = strings.TrimSpace(*f.model)
	if s.Model == "" {
		s.Model = embed.DefaultModel
	}
	if raw := strings.TrimSpace(*f.url); raw != "" {
		base, err := embed.ParseURL(raw)
		if err != nil {
			return s, fmt.Errorf("--embed-url: %w", err)
		}
		s.URL = base
	}
	s.MinSimilarity = embed.DefaultMinSimilarity
	if v := strings.TrimSpace(*f.minSimilarity); v != "" {
		x, err := strconv.ParseFloat(v, 32)
		if err != nil || x < 0 || x >= 1 {
			return s, fmt.Errorf("--embed-min-similarity must be a number from 0 to below 1, got %q", v)
		}
		s.MinSimilarity = float32(x)
	}
	if !f.job {
		return s, nil
	}
	duration := func(name string, raw *string, dst *time.Duration) error {
		v := strings.TrimSpace(*raw)
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("--%s must be a positive duration such as 2s, got %q", name, v)
		}
		*dst = d
		return nil
	}
	if err := duration("embed-query-timeout", f.queryTimeout, &s.QueryTimeout); err != nil {
		return s, err
	}
	if s.QueryTimeout == 0 {
		s.QueryTimeout = embed.DefaultQueryTimeout
	}
	if err := duration("embed-pause", f.pause, &s.Job.Pause); err != nil {
		return s, err
	}
	if err := duration("embed-idle", f.idle, &s.Job.Idle); err != nil {
		return s, err
	}
	if err := duration("embed-max-backoff", f.maxBackoff, &s.Job.MaxBackoff); err != nil {
		return s, err
	}
	if v := strings.TrimSpace(*f.batch); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 64 {
			return s, fmt.Errorf("--embed-batch must be a number from 1 to 64, got %q", v)
		}
		s.Job.Batch = n
	}
	return s, nil
}

// startEmbedding turns hybrid search on and starts the background job. It
// returns at once; the returned function waits for the job to have stopped,
// which it does when ctx ends.
func startEmbedding(ctx context.Context, st *store.Store, cfg embedSettings, logger *slog.Logger) func() {
	if !cfg.enabled() {
		return func() {}
	}
	c, err := cfg.client()
	if err != nil {
		logger.Error("embed_config", "error", err.Error())
		return func() {}
	}
	st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, Timeout: cfg.QueryTimeout, MinSimilarity: cfg.MinSimilarity, Logger: logger})
	job := embedjob.New(st, c, cfg.Job, logger)
	logger.Info("embed_config", "url", c.URL(), "model", c.Model(), "prefix_version", c.PrefixVersion(),
		"query_timeout", cfg.QueryTimeout.String(), "min_similarity", cfg.MinSimilarity, "batch", orDefault(cfg.Job.Batch, embedjob.DefaultBatch),
		"pause", orDefault(cfg.Job.Pause, embedjob.DefaultPause).String(),
		"note", "hybrid search is on; entries are embedded in the background")
	if !embed.Loopback(c.URL()) {
		logger.Warn("embed_config", "url", c.URL(),
			"note", "the embedding server is not on this machine; the text of every active knowledge entry is sent to it")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.Run(ctx)
	}()
	return func() { <-done }
}

func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}
