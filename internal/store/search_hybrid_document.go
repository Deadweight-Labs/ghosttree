package store

import "strings"

// DocumentSearch describes one search over the long-form documents.
type DocumentSearch struct {
	// Project limits the search to one project; empty means every project.
	Project string
	// Kind limits it to one kind of document (spec, plan, ...); empty means all.
	Kind  string
	Limit int
	// Keep is the row-level rule a caller applies to the answer (what the reader
	// may see). Both rankings are cut to the rows it allows before they are fused,
	// so a document the reader may not see can neither appear nor move another.
	// Nil keeps every document.
	Keep func(Document) bool
	// Prefilter is a condition in SQL on the alias d of documents that every row
	// Keep lets through satisfies; see Prefilter.
	Prefilter Prefilter
	Mode      SearchMode
}

// SearchDocuments searches the active documents by title, slug and the text of
// the head revision (full text, ranked by bm25) and, with an embedding server
// configured and documents switched on for search, by the meaning of the title
// and the beginning of the text; the two rankings are fused by reciprocal rank,
// and the documents that only the embedding found are marked Match "semantic",
// at most five. An archived document is never a hit. The answer holds no text,
// only the documents as ListDocuments gives them.
func (s *Store) SearchDocuments(q string, o DocumentSearch) ([]Document, error) {
	if s.reader != nil {
		return s.reader.SearchDocuments(q, o)
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 20
	}
	qvec, cfg, err := s.queryVector(q, o.Mode, KindDocument)
	if err != nil {
		return nil, err
	}
	if qvec != nil {
		ds, err := s.hybridDocuments(q, o, limit, qvec, cfg)
		switch {
		case err == nil:
			s.embedShared().hybrid.Add(1)
			return ds, nil
		case o.Mode.strict():
			return nil, err
		}
		s.embedShared().logFallback(cfg, err)
	}
	return s.ftsDocuments(q, o, limit)
}

// documentWhere is what both rankings share: the active documents of a project
// and kind that the prefilter admits, on the alias d.
func documentWhere(o DocumentSearch) (string, []any) {
	where := []string{`d.status = 'active'`}
	var args []any
	if o.Project != "" {
		where = append(where, `d.project = ?`)
		args = append(args, o.Project)
	}
	if o.Kind != "" {
		where = append(where, `d.kind = ?`)
		args = append(args, o.Kind)
	}
	return o.Prefilter.with(strings.Join(where, " AND "), args)
}

func (s *Store) documentsByID(ids []int64) (map[int64]Document, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT `+documentColumns+` FROM documents WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]Document, len(ids))
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out[d.ID] = d
	}
	return out, rows.Err()
}

// ftsDocuments is the full-text ranking alone.
func (s *Store) ftsDocuments(q string, o DocumentSearch, limit int) ([]Document, error) {
	scan := limit
	if o.Keep != nil {
		scan = keepScanCap
	}
	ids, err := s.textDocumentIDs(q, o, scan)
	if err != nil {
		return nil, err
	}
	out, err := keptByID(ids, limit, s.documentsByID, o.Keep)
	if out == nil {
		out = []Document{}
	}
	return out, err
}

func (s *Store) textDocumentIDs(q string, o DocumentSearch, scan int) ([]int64, error) {
	where, args := documentWhere(o)
	rows, err := s.db.Query(`SELECT d.id FROM documents_fts f JOIN documents d ON d.id = f.rowid
		WHERE documents_fts MATCH ? AND `+where+` ORDER BY f.rank LIMIT ?`,
		append(append([]any{ftsQuery(q)}, args...), scan)...)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func (s *Store) hybridDocuments(q string, o DocumentSearch, limit int, qvec []float32, cfg *EmbedSearchConfig) ([]Document, error) {
	pool := poolFor(limit)
	var textIDs []int64
	if o.Mode != SearchVectorOnly {
		scan := pool
		if o.Keep != nil {
			scan = keepScanCap
		}
		var err error
		if textIDs, err = s.textDocumentIDs(q, o, scan); err != nil {
			return nil, err
		}
	}
	k, err := embedKindOf(KindDocument)
	if err != nil {
		return nil, err
	}
	// The kind's own condition (active) is part of the vector query; the rest is
	// the project, the kind and the prefilter.
	var rest []string
	var args []any
	if o.Project != "" {
		rest, args = append(rest, `d.project = ?`), append(args, o.Project)
	}
	if o.Kind != "" {
		rest, args = append(rest, `d.kind = ?`), append(args, o.Kind)
	}
	cond, cargs := o.Prefilter.with(strings.Join(rest, " AND "), args)
	vecIDs, err := s.vectorIDs(k, cfg, qvec, cond, cargs)
	if err != nil {
		return nil, err
	}
	if o.Keep != nil && len(vecIDs) > keepScanCap {
		vecIDs = vecIDs[:keepScanCap]
	}
	room := maxSemanticOnly(limit)
	if o.Mode == SearchVectorOnly {
		room = limit
	}
	return hybridRows(hybridKind[Document]{
		id:       func(d Document) int64 { return d.ID },
		semantic: func(d *Document) { d.Match = MatchSemantic },
		load:     s.documentsByID,
	}, textIDs, vecIDs, pool, limit, room, o.Keep)
}
