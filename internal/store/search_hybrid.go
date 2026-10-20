package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// MatchSemantic marks a search hit that only the embedding found: its words do
// not match the query, its meaning does. A hit the full-text index found has no
// marker.
const MatchSemantic = "semantic"

// SearchMode picks the rankings a knowledge search uses. Production is always
// SearchAuto; the other modes exist for the quality measurement, which has to
// call the production code on one ranking at a time and must never be answered
// by the fallback.
type SearchMode int

const (
	// SearchAuto fuses the full-text and the embedding ranking when an embedding
	// server is configured and answers in time, and uses the full-text ranking
	// alone otherwise. Without configuration it is exactly the search of
	// ghosttree without embeddings.
	SearchAuto SearchMode = iota
	// SearchFullTextOnly is the full-text ranking alone.
	SearchFullTextOnly
	// SearchVectorOnly is the embedding ranking alone; an unavailable server is
	// an error.
	SearchVectorOnly
	// SearchHybridStrict is SearchAuto without the fallback: an unavailable
	// server is an error.
	SearchHybridStrict
)

func (m SearchMode) strict() bool { return m == SearchVectorOnly || m == SearchHybridStrict }

// KnowledgeSearch describes one search over knowledge entries. SearchKnowledge,
// SearchKnowledgeForContext and SearchAllKnowledge are its three common shapes.
type KnowledgeSearch struct {
	Axes scope.Axes
	// Union matches the axes the way a session reads them (global, machine,
	// project and their combinations) instead of exactly as given.
	Union bool
	// Operator is the operator view: quarantined and inactive entries are not
	// left out of the full-text ranking, and no use is counted.
	Operator bool
	// ActiveOnly restricts the operator view to active entries.
	ActiveOnly bool
	// NoRecord keeps the agent view from counting its hits as use.
	NoRecord bool
	Limit    int
	Viewer   SessionViewer
	// Keep is the row-level rule a caller applies to the answer on top of the
	// query (what the reader may see, what may be delivered, filters of a page).
	// Hybrid search applies it to both rankings before it fuses them, so a row
	// the reader may not see can neither appear nor move another row. It must be
	// the same rule the caller applies afterwards. The full-text search alone
	// ignores it, as it always did.
	Keep func(Knowledge) bool
	// Prefilter is a condition in SQL that every row Keep lets through satisfies.
	// Hybrid search adds it to the query of both rankings, so what a reader has no
	// access to at all is neither compared nor handed to Keep. It must never be
	// narrower than Keep; Keep stays the exact rule.
	Prefilter KnowledgePrefilter
	Mode      SearchMode
}

// KnowledgePrefilter is a SQL condition on the table alias k of knowledge with
// its arguments. The zero value admits everything.
type KnowledgePrefilter struct {
	SQL  string
	Args []any
}

const (
	// hybridPool is how many entries each ranking contributes to the fusion.
	hybridPool = 100
	// keepScanCap bounds how deep the full-text ranking is read to find enough
	// rows that pass Keep.
	keepScanCap = 1000
	// loadChunk is how many rows are loaded at a time while rows are being kept.
	loadChunk = 64
)

var errNoEmbedder = errors.New("no embedding server is configured")

// SearchKnowledgeWith runs a knowledge search, hybrid when an embedding server
// is configured. See KnowledgeSearch.
func (s *Store) SearchKnowledgeWith(q string, o KnowledgeSearch) ([]Knowledge, error) {
	if s.reader != nil {
		return s.reader.SearchKnowledgeWith(q, o)
	}
	return s.searchKnowledgeWith(q, o)
}

func firstViewer(v []SessionViewer) SessionViewer {
	if len(v) == 0 {
		return SessionViewer{}
	}
	return v[0]
}

