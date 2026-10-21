package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	envEmbedQueryURL      = "GHOSTTREE_EMBED_QUERY_URL"
	envEmbedModel         = "GHOSTTREE_EMBED_MODEL"
	envEmbedMinSimilarity = "GHOSTTREE_EMBED_MIN_SIMILARITY"
	envEmbedQueryTimeout  = "GHOSTTREE_EMBED_QUERY_TIMEOUT"
	envEmbedBatch         = "GHOSTTREE_EMBED_BATCH"
	envEmbedPause         = "GHOSTTREE_EMBED_PAUSE"
	envEmbedIdle          = "GHOSTTREE_EMBED_IDLE"
	envEmbedMaxBackoff    = "GHOSTTREE_EMBED_MAX_BACKOFF"
	// The kinds of object that are embedded, and the ones whose search uses the
	// vectors. Without them it is knowledge, as it was before there were other
	// kinds; with only the first, the search kinds are the same.
	envEmbedKinds       = "GHOSTTREE_EMBED_KINDS"
	envEmbedSearchKinds = "GHOSTTREE_EMBED_SEARCH_KINDS"
	// The span of the clock in which the sessions are embedded (they are many and
	// take three nights to catch up on), and the weight of their embedding ranking
	// in the search.
	envEmbedSessionWindow = "GHOSTTREE_EMBED_SESSION_WINDOW"
	envEmbedSessionWeight = "GHOSTTREE_EMBED_SESSION_WEIGHT"
)

// embedSettings is the validated configuration.
type embedSettings struct {
	// URL is empty when embeddings are off. It is the server of the background
	// job, which makes the vectors, and of the search queries unless QueryURL says
	// otherwise.
	URL string
	// QueryURL is a second server that embeds only search queries, so that a query
	// does not wait behind a batch of documents. It is empty when the queries go to
	// URL, which is also the case when it names the same server.
	QueryURL     string
	Model        string
	QueryTimeout time.Duration
	// MinSimilarity is the cosine an entry needs to enter the embedding ranking.
	MinSimilarity float32
	// SessionWeight is the weight of the embedding ranking in the fusion of the
	// session search, against 1 for the words.
	SessionWeight float64
	Job           embedjob.Config
	// Kinds are the kinds of object that get vectors, SearchKinds the ones whose
	// search uses them (a subset). Empty Kinds means knowledge only, empty
	// SearchKinds the same as Kinds.
	Kinds, SearchKinds []string
	// kindsGiven says that a flag or the environment named the kinds, as opposed
	// to the default; "ctx embed status" then says which kind is switched on.
	kindsGiven bool
}

func (e embedSettings) enabled() bool { return e.URL != "" }

// embedKinds are the kinds the job embeds.
func (e embedSettings) embedKinds() []string {
	if len(e.Kinds) == 0 {
		return []string{store.KindKnowledge}
	}
	return e.Kinds
}

// searchKinds are the kinds whose search uses the vectors.
func (e embedSettings) searchKinds() []string {
	if len(e.SearchKinds) == 0 {
		return e.embedKinds()
	}
	return e.SearchKinds
}

// separateQuery says whether search queries go to another server than the
// documents of the background job.
func (e embedSettings) separateQuery() bool { return e.QueryURL != "" }

// client builds the client for the server of the background job.
func (e embedSettings) client() (*embed.Client, error) {
	return embed.New(embed.Config{URL: e.URL, Model: e.Model})
}

// queryClient builds the client for the server that embeds search queries; only
// meaningful with separateQuery.
func (e embedSettings) queryClient() (*embed.Client, error) {
	return embed.New(embed.Config{URL: e.QueryURL, Model: e.Model})
}

// embedFlags holds the raw flag values; resolve parses and checks them.
type embedFlags struct {
	url, queryURL, model, minSimilarity, queryTimeout, batch, pause, idle, maxBackoff *string
	kinds, searchKinds                                                                *string
	sessionWindow, sessionWeight                                                      *string
	job                                                                               bool
}

