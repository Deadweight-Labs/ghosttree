package store

// EvidenceQuery is a quote from a transcript that backs an active knowledge
// entry, with the entry it backs. The search-quality measurement turns it into a
// query and asks whether the search finds the entry.
type EvidenceQuery struct {
	ID          int64
	KnowledgeID int64
	Quote       string
	// Confidence is that of the entry: whether it is quarantined decides which
	// views of the search can reach it.
	Confidence string
}

// EvidenceQueries lists the quotes of at least minChars characters (without the
// whitespace around them) that back active entries, in the order they were
// stored.
func (s *Store) EvidenceQueries(minChars int) ([]EvidenceQuery, error) {
	if s.reader != nil {
		return s.reader.EvidenceQueries(minChars)
	}
	rows, err := s.db.Query(`SELECT e.id, e.knowledge_id, e.quote, k.confidence
		FROM knowledge_evidence e JOIN knowledge k ON k.id = e.knowledge_id
		WHERE k.status = 'active' AND length(trim(e.quote)) >= ?
		ORDER BY e.id`, minChars)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceQuery
	for rows.Next() {
		var q EvidenceQuery
		if err := rows.Scan(&q.ID, &q.KnowledgeID, &q.Quote, &q.Confidence); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