func (s *Store) searchKnowledgeWith(q string, o KnowledgeSearch) ([]Knowledge, error) {
	var where string
	var args []any
	if o.Union {
		where, args = o.Axes.UnionWhere()
	} else {
		where, args = o.Axes.FilterWhere()
	}
	where, args = withGlobalScope(where, args, "k", []SessionViewer{o.Viewer})
	limit := o.Limit
	if limit <= 0 {
		limit = 20
		if o.Operator {
			limit = 50
		}
	}

	var qvec []float32
	sh := s.embedShared()
	cfg := sh.cfg.Load()
	switch {
	case o.Mode == SearchFullTextOnly:
	case cfg == nil:
		if o.Mode.strict() {
			return nil, errNoEmbedder
		}
	case o.Mode == SearchAuto && isListingQuery(q):
		// A question made of ordinary words asks for a list, which no ranking
		// answers; the full-text search says so by finding nothing.
	default:
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		v, err := cfg.Embedder.Query(ctx, q)
		cancel()
		switch {
		case err == nil:
			qvec = v
		case o.Mode.strict():
			return nil, fmt.Errorf("embedding the query: %w", err)
		default:
			sh.logFallback(cfg, err)
		}
	}

	var ks []Knowledge
	var err error
	if qvec != nil {
		hwhere, hargs := where, args
		if o.Prefilter.SQL != "" {
			hwhere = "(" + where + ") AND (" + o.Prefilter.SQL + ")"
			hargs = append(slices.Clone(args), o.Prefilter.Args...)
		}
		ks, err = s.hybridKnowledge(q, hwhere, hargs, limit, qvec, cfg, o)
		switch {
		case err == nil:
			sh.hybrid.Add(1)
		case !o.Mode.strict():
			// The vectors are an addition; whatever goes wrong with them, the
			// caller gets the answer the full-text search gives. A failure of the
			// full-text part itself comes back from that search.
			sh.logFallback(cfg, err)
			qvec = nil
		}
	}
	if qvec == nil {
		ks, err = s.ftsKnowledge(q, where, args, limit, o)
	}
	if err != nil {
		return nil, err
	}
	if !o.Operator && !o.NoRecord {
		s.recordKnowledgeSearchHit(ks)
	}
	return ks, nil
}

// ftsConditions are the status rules of the two views.
func ftsConditions(o KnowledgeSearch) string {
	switch {
	case !o.Operator:
		return ` AND k.status = 'active' AND k.confidence != 'quarantined'`
	case o.ActiveOnly:
		return ` AND k.status = 'active'`
	}
	return ""
}