// addEmbedFlags registers --embed-url, --embed-query-url and --embed-model, and
// with job also the settings of the background job and of the query timeout,
// which only the server needs.
func addEmbedFlags(fs *flag.FlagSet, job bool) *embedFlags {
	f := &embedFlags{job: job}
	f.url = fs.String("embed-url", os.Getenv(envEmbedURL), "base URL of the embedding server, e.g. http://127.0.0.1:8475; empty turns hybrid search off (env "+envEmbedURL+")")
	f.queryURL = fs.String("embed-query-url", os.Getenv(envEmbedQueryURL), "base URL of a second embedding server that embeds only search queries, so that a search does not wait behind the background job; it must serve the same model, and --embed-url is still needed for the job; empty uses --embed-url (env "+envEmbedQueryURL+")")
	f.model = fs.String("embed-model", os.Getenv(envEmbedModel), "model id sent to the embedding server and stored with every vector (default "+embed.DefaultModel+", env "+envEmbedModel+")")
	f.minSimilarity = fs.String("embed-min-similarity", os.Getenv(envEmbedMinSimilarity), "cosine an entry needs to enter the embedding ranking: 0 for no minimum up to below 1 (default "+strconv.FormatFloat(embed.DefaultMinSimilarity, 'f', -1, 64)+", measured for the default model; env "+envEmbedMinSimilarity+")")
	f.sessionWeight = fs.String("embed-session-weight", os.Getenv(envEmbedSessionWeight), "weight of the embedding ranking against 1 for the words ranking in the fusion of the session search, above 0 up to 100 (default "+strconv.FormatFloat(embed.DefaultSessionVectorWeight, 'f', -1, 64)+"; env "+envEmbedSessionWeight+"); the other kinds fuse with equal weights")
	if job {
		f.addKinds(fs)
		f.queryTimeout = fs.String("embed-query-timeout", os.Getenv(envEmbedQueryTimeout), "how long a search waits for the query vector before it answers from full text alone (default "+embed.DefaultQueryTimeout.String()+", env "+envEmbedQueryTimeout+")")
		f.batch = fs.String("embed-batch", os.Getenv(envEmbedBatch), "most documents in one request of the background job (default "+strconv.Itoa(embedjob.DefaultBatch)+", env "+envEmbedBatch+")")
		f.pause = fs.String("embed-pause", os.Getenv(envEmbedPause), "wait between two batches (default "+embedjob.DefaultPause.String()+", env "+envEmbedPause+")")
		f.idle = fs.String("embed-idle", os.Getenv(envEmbedIdle), "wait before the next look when nothing is due (default "+embedjob.DefaultIdle.String()+", env "+envEmbedIdle+")")
		f.maxBackoff = fs.String("embed-max-backoff", os.Getenv(envEmbedMaxBackoff), "longest wait after repeated failures; it starts at "+embedjob.DefaultInitialBackoff.String()+" and doubles (default "+embedjob.DefaultMaxBackoff.String()+", env "+envEmbedMaxBackoff+")")
	}
	return f
}

