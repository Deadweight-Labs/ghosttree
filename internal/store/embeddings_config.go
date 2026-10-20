package store

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// QueryEmbedder turns a search query into a unit vector. *embed.Client is the
// implementation in service; tests and the quality measurement bring their own.
type QueryEmbedder interface {
	Model() string
	PrefixVersion() string
	Query(ctx context.Context, q string) ([]float32, error)
}

// EmbedSearchConfig switches hybrid search on.
type EmbedSearchConfig struct {
	Embedder QueryEmbedder
	// Timeout is how long a search waits for the query vector before it answers
	// from the full-text index alone. Zero means embed.DefaultQueryTimeout.
	Timeout time.Duration
	// MinSimilarity is the cosine below which an entry does not enter the
	// embedding ranking. Zero keeps every entry: the ranking then always has
	// neighbours to offer, however far away. Which value separates a near entry
	// from a far one depends on the model; "ctx eval search --embed-min-similarity"
	// measures what a value costs.
	MinSimilarity float32
	// Logger receives the one line per minute about a search that fell back.
	// Nil means slog.Default().
	Logger *slog.Logger
}

// embedShared is the embedding configuration of a store and its reader, set
// once at startup and read on every search.
type embedShared struct {
	cfg atomic.Pointer[EmbedSearchConfig]

	hybrid    atomic.Int64
	fallbacks atomic.Int64

	logMu      sync.Mutex
	lastLog    time.Time
	suppressed int
	lastDim    time.Time
}

func (s *Store) embedShared() *embedShared {
	s.embedOnce.Do(func() {
		if s.esh == nil {
			s.esh = &embedShared{}
		}
	})
	return s.esh
}

// SetEmbedder turns hybrid search on for this store and its readers, or off
// again with a nil Embedder. It is called while the server starts.
func (s *Store) SetEmbedder(c EmbedSearchConfig) {
	if c.Embedder == nil {
		s.embedShared().cfg.Store(nil)
		return
	}
	if c.Timeout <= 0 {
		c.Timeout = embed.DefaultQueryTimeout
	}
	s.embedShared().cfg.Store(&c)
}

// EmbedderConfigured says whether searches ask an embedding server.
func (s *Store) EmbedderConfigured() bool { return s.embedShared().cfg.Load() != nil }

// EmbedSearchStats counts the searches that were answered by both rankings and
// those that fell back to full text because the embedding was not available.
func (s *Store) EmbedSearchStats() (hybrid, fallbacks int64) {
	sh := s.embedShared()
	return sh.hybrid.Load(), sh.fallbacks.Load()
}

// logFallback writes at most one line a minute. The error text never holds the
// query: it comes from the transport or from the server's own message.
func (sh *embedShared) logFallback(c *EmbedSearchConfig, err error) {
	sh.fallbacks.Add(1)
	sh.logMu.Lock()
	if !sh.lastLog.IsZero() && time.Since(sh.lastLog) < time.Minute {
		sh.suppressed++
		sh.logMu.Unlock()
		return
	}
	skipped := sh.suppressed
	sh.lastLog, sh.suppressed = time.Now(), 0
	sh.logMu.Unlock()
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("embed_search_fallback", "error", fmt.Sprint(err), "suppressed", skipped,
		"note", "answering from the full-text index; at most one line a minute")
}

// warnDimension says, at most once a minute, that stored vectors have another
// length than the query vector and are therefore left out of the ranking. That
// happens when the model behind a model id is exchanged without changing the id;
// without a word the search would just stop finding those entries.
func (sh *embedShared) warnDimension(c *EmbedSearchConfig, mismatched, want int) {
	sh.logMu.Lock()
	if !sh.lastDim.IsZero() && time.Since(sh.lastDim) < time.Minute {
		sh.logMu.Unlock()
		return
	}
	sh.lastDim = time.Now()
	sh.logMu.Unlock()
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("embed_vector_dimension", "vectors", mismatched, "query_dimension", want,
		"note", "stored vectors of another dimension are not compared; give the model a new id (--embed-model) so that they are computed again")
}
