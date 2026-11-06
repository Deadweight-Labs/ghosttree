package store

import (
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// SearchPains lists pains of a project for a viewer. Without a state filter it
// returns active and mitigated; "all" returns every state. With a query it is a
// hybrid search when an embedding server is configured and pains are switched on
// for search (see SearchPainsWith).
func (a *ProjectAccess) SearchPains(f pain.Filter) ([]pain.Pain, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.RequestID != 0 {
		// An edge to a request the viewer cannot read must not be provable
		// through the filter; over several projects, a project in which it cannot
		// be read is left out of the search.
		if len(f.Projects) == 0 {
			if _, err := a.resolveLinkTarget(f.Project, "request", f.RequestID); err != nil {
				return []pain.Pain{}, nil
			}
		} else {
			var open []string
			for _, p := range f.Projects {
				if _, err := a.resolveLinkTarget(p, "request", f.RequestID); err == nil {
					open = append(open, p)
				}
			}
			if len(open) == 0 {
				return []pain.Pain{}, nil
			}
			f.Projects = open
		}
	}
	return a.st.SearchPainsWith(f, PainSearch{Keep: a.CanSeePain})
}

// PainSearch describes one search over the pains of a project.
type PainSearch struct {
	// Keep is the row-level rule a caller applies to the answer (what the reader
	// may see). Hybrid search applies it to both rankings before it fuses them, so
	// a pain the reader may not see can neither appear nor move another. Nil keeps
	// every pain, which is what the quality measurement wants.
	Keep func(pain.Pain) bool
	Mode SearchMode
}

// SearchPainsWith lists the pains of f.Project (or of f.Projects, all of them in
// one search), at most f.Limit. A query that is found in a title or text word for
// word is the full-text ranking, which has no order of its own: the pains come in
// the order of their state. With an embedding server configured and pains switched
// on for search, that list and the ranking by the cosine of the stored vectors are
// fused by reciprocal rank, and the pains that only the embedding found are marked
// Match "semantic", at most five. Over several projects there is one embedding of
// the query and one share of those hits for the whole list, not one per project.
// Without a query, or wherever the embedding is not used, the answer is the list
// it always was.
func (s *Store) SearchPainsWith(f pain.Filter, o PainSearch) ([]pain.Pain, error) {
	if s.reader != nil {
		return s.reader.SearchPainsWith(f, o)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if strings.TrimSpace(f.Query) != "" {
		qvec, cfg, err := s.queryVector(f.Query, o.Mode, KindPain)
		if err != nil {
			return nil, err
		}
		if qvec != nil {
			out, err := s.hybridPains(f, o, qvec, cfg)
			switch {
			case err == nil:
				s.embedShared().hybrid.Add(1)
				return out, nil
			case o.Mode.strict():
				return nil, err
			}
			s.embedShared().logFallback(cfg, err)
		}
	}
	rows, err := s.painRows(f)
	if err != nil {
		return nil, err
	}
	out := make([]pain.Pain, 0, len(rows))
	for _, p := range rows {
		if o.Keep == nil || o.Keep(p) {
			out = append(out, p)
			if len(out) >= f.Limit {
				break
			}
		}
	}
	return out, nil
}

func likeEscape(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// painWhere is the part of a pain query that every ranking shares: the project
// or projects, the state and the request an edge leads to, on the alias p.
func painWhere(f pain.Filter) ([]string, []any) {
	where, args := []string{`p.project=?`}, []any{f.Project}
	if len(f.Projects) > 0 {
		where, args = []string{`p.project IN (` + placeholders(len(f.Projects)) + `)`}, nil
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	switch f.State {
	case "", "open":
		where = append(where, `p.state IN ('active','mitigated')`)
	case "all":
	default:
		where = append(where, `p.state=?`)
		args = append(args, f.State)
	}
	if f.RequestID != 0 {
		where = append(where, `EXISTS(SELECT 1 FROM entity_links l WHERE l.src_kind='pain' AND l.src_id=p.id AND l.dst_kind='request' AND l.dst_id=?)`)
		args = append(args, f.RequestID)
	}
	return where, args
}

func (s *Store) painRows(f pain.Filter) ([]pain.Pain, error) {
	if s.reader != nil {
		return s.reader.painRows(f)
	}
	where, args := painWhere(f)
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, `(p.title LIKE ? ESCAPE '\' OR p.body LIKE ? ESCAPE '\')`)
		like := "%" + likeEscape(q) + "%"
		args = append(args, like, like)
	}
	limit := f.Limit * 4
	if limit < 200 {
		limit = 200
	}
	// Over several projects the pains come project after project, in the order the
	// projects were given, as they did when each project was asked for in turn.
	order := ""
	if len(f.Projects) > 1 {
		order = `CASE p.project`
		for i, p := range f.Projects {
			order += ` WHEN ? THEN ` + strconv.Itoa(i)
			args = append(args, p)
		}
		order += ` ELSE ` + strconv.Itoa(len(f.Projects)) + ` END, `
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+painCols+painFrom+` WHERE `+strings.Join(where, ` AND `)+
		` ORDER BY `+order+`CASE p.state WHEN 'active' THEN 0 WHEN 'mitigated' THEN 1 WHEN 'resolved' THEN 2 ELSE 3 END, p.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPains(rows)
}

func (s *Store) painsByID(ids []int64) (map[int64]pain.Pain, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT `+painCols+painFrom+` WHERE p.id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanPains(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]pain.Pain, len(list))
	for _, p := range list {
		out[p.ID] = p
	}
	return out, nil
}

func (s *Store) hybridPains(f pain.Filter, o PainSearch, qvec []float32, cfg *EmbedSearchConfig) ([]pain.Pain, error) {
	pool := poolFor(f.Limit)
	var textIDs []int64
	if o.Mode != SearchVectorOnly {
		tf := f
		tf.Limit = keepScanCap
		rows, err := s.painRows(tf)
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			textIDs = append(textIDs, p.ID)
		}
	}
	where, args := painWhere(f)
	k, err := embedKindOf(KindPain)
	if err != nil {
		return nil, err
	}
	vecIDs, err := s.vectorIDs(k, cfg, qvec, strings.Join(where, " AND "), args)
	if err != nil {
		return nil, err
	}
	if o.Keep != nil && len(vecIDs) > keepScanCap {
		vecIDs = vecIDs[:keepScanCap]
	}
	room := maxSemanticOnly(f.Limit)
	if o.Mode == SearchVectorOnly {
		room = f.Limit
	}
	return hybridRows(hybridKind[pain.Pain]{
		id:       func(p pain.Pain) int64 { return p.ID },
		semantic: func(p *pain.Pain) { p.Match = MatchSemantic },
		load:     s.painsByID,
	}, textIDs, vecIDs, pool, f.Limit, room, o.Keep)
}
