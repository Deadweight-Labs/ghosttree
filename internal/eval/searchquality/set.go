package searchquality

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// SetQuery is one query of a set with the object that is its right answer. The
// file format is one JSON object per line:
//
//	{"query": "...", "kind": "request|ghost|pain|document|knowledge", "target": "..."}
//
// target is the id of the object; for a ghost description it is "project:path",
// split at the first colon. Further members are ignored.
type SetQuery struct {
	Query  string `json:"query"`
	Kind   string `json:"kind"`
	Target string `json:"target"`

	// Label is the heading of the block in the report; empty means the kind.
	Label string `json:"-"`
	// Exclude is the id of an object that is not an answer to the query, because
	// the query is made from it (the title of a request, when the other end of a
	// duplicate relation is wanted).
	Exclude int64 `json:"-"`
}

func (q SetQuery) label() string {
	if q.Label != "" {
		return q.Label
	}
	return q.Kind
}

// ParseSet reads a query set. The error names a line and what is wrong with it,
// never the text of a query.
func ParseSet(r io.Reader) ([]SetQuery, error) {
	var out []SetQuery
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for line := 1; sc.Scan(); line++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var q SetQuery
		if err := json.Unmarshal([]byte(raw), &q); err != nil {
			return nil, fmt.Errorf("line %d: not a JSON object with query, kind and target", line)
		}
		kind, err := store.ParseEmbedKind(q.Kind)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		q.Kind = kind
		q.Query, q.Target = strings.TrimSpace(q.Query), strings.TrimSpace(q.Target)
		if q.Query == "" || q.Target == "" {
			return nil, fmt.Errorf("line %d: query and target must not be empty", line)
		}
		if _, _, err := splitTarget(q); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, q)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the set: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("the set holds no query")
	}
	return out, nil
}

// splitTarget reads the target of a query: an id, or project and path.
func splitTarget(q SetQuery) (id int64, ghost [2]string, err error) {
	if q.Kind == store.KindGhost {
		project, path, ok := strings.Cut(q.Target, ":")
		if !ok || project == "" {
			return 0, ghost, errors.New(`the target of a ghost query is "project:path"`)
		}
		return 0, [2]string{project, path}, nil
	}
	id, err = strconv.ParseInt(q.Target, 10, 64)
	if err != nil || id <= 0 {
		return 0, ghost, fmt.Errorf("the target of a %s query is an id", q.Kind)
	}
	return id, ghost, nil
}

// RequestSet draws the queries the request ledger provides (see
// store.RequestQueries): the sighting quotes and the titles of requests that
// duplicate or supersede another. sample is the most of each; a larger stock is
// drawn from with the seed. They come in two blocks, since a quote and a title
// are different questions.
func RequestSet(st *store.Store, minChars, sample int, seed int64) ([]SetQuery, error) {
	all, err := st.RequestQueries(minChars)
	if err != nil {
		return nil, err
	}
	bySource := map[string][]store.RequestQuery{}
	for _, q := range all {
		bySource[q.Source] = append(bySource[q.Source], q)
	}
	var out []SetQuery
	for _, source := range []struct{ key, label string }{
		{store.RequestQuerySighting, "requests: quotes of the sessions they came from"},
		{store.RequestQueryRelation, "requests: the title of a duplicate or successor, the older request wanted"},
	} {
		qs := bySource[source.key]
		if sample > 0 && len(qs) > sample {
			rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
			picked := make([]store.RequestQuery, 0, sample)
			for _, i := range rng.Perm(len(qs))[:sample] {
				picked = append(picked, qs[i])
			}
			qs = picked
		}
		for _, q := range qs {
			out = append(out, SetQuery{Query: q.Query, Kind: store.KindRequest, Target: strconv.FormatInt(q.Target, 10),
				Label: source.label, Exclude: q.Exclude})
		}
	}
	return out, nil
}

// SetBlock is one group of a set report: the queries of a kind, or of one of the
// built-in sets.
type SetBlock struct {
	Label, Kind string
	// Queries is the number of queries, Missing how many of them have a target
	// that does not exist (any more) or cannot be found by any search (an archived
	// document); those are not measured.
	Queries, Missing int
	// Embedded of Active objects of the kind have a current vector.
	Embedded, Active int
	Rows             map[Mode]Scores
}

// SetReport is the result of RunSet.
type SetReport struct {
	Model, Format string
	Modes         []Mode
	Blocks        []SetBlock
}

