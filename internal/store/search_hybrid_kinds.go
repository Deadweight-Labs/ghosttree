package store

import "slices"

// Prefilter is a condition in SQL that every row a reader's row rule lets through
// satisfies, on the alias of the table of objects that the search names. Hybrid
// search adds it to the query of both rankings, so what a reader has no access to
// at all is neither compared nor handed to the row rule. It must never be
// narrower than that rule; the rule stays the exact one. The zero value admits
// everything.
type Prefilter struct {
	SQL  string
	Args []any
}

// with adds a prefilter to a condition and its arguments.
func (p Prefilter) with(where string, args []any) (string, []any) {
	if p.SQL == "" {
		return where, args
	}
	if where == "" {
		return "(" + p.SQL + ")", slices.Clone(p.Args)
	}
	return "(" + where + ") AND (" + p.SQL + ")", append(slices.Clone(args), p.Args...)
}

// vectorIDs ranks the objects of a kind by the cosine of their stored vector and
// the query vector, best first. The objects are the active ones of the kind, those
// with a vector of the configured model and text format that satisfy cond (SQL on
// the alias of the objects, with args); nothing else is read, let alone compared.
// A vector never counts without its object: one left behind by an object that is
// gone or archived is not a hit.
func (s *Store) vectorIDs(k embedKind, cfg *EmbedSearchConfig, qvec []float32, cond string, args []any) ([]int64, error) {
	query := `SELECT e.` + k.idCol + `, e.scale, e.vec FROM ` + k.emb + ` e JOIN ` + k.from + ` ON ` + k.id + ` = e.` + k.idCol + `
		WHERE ` + k.keyWhere("e") + `e.model = ? AND e.prefix_version = ? AND ` + k.active
	if cond != "" {
		query += ` AND (` + cond + `)`
	}
	rows, err := s.db.Query(query, append([]any{cfg.Embedder.Model(), cfg.Embedder.PrefixVersion()}, args...)...)
	if err != nil {
		return nil, err
	}
	return s.rankByCosine(rows, qvec, cfg)
}

// keptByID loads the rows for ids in the given order and returns the first want
// of those that pass keep. Without keep only the first want are loaded; with it
// the rest are loaded in chunks until enough have passed. load returns the rows
// it finds, in any order; an id without a row is skipped.
func keptByID[T any](ids []int64, want int, load func(part []int64) (map[int64]T, error), keep func(T) bool) ([]T, error) {
	var out []T
	chunk := loadChunk
	if keep == nil {
		chunk = max(want, 1)
	}
	for start := 0; start < len(ids) && len(out) < want; start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		rows, err := load(part)
		if err != nil {
			return nil, err
		}
		for _, id := range part {
			row, ok := rows[id]
			if !ok || (keep != nil && !keep(row)) {
				continue
			}
			out = append(out, row)
			if len(out) >= want {
				break
			}
		}
	}
	return out, nil
}

// hybridKind says how to handle the rows of one kind in a fusion.
type hybridKind[T any] struct {
	id       func(T) int64
	semantic func(*T)
	load     func(part []int64) (map[int64]T, error)
}

// hybridRows is the fusion of a search of any kind: both rankings are cut to the
// rows keep allows (so that a row the reader may not see can neither appear nor
// move another row), at most pool rows each, and fused by reciprocal rank; the
// first depth rows come back, those found by the embedding alone marked, and at
// most room of them.
func hybridRows[T any](h hybridKind[T], textIDs, vecIDs []int64, pool, depth, room int, keep func(T) bool) ([]T, error) {
	text, err := keptByID(textIDs, pool, h.load, keep)
	if err != nil {
		return nil, err
	}
	vec, err := keptByID(vecIDs, pool, h.load, keep)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]T, len(text)+len(vec))
	idsOf := func(rows []T) []int64 {
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = h.id(r)
			byID[ids[i]] = r
		}
		return ids
	}
	textKept, vecKept := idsOf(text), idsOf(vec)
	out := []T{}
	for _, f := range fuseRankings(textKept, vecKept, depth, room) {
		row := byID[f.ID]
		if f.Semantic {
			h.semantic(&row)
		}
		out = append(out, row)
	}
	return out, nil
}

// poolFor is how many rows each ranking contributes to a fusion that wants the
// first depth entries: the usual pool at least, more when a later page needs it,
// never beyond what the row rule is willing to scan.
func poolFor(depth int) int { return min(max(hybridPool, depth), keepScanCap) }

// EmbedSearches says whether the search of a kind uses the vectors: an embedding
// server is configured and the kind is switched on for search.
func (s *Store) EmbedSearches(kind string) bool {
	cfg := s.embedShared().cfg.Load()
	return cfg != nil && cfg.searches(kind)
}