// addKinds registers --embed-kinds and --embed-search-kinds: for serve, which
// embeds and searches by them, and for "ctx embed status", which says what a
// configuration would do. "ctx eval search" measures any kind and takes neither.
func (f *embedFlags) addKinds(fs *flag.FlagSet) {
	names := strings.Join(store.EmbedKinds(), ", ")
	f.kinds = fs.String("embed-kinds", os.Getenv(envEmbedKinds), "kinds of object that get vectors, comma-separated: "+names+", or all, which is every kind but session; sessions are only embedded when named (default "+store.KindKnowledge+", env "+envEmbedKinds+"); a kind is embedded in the background and can be measured with ctx eval search before its search uses the vectors")
	f.searchKinds = fs.String("embed-search-kinds", os.Getenv(envEmbedSearchKinds), "kinds whose search uses the vectors, a subset of --embed-kinds (default: all of them, env "+envEmbedSearchKinds+"); the search of every other kind answers from full text")
	f.sessionWindow = fs.String("embed-session-window", os.Getenv(envEmbedSessionWindow), "span of the local time of the server in which the sessions are embedded, HH:MM-HH:MM (may cross midnight) or always (default "+embedjob.DefaultSessionWindow.String()+", env "+envEmbedSessionWindow+"); outside it the sessions wait, and every other kind goes first at any hour")
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
	if raw := strings.TrimSpace(*f.queryURL); raw != "" {
		base, err := embed.ParseURL(raw)
		if err != nil {
			return s, fmt.Errorf("--embed-query-url: %w", err)
		}
		if s.URL == "" {
			return s, errors.New("--embed-query-url needs --embed-url: the server of --embed-url makes the vectors that a query is compared with, and without them there is nothing to search")
		}
		if base != s.URL {
			s.QueryURL = base
		}
	}
	s.MinSimilarity = embed.DefaultMinSimilarity
	if v := strings.TrimSpace(*f.minSimilarity); v != "" {
		x, err := strconv.ParseFloat(v, 32)
		if err != nil || x < 0 || x >= 1 {
			return s, fmt.Errorf("--embed-min-similarity must be a number from 0 to below 1, got %q", v)
		}
		s.MinSimilarity = float32(x)
	}
	if f.kinds != nil {
		kinds, err := store.ParseEmbedKinds(*f.kinds)
		if err != nil {
			return s, fmt.Errorf("--embed-kinds: %w", err)
		}
		if len(kinds) == 0 {
			kinds = []string{store.KindKnowledge}
		}
		s.Kinds = kinds
		s.SearchKinds = kinds
		s.kindsGiven = strings.TrimSpace(*f.kinds) != "" || strings.TrimSpace(*f.searchKinds) != ""
		if f.sessionWindow != nil && strings.TrimSpace(*f.sessionWindow) != "" {
			if s.Job.SessionWindow, err = embedjob.ParseWindow(*f.sessionWindow); err != nil {
				return s, fmt.Errorf("--embed-session-window: %w", err)
			}
		}
		if strings.TrimSpace(*f.searchKinds) != "" {
			if s.SearchKinds, err = store.ParseEmbedKinds(*f.searchKinds); err != nil {
				return s, fmt.Errorf("--embed-search-kinds: %w", err)
			}
			for _, k := range s.SearchKinds {
				if !slices.Contains(s.Kinds, k) {
					return s, fmt.Errorf("--embed-search-kinds names %s, which --embed-kinds does not embed; add it there, or the search has no vectors to use", k)
				}
			}
		}
		s.Job.Kinds = s.Kinds
	}
	s.SessionWeight = embed.DefaultSessionVectorWeight
	if v := strings.TrimSpace(*f.sessionWeight); v != "" {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x <= 0 || x > 100 {
			return s, fmt.Errorf("--embed-session-weight must be a number above 0 up to 100, got %q", v)
		}
		s.SessionWeight = x
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
//
// With a separate query server the queries are embedded there, and a check in
// the background keeps verifying that it serves the same model as the job server;
// as long as it finds a difference, searches answer from full text.
func startEmbedding(ctx context.Context, st *store.Store, cfg embedSettings, logger *slog.Logger) func() {
	if !cfg.enabled() {
		return func() {}
	}
	c, err := cfg.client()
	if err != nil {
		logger.Error("embed_config", "error", err.Error())
		return func() {}
	}
	search := store.EmbedSearchConfig{Embedder: c, Timeout: cfg.QueryTimeout, MinSimilarity: cfg.MinSimilarity, Logger: logger, Kinds: cfg.searchKinds(),
		VectorWeights: map[string]float64{store.KindSession: cfg.SessionWeight}}
	var check *embed.ModelCheck
	var queryClient *embed.Client
	if cfg.separateQuery() {
		if queryClient, err = cfg.queryClient(); err != nil {
			logger.Error("embed_config", "error", err.Error())
			return func() {}
		}
		check = embed.NewModelCheck(queryClient, c, embed.CheckConfig{}, logger)
		search.Embedder, search.Ready = queryClient, check.Err
	}
	st.SetEmbedder(search)
	jobConfig := cfg.Job
	jobConfig.Kinds = cfg.embedKinds()
	job := embedjob.New(st, c, jobConfig, logger)
	attrs := []any{"url", c.URL(), "model", c.Model(), "prefix_version", c.PrefixVersion(),
		"query_timeout", cfg.QueryTimeout.String(), "min_similarity", cfg.MinSimilarity, "batch", orDefault(cfg.Job.Batch, embedjob.DefaultBatch),
		"pause", orDefault(cfg.Job.Pause, embedjob.DefaultPause).String(),
		"kinds", strings.Join(jobConfig.Kinds, ","), "search_kinds", strings.Join(search.Kinds, ",")}
	if slices.Contains(jobConfig.Kinds, store.KindSession) {
		window := jobConfig.SessionWindow
		if window == (embedjob.Window{}) {
			window = embedjob.DefaultSessionWindow
		}
		attrs = append(attrs, "session_window", window.String(), "session_weight", cfg.SessionWeight)
	}
	note := "hybrid search is on; objects are embedded in the background"
	if queryClient != nil {
		attrs = append(attrs, "query_url", queryClient.URL())
		note += "; search queries go to their own server"
	}
	logger.Info("embed_config", append(attrs, "note", note)...)
	if !embed.Loopback(c.URL()) {
		logger.Warn("embed_config", "url", c.URL(),
			"note", "the embedding server is not on this machine; the text of every embedded object ("+strings.Join(jobConfig.Kinds, ", ")+") is sent to it")
	}
	if queryClient != nil && !embed.Loopback(queryClient.URL()) {
		logger.Warn("embed_config", "url", queryClient.URL(),
			"note", "the query server is not on this machine; every search query is sent to it")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job.Run(ctx)
	}()
	if check != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			check.Run(ctx)
		}()
	}
	return wg.Wait
}

// verifyServers compares the query server with the job server once, for the two
// commands that look at the servers from outside. A *embed.DisagreementError is
// a verdict; any other error says that a server did not answer.
func verifyServers(cfg embedSettings) (embed.Agreement, error) {
	job, err := cfg.client()
	if err != nil {
		return embed.Agreement{}, err
	}
	query, err := cfg.queryClient()
	if err != nil {
		return embed.Agreement{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return embed.CompareServers(ctx, query, job)
}

func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}