// target is what a query's right answer is, looked up.
type target struct {
	id      int64
	project string
	path    string
	ok      bool
}

func resolveTarget(st *store.Store, q SetQuery) (target, error) {
	id, ghost, err := splitTarget(q)
	if err != nil {
		return target{}, err
	}
	var t target
	switch q.Kind {
	case store.KindKnowledge:
		k, err := st.KnowledgeByID(id)
		t = target{id: id, ok: err == nil && k.Status == "active"}
		return t, notFound(err)
	case store.KindRequest:
		d, err := st.RequestByID(id)
		return target{id: id, project: d.Request.Scope.Project, ok: err == nil}, notFound(err)
	case store.KindPain:
		p, err := st.PainByID(id)
		return target{id: id, project: p.Project, ok: err == nil}, notFound(err)
	case store.KindDocument:
		d, err := st.DocumentByID(id)
		return target{id: id, project: d.Project, ok: err == nil && d.Status == "active"}, notFound(err)
	case store.KindGhost:
		g, err := st.GhostFileByPath(ghost[0], ghost[1])
		return target{id: g.ID, project: g.Project, path: g.Path, ok: err == nil}, notFound(err)
	}
	return target{}, fmt.Errorf("unknown kind %q", q.Kind)
}

// notFound turns "there is no such object" into no error: a set may name an
// object that has been deleted since, and that is counted, not fatal.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// searchIDs runs the production search of the kind, scoped the way a reader
// asks it (to the project of the target), and returns the ids of the first ten
// hits in order.
func searchIDs(st *store.Store, q SetQuery, t target, mode Mode) ([]int64, error) {
	const limit = 10
	m := mode.searchMode()
	// A hit the query was made from is not an answer to it. Ask for one more, so
	// that excluding it still leaves ten.
	ids := func(n int, id func(i int) int64) []int64 {
		out := make([]int64, 0, n)
		for i := range n {
			if v := id(i); v != q.Exclude || q.Exclude == 0 {
				out = append(out, v)
			}
		}
		return out
	}
	switch q.Kind {
	case store.KindKnowledge:
		hits, err := st.SearchKnowledgeWith(q.Query, store.KnowledgeSearch{Operator: true, ActiveOnly: true, NoRecord: true, Limit: limit, Mode: m})
		return ids(len(hits), func(i int) int64 { return hits[i].ID }), err
	case store.KindRequest:
		page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Query: q.Query, Scope: scope.Axes{Project: t.project}, Limit: limit + 1}, m)
		if err != nil {
			return nil, err
		}
		out := ids(len(page.Results), func(i int) int64 { return page.Results[i].Request.ID })
		return out[:min(len(out), limit)], nil
	case store.KindPain:
		hits, err := st.SearchPainsWith(pain.Filter{Project: t.project, State: "all", Query: q.Query, Limit: limit}, store.PainSearch{Mode: m})
		return ids(len(hits), func(i int) int64 { return hits[i].ID }), err
	case store.KindDocument:
		hits, err := st.SearchDocuments(q.Query, store.DocumentSearch{Project: t.project, Limit: limit, Mode: m})
		return ids(len(hits), func(i int) int64 { return hits[i].ID }), err
	case store.KindGhost:
		hits, err := st.SearchGhostFilesWith(q.Query, store.GhostSearch{Project: t.project, Limit: limit, Mode: m})
		return ids(len(hits), func(i int) int64 { return hits[i].ID }), err
	}
	return nil, fmt.Errorf("unknown kind %q", q.Kind)
}

