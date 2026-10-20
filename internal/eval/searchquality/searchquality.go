// Package searchquality measures how well the knowledge search finds an entry
// from a quote of the transcript it was distilled from. It calls the same
// store function the server's search calls, once per ranking, so the numbers
// are those of production code and not of a re-implementation. The report holds
// counts and rates only: the quotes can contain secrets, and nothing of them is
// ever printed.
package searchquality

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Options sets what is measured.
type Options struct {
	// Sample is the number of queries drawn from the evidence quotes. A smaller
	// stock is used whole. Default 1500.
	Sample int
	// Seed makes the draw repeatable. Default 20261020.
	Seed int64
	// MinChars is the shortest quote that counts as a query. Default 30.
	MinChars int
	// Embedder embeds the queries for the vector and hybrid rankings. Without
	// it only the full-text ranking is measured.
	Embedder store.QueryEmbedder
	// MinSimilarity is the cosine an entry needs to enter the embedding ranking,
	// as the server would be configured.
	MinSimilarity float32
	// Pause is the wait between two query embeddings, to keep the embedding
	// server from being the only thing the machine does.
	Pause time.Duration
	// Progress receives a line now and then while the queries are embedded and
	// searched. Nil means silence.
	Progress io.Writer
	// Every is the interval between two progress lines. Default 10s.
	Every time.Duration
}

func (o Options) withDefaults() Options {
	if o.Sample <= 0 {
		o.Sample = 1500
	}
	if o.Seed == 0 {
		o.Seed = 20261020
	}
	if o.MinChars <= 0 {
		o.MinChars = 30
	}
	if o.Every <= 0 {
		o.Every = 10 * time.Second
	}
	return o
}

// Mode names a ranking.
type Mode string

const (
	FullText Mode = "fulltext"
	Vector   Mode = "vector"
	Hybrid   Mode = "hybrid"
)

func (m Mode) searchMode() store.SearchMode {
	switch m {
	case Vector:
		return store.SearchVectorOnly
	case Hybrid:
		return store.SearchHybridStrict
	}
	return store.SearchFullTextOnly
}

// Scores are the usual rank measures over n queries.
type Scores struct {
	N                int
	R1, R3, R10, MRR float64
}

// Block is one view of the search over one set of queries.
type Block struct {
	Title string
	Rows  map[Mode]Scores
}

// Report is the result of a run.
type Report struct {
	Stock, Sampled     int
	DistinctTargets    int
	QuarantinedTargets int
	Seed               int64
	MinChars           int
	Model, Format      string
	// Embedded and Active say how much of the stock has a vector.
	Embedded, Active int
	Modes            []Mode
	Blocks           []Block
}

type tally struct {
	n, r1, r3, r10 int
	mrr            float64
}

func (t *tally) add(rank int) {
	t.n++
	if rank == 0 || rank > 10 {
		return
	}
	t.r10++
	t.mrr += 1 / float64(rank)
	if rank <= 3 {
		t.r3++
	}
	if rank == 1 {
		t.r1++
	}
}

func (t tally) scores() Scores {
	if t.n == 0 {
		return Scores{}
	}
	n := float64(t.n)
	return Scores{N: t.n, R1: float64(t.r1) / n, R3: float64(t.r3) / n, R10: float64(t.r10) / n, MRR: t.mrr / n}
}

// cache embeds each query once and remembers it: the three rankings of two views
// ask for the same vector six times.
type cache struct {
	inner store.QueryEmbedder
	mu    sync.Mutex
	vecs  map[string][]float32
}

func (c *cache) Model() string         { return c.inner.Model() }
func (c *cache) PrefixVersion() string { return c.inner.PrefixVersion() }
func (c *cache) Query(ctx context.Context, q string) ([]float32, error) {
	c.mu.Lock()
	v, ok := c.vecs[q]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := c.inner.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.vecs[q] = v
	c.mu.Unlock()
	return v, nil
}

type progress struct {
	w     io.Writer
	every time.Duration
	start time.Time
	last  time.Time
}

func (p *progress) tick(what string, done, total int) {
	if p.w == nil {
		return
	}
	now := time.Now()
	if done != total && now.Sub(p.last) < p.every {
		return
	}
	p.last = now
	elapsed := now.Sub(p.start)
	eta := ""
	if done > 0 && done < total {
		eta = fmt.Sprintf(", about %s left", (elapsed * time.Duration(total-done) / time.Duration(done)).Round(time.Second))
	}
	fmt.Fprintf(p.w, "%s: %d of %d (%d%%), %s elapsed%s\n", what, done, total, done*100/max(total, 1), elapsed.Round(time.Second), eta)
}

