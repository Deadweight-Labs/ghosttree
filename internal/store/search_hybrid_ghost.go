package store

// GhostSearch describes one search over the descriptions of paths.
type GhostSearch struct {
	// Project limits the search to one project; empty means every project.
	Project string
	Limit   int
	// Keep is the row-level rule a caller applies to the answer (what the reader
	// may see, what may be delivered). Hybrid search applies it to both rankings
	// before it fuses them, so a description the reader may not see can neither
	// appear nor move another. It must be the rule the caller applies afterwards.
	// The full-text fallback, as ever, ignores it.
	Keep func(GhostFile) bool
	// Prefilter is a condition in SQL on the alias g of ghost_files that every row
	// Keep lets through satisfies; see Prefilter.
	Prefilter Prefilter
	Mode      SearchMode
}

// SearchGhostFilesWith runs a search over path and description, hybrid when an
// embedding server is configured and ghost descriptions are switched on for
// search: the full-text and the embedding ranking fused by reciprocal rank, the
// descriptions that only the embedding found marked Match "semantic" and at most
// five. Otherwise it is the full-text search alone, as it always was.
func (s *Store) SearchGhostFilesWith(q string, o GhostSearch) ([]GhostFile, error) {
	if s.reader != nil {
		return s.reader.SearchGhostFilesWith(q, o)
	}
	return s.searchGhostFiles(q, o)
}

func (s *Store) searchGhostFiles(q string, o GhostSearch) ([]GhostFile, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 20
	}
	qvec, cfg, err := s.queryVector(q, o.Mode, KindGhost)
	if err != nil {
		return nil, err
	}
	if qvec != nil {
		gs, err := s.hybridGhostFiles(q, o, limit, qvec, cfg)
		switch {
		case err == nil:
			s.embedShared().hybrid.Add(1)
			return gs, nil
		case o.Mode.strict():
			return nil, err
		}
		s.embedShared().logFallback(cfg, err)
	}
	return s.ftsGhostFiles(q, o.Project, limit)
}

func (s *Store) ghostFilesByID(ids []int64) (map[int64]GhostFile, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT `+ghostCols+` FROM ghost_files WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	gs, err := scanGhostFiles(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]GhostFile, len(gs))
	for _, g := range gs {
		out[g.ID] = g
	}
	return out, nil
}

func (s *Store) hybridGhostFiles(q string, o GhostSearch, limit int, qvec []float32, cfg *EmbedSearchConfig) ([]GhostFile, error) {
	pool := poolFor(limit)
	where, args := o.Prefilter.with(`(? = '' OR g.project = ?)`, []any{o.Project, o.Project})
	var textIDs []int64
	if o.Mode != SearchVectorOnly {
		scan := pool
		if o.Keep != nil {
			scan = keepScanCap
		}
		rows, err := s.db.Query(`SELECT g.id FROM ghost_files_fts f JOIN ghost_files g ON g.id = f.rowid
			WHERE ghost_files_fts MATCH ? AND `+where+` ORDER BY f.rank LIMIT ?`,
			append(append([]any{ftsQuery(q)}, args...), scan)...)
		if err != nil {
			return nil, err
		}
		if textIDs, err = scanIDs(rows); err != nil {
			return nil, err
		}
	}
	k, err := embedKindOf(KindGhost)
	if err != nil {
		return nil, err
	}
	vecIDs, err := s.vectorIDs(k, cfg, qvec, where, args)
	if err != nil {
		return nil, err
	}
	// The row rule is the expensive part: read only as deep as the full-text
	// ranking is read.
	if o.Keep != nil && len(vecIDs) > keepScanCap {
		vecIDs = vecIDs[:keepScanCap]
	}
	room := maxSemanticOnly(limit)
	if o.Mode == SearchVectorOnly {
		room = limit
	}
	return hybridRows(hybridKind[GhostFile]{
		id:       func(g GhostFile) int64 { return g.ID },
		semantic: func(g *GhostFile) { g.Match = MatchSemantic },
		load:     s.ghostFilesByID,
	}, textIDs, vecIDs, pool, limit, room, o.Keep)
}