// RunSet measures the full-text, the embedding and the fused ranking on a set of
// queries with known answers, by calling the search function of the server for
// each kind, scoped to the project of the answer (the way an agent asks). It
// reads only: st must be opened read-only. The report holds counts and rates
// only, no query and no text of an object.
func RunSet(ctx context.Context, st *store.Store, opts Options, queries []SetQuery) (SetReport, error) {
	opts = opts.withDefaults()
	var rep SetReport
	rep.Modes = []Mode{FullText}
	prog := &progress{w: opts.Progress, every: opts.Every, start: time.Now()}
	if opts.Embedder != nil {
		rep.Model, rep.Format = opts.Embedder.Model(), opts.Embedder.PrefixVersion()
		rep.Modes = append(rep.Modes, Vector, Hybrid)
		c := &cache{inner: opts.Embedder, vecs: map[string][]float32{}}
		seen := map[string]bool{}
		var distinct []string
		for _, q := range queries {
			if !seen[q.Query] {
				seen[q.Query] = true
				distinct = append(distinct, q.Query)
			}
		}
		for i, q := range distinct {
			if _, err := c.Query(ctx, q); err != nil {
				return rep, fmt.Errorf("embedding query %d of %d: %w", i+1, len(distinct), err)
			}
			prog.tick("embedding the queries", i+1, len(distinct))
			if opts.Pause > 0 && i+1 < len(distinct) {
				select {
				case <-ctx.Done():
					return rep, ctx.Err()
				case <-time.After(opts.Pause):
				}
			}
		}
		// Every kind is measured, whether or not the server uses its vectors in the
		// search: that is what the measurement is for.
		st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, Timeout: time.Minute, MinSimilarity: opts.MinSimilarity, Kinds: store.EmbedKinds()})
	}

	type blockKey struct{ label, kind string }
	var order []blockKey
	blocks := map[blockKey]*SetBlock{}
	tallies := map[blockKey]map[Mode]*tally{}
	for _, q := range queries {
		k := blockKey{q.label(), q.Kind}
		if blocks[k] == nil {
			blocks[k] = &SetBlock{Label: k.label, Kind: k.kind, Rows: map[Mode]Scores{}}
			tallies[k] = map[Mode]*tally{}
			order = append(order, k)
		}
	}
	// Blocks in the order of the kinds, the built-in sets after their kind's own.
	slices.SortStableFunc(order, func(a, b blockKey) int {
		return slices.Index(store.EmbedKinds(), a.kind) - slices.Index(store.EmbedKinds(), b.kind)
	})
	prog.start, prog.last = time.Now(), time.Time{}
	for i, q := range queries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		k := blockKey{q.label(), q.Kind}
		blocks[k].Queries++
		t, err := resolveTarget(st, q)
		if err != nil {
			return rep, fmt.Errorf("looking up a target of kind %s: %w", q.Kind, err)
		}
		if !t.ok {
			blocks[k].Missing++
			continue
		}
		for _, mode := range rep.Modes {
			ids, err := searchIDs(st, q, t, mode)
			if err != nil {
				return rep, fmt.Errorf("%s search of %s: %w", mode, q.Kind, err)
			}
			rank := 0
			if at := slices.Index(ids, t.id); at >= 0 {
				rank = at + 1
			}
			if tallies[k][mode] == nil {
				tallies[k][mode] = &tally{}
			}
			tallies[k][mode].add(rank)
		}
		prog.tick("searching", i+1, len(queries))
	}
	var statuses = map[string]store.EmbedStatus{}
	for _, k := range order {
		b := blocks[k]
		for mode, t := range tallies[k] {
			b.Rows[mode] = t.scores()
		}
		if opts.Embedder != nil {
			if _, done := statuses[k.kind]; !done {
				s, err := st.EmbeddingStatus(k.kind, store.EmbedModel{Model: rep.Model, PrefixVersion: rep.Format})
				if err != nil {
					return rep, err
				}
				statuses[k.kind] = s
			}
			b.Embedded, b.Active = statuses[k.kind].Current, statuses[k.kind].Active
		}
		rep.Blocks = append(rep.Blocks, *b)
	}
	return rep, nil
}

// Write prints the report: numbers and the names of the settings, no query and no
// text of any object.
func (r SetReport) Write(w io.Writer) {
	fmt.Fprintf(w, "search quality on query sets\n")
	if r.Model == "" {
		fmt.Fprintln(w, "embedding: not configured; only the full-text ranking is measured")
	} else {
		fmt.Fprintf(w, "embedding: %s (text formats %s)\n", r.Model, r.Format)
	}
	for _, b := range r.Blocks {
		fmt.Fprintf(w, "\n%s: %d queries", b.Label, b.Queries)
		if b.Missing > 0 {
			fmt.Fprintf(w, ", %d of them not measured (the target does not exist any more or cannot be found, e.g. an archived document)", b.Missing)
		}
		fmt.Fprintln(w)
		if r.Model != "" {
			fmt.Fprintf(w, "current vectors for %d of %d active %s objects\n", b.Embedded, b.Active, b.Kind)
			if b.Embedded < b.Active {
				fmt.Fprintf(w, "WARNING: %d %s objects have no current vector yet; the vector and hybrid rows understate what the search will do once the job has caught up (see: ctx embed status)\n", b.Active-b.Embedded, b.Kind)
			}
		}
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