// Run draws the queries and measures the rankings on st, which must be opened
// read-only. The view of the operator sees every active entry, quarantined ones
// included; a second view leaves the quarantined entries out, the way the search
// for agents and the hook deliver them.
func Run(ctx context.Context, st *store.Store, opts Options) (Report, error) {
	opts = opts.withDefaults()
	var rep Report
	all, err := st.EvidenceQueries(opts.MinChars)
	if err != nil {
		return rep, err
	}
	rep.Stock, rep.Seed, rep.MinChars = len(all), opts.Seed, opts.MinChars
	queries := all
	if len(all) > opts.Sample {
		rng := rand.New(rand.NewPCG(uint64(opts.Seed), uint64(opts.Seed)^0x9e3779b97f4a7c15))
		queries = make([]store.EvidenceQuery, 0, opts.Sample)
		for _, i := range rng.Perm(len(all))[:opts.Sample] {
			queries = append(queries, all[i])
		}
		slices.SortFunc(queries, func(a, b store.EvidenceQuery) int { return int(a.ID - b.ID) })
	}
	rep.Sampled = len(queries)
	targets := map[int64]bool{}
	for _, q := range queries {
		targets[q.KnowledgeID] = true
		if q.Confidence == "quarantined" {
			rep.QuarantinedTargets++
		}
	}
	rep.DistinctTargets = len(targets)

	rep.Modes = []Mode{FullText}
	prog := &progress{w: opts.Progress, every: opts.Every, start: time.Now()}
	if opts.Embedder != nil {
		rep.Model, rep.Format = opts.Embedder.Model(), opts.Embedder.PrefixVersion()
		model := store.EmbedModel{Model: rep.Model, PrefixVersion: rep.Format}
		status, err := st.KnowledgeEmbeddingStatus(model)
		if err != nil {
			return rep, err
		}
		rep.Embedded, rep.Active = status.Current, status.Active
		rep.Modes = append(rep.Modes, Vector, Hybrid)

		c := &cache{inner: opts.Embedder, vecs: map[string][]float32{}}
		for i, q := range queries {
			if _, err := c.Query(ctx, q.Quote); err != nil {
				return rep, fmt.Errorf("embedding query %d of %d: %w", i+1, len(queries), err)
			}
			prog.tick("embedding the queries", i+1, len(queries))
			if opts.Pause > 0 && i+1 < len(queries) {
				select {
				case <-ctx.Done():
					return rep, ctx.Err()
				case <-time.After(opts.Pause):
				}
			}
		}
		st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, Timeout: time.Minute, MinSimilarity: opts.MinSimilarity})
	}

	rank := func(q store.EvidenceQuery, operator bool, mode Mode) (int, error) {
		hits, err := st.SearchKnowledgeWith(q.Quote, store.KnowledgeSearch{
			Operator: operator, ActiveOnly: true, NoRecord: true, Limit: 10, Mode: mode.searchMode(),
		})
		if err != nil {
			return 0, err
		}
		for i, h := range hits {
			if h.ID == q.KnowledgeID {
				return i + 1, nil
			}
		}
		return 0, nil
	}

	type key struct {
		block int
		mode  Mode
	}
	tallies := map[key]*tally{}
	add := func(block int, mode Mode, rank int) {
		k := key{block, mode}
		if tallies[k] == nil {
			tallies[k] = &tally{}
		}
		tallies[k].add(rank)
	}
	const (
		allTargets = iota
		openTargets
		agentView
	)
	prog.start, prog.last = time.Now(), time.Time{}
	for i, q := range queries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		open := q.Confidence != "quarantined"
		for _, mode := range rep.Modes {
			r, err := rank(q, true, mode)
			if err != nil {
				return rep, fmt.Errorf("%s search: %w", mode, err)
			}
			add(allTargets, mode, r)
			if open {
				add(openTargets, mode, r)
				if r, err = rank(q, false, mode); err != nil {
					return rep, fmt.Errorf("%s search: %w", mode, err)
				}
				add(agentView, mode, r)
			}
		}
		prog.tick("searching", i+1, len(queries))
	}
	for i, title := range []string{
		"all active entries, quarantined ones included; all targets",
		"all active entries, quarantined ones included; targets that are not quarantined",
		"entries that are not quarantined (as search for agents and the hook deliver them); their targets",
	} {
		b := Block{Title: title, Rows: map[Mode]Scores{}}
		for _, mode := range rep.Modes {
			if t := tallies[key{i, mode}]; t != nil {
				b.Rows[mode] = t.scores()
			}
		}
		rep.Blocks = append(rep.Blocks, b)
	}
	return rep, nil
}

// Write prints the report. It holds numbers and names of the settings, no text
// of any entry or quote.
func (r Report) Write(w io.Writer) {
	fmt.Fprintf(w, "search quality on evidence quotes\n")
	fmt.Fprintf(w, "quotes of at least %d characters: %d; drawn: %d (seed %d); distinct target entries: %d; quarantined targets: %d (%s)\n",
		r.MinChars, r.Stock, r.Sampled, r.Seed, r.DistinctTargets, r.QuarantinedTargets, percent(r.QuarantinedTargets, r.Sampled))
	if r.Model == "" {
		fmt.Fprintln(w, "embedding: not configured; only the full-text ranking is measured")
	} else {
		fmt.Fprintf(w, "embedding: %s (text formats %s); current vectors for %d of %d active entries\n", r.Model, r.Format, r.Embedded, r.Active)
		if r.Embedded < r.Active {
			fmt.Fprintf(w, "WARNING: %d active entries have no current vector yet; the vector and hybrid rows understate what the search will do once the job has caught up (see: ctx embed status)\n", r.Active-r.Embedded)
		}
	}
	for _, b := range r.Blocks {
		fmt.Fprintf(w, "\n%s\n", b.Title)
		fmt.Fprintf(w, "%-10s %6s %7s %7s %7s %9s\n", "ranking", "n", "R@1", "R@3", "R@10", "MRR@10")
		for _, mode := range r.Modes {
			s, ok := b.Rows[mode]
			if !ok || s.N == 0 {
				fmt.Fprintf(w, "%-10s %6d %7s %7s %7s %9s\n", mode, 0, "-", "-", "-", "-")
				continue
			}
			fmt.Fprintf(w, "%-10s %6d %6.1f%% %6.1f%% %6.1f%% %8.1f\n", mode, s.N, 100*s.R1, 100*s.R3, 100*s.R10, 100*s.MRR)
		}
	}
}

func percent(n, total int) string {
	if total == 0 {
		return "0 %"
	}
	return fmt.Sprintf("%d %%", n*100/total)
}