// ftsKnowledge is the full-text ranking as it has always been answered.
func (s *Store) ftsKnowledge(q, where string, args []any, limit int, o KnowledgeSearch) ([]Knowledge, error) {
	args = append([]any{ftsQuery(q)}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(knowledgeCols, "k.")+`
		FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND `+where+ftsConditions(o)+`
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}

func (s *Store) hybridKnowledge(q, where string, args []any, limit int, qvec []float32, cfg *EmbedSearchConfig, o KnowledgeSearch) ([]Knowledge, error) {
	pool := max(hybridPool, limit)
	var text, vec []Knowledge
	var err error
	if o.Mode != SearchVectorOnly {
		if text, err = s.textRanking(q, where, args, pool, o); err != nil {
			return nil, err
		}
	}
	if vec, err = s.vectorRanking(qvec, cfg, where, args, pool, o); err != nil {
		return nil, err
	}
	byID := make(map[int64]Knowledge, len(text)+len(vec))
	idsOf := func(ks []Knowledge) []int64 {
		ids := make([]int64, len(ks))
		for i, k := range ks {
			ids[i], byID[k.ID] = k.ID, k
		}
		return ids
	}
	textIDs, vecIDs := idsOf(text), idsOf(vec)
	// Hits that only the embedding found are neighbours, not matches: show a few
	// of them, so that a search that finds two entries by their words does not
	// come back padded to the limit. The ranking alone (the measurement) is not
	// capped.
	room := maxSemanticOnly(limit)
	if o.Mode == SearchVectorOnly {
		room = limit
	}
	out := []Knowledge{}
	for _, f := range embed.Fuse(textIDs, vecIDs) {
		if len(out) >= limit {
			break
		}
		k := byID[f.ID]
		if f.Ranks[0] == 0 {
			if room == 0 {
				continue
			}
			room--
			k.Match = MatchSemantic
		}
		out = append(out, k)
	}
	return out, nil
}

// maxSemanticHits is the most entries one search shows that only the embedding
// found.
const maxSemanticHits = 5

// maxSemanticOnly is the share of a result of the given size that may be
// semantic-only hits: five, or half of a short result.
func maxSemanticOnly(limit int) int { return min(maxSemanticHits, (limit+1)/2) }

// textRanking is the full-text ranking, cut to the rows Keep allows.
func (s *Store) textRanking(q, where string, args []any, pool int, o KnowledgeSearch) ([]Knowledge, error) {
	scan := pool
	if o.Keep != nil {
		scan = keepScanCap
	}
	args = append([]any{ftsQuery(q)}, args...)
	args = append(args, scan)
	rows, err := s.db.Query(`SELECT k.id FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
		WHERE knowledge_fts MATCH ? AND `+where+ftsConditions(o)+`
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	return s.keptRows(ids, pool, o.Keep)
}

// vectorRanking ranks the active entries the reader may reach by the cosine of
// their stored vector and the query vector. The scope and the global-entry rule
// are part of the query, exactly as in the full-text ranking, so nothing outside
// them is even compared; Keep then removes what the row-level rule hides before
// a rank is given.
func (s *Store) vectorRanking(qvec []float32, cfg *EmbedSearchConfig, where string, args []any, pool int, o KnowledgeSearch) ([]Knowledge, error) {
	query := `SELECT e.knowledge_id, e.scale, e.vec FROM knowledge_embeddings e
		JOIN knowledge k ON k.id = e.knowledge_id
		WHERE e.model = ? AND e.prefix_version = ? AND k.status = 'active'`
	if !o.Operator {
		query += ` AND k.confidence != 'quarantined'`
	}
	rows, err := s.db.Query(query+` AND `+where, append([]any{cfg.Embedder.Model(), cfg.Embedder.PrefixVersion()}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		id    int64
		score float32
	}
	var all []scored
	mismatched := 0
	for rows.Next() {
		var id int64
		var scale float64
		var raw sql.RawBytes
		if err := rows.Scan(&id, &scale, &raw); err != nil {
			return nil, err
		}
		if len(raw) != len(qvec) {
			mismatched++
			continue
		}
		if score := embed.Cosine(qvec, raw, float32(scale)); cfg.MinSimilarity <= 0 || score >= cfg.MinSimilarity {
			all = append(all, scored{id, score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if mismatched > 0 {
		s.embedShared().warnDimension(cfg, mismatched, len(qvec))
	}
	slices.SortFunc(all, func(a, b scored) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	// The row rule is the expensive part: read only as deep as the full-text
	// ranking is read, so a reader with little to see does not make every search
	// check the whole stock.
	if o.Keep != nil && len(all) > keepScanCap {
		all = all[:keepScanCap]
	}
	ids := make([]int64, len(all))
	for i, a := range all {
		ids[i] = a.id
	}
	return s.keptRows(ids, pool, o.Keep)
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// keptRows loads the entries for ids in the given order and returns the first
// want of those that pass keep. Without keep only the first want are loaded;
// with it the rest are loaded in chunks until enough have passed.
func (s *Store) keptRows(ids []int64, want int, keep func(Knowledge) bool) ([]Knowledge, error) {
	var out []Knowledge
	chunk := loadChunk
	if keep == nil {
		chunk = want
	}
	for start := 0; start < len(ids) && len(out) < want; start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		loaded, err := s.scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		byID := make(map[int64]Knowledge, len(loaded))
		for _, k := range loaded {
			byID[k.ID] = k
		}
		for _, id := range part {
			k, ok := byID[id]
			if !ok || (keep != nil && !keep(k)) {
				continue
			}
			out = append(out, k)
			if len(out) >= want {
				break
			}
		}
	}
	return out, nil
}
