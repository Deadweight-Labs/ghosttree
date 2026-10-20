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

// The sources of RequestQueries.
const (
	// RequestQuerySighting: a quote of the session in which the wish was spoken,
	// and the request that was made of it.
	RequestQuerySighting = "sighting"
	// RequestQueryRelation: the title of a request that is marked as a duplicate
	// of another, or as superseding it, and the other request. Somebody who asks
	// "is there a request for this already?" with the words of the new one should
	// find the old.
	RequestQueryRelation = "relation"
)

// RequestQuery is a query the ledger holds the answer to: the text, the request
// that is the right answer, and where it comes from.
type RequestQuery struct {
	Source string
	Query  string
	Target int64
	// Exclude is the request the text is the title of, which is not an answer to it
	// (zero for a quote).
	Exclude int64
}

// RequestQueries lists the queries the request ledger itself provides for the
// search-quality measurement: the sighting quotes of at least minChars characters
// (without the whitespace around them) with the request they led to, and, for
// every relation that marks a request as a duplicate of another or as superseding
// it, the title of the first with the second as the answer.
func (s *Store) RequestQueries(minChars int) ([]RequestQuery, error) {
	if s.reader != nil {
		return s.reader.RequestQueries(minChars)
	}
	var out []RequestQuery
	rows, err := s.db.Query(`SELECT g.request_id, g.quote FROM request_sightings g
		JOIN requests r ON r.id = g.request_id
		WHERE length(trim(g.quote)) >= ? ORDER BY g.id`, minChars)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		q := RequestQuery{Source: RequestQuerySighting}
		if err := rows.Scan(&q.Target, &q.Query); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT rel.request_id, rel.other_request_id, a.title FROM request_relations rel
		JOIN requests a ON a.id = rel.request_id
		JOIN requests b ON b.id = rel.other_request_id
		WHERE rel.kind IN ('duplicates','supersedes') AND rel.other_request_id IS NOT NULL
		ORDER BY rel.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		q := RequestQuery{Source: RequestQueryRelation}
		if err := rows.Scan(&q.Exclude, &q.Target, &q.Query); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// SessionTitleQuery is the title of an active knowledge entry and the sessions
// that are the evidence of it. Somebody who asks for a session with what the
// entry says should find one of them: a session is an answer when it backs the
// entry, whichever of them it is.
type SessionTitleQuery struct {
	KnowledgeID int64
	Title       string
	// Sessions are the numbers of the sessions of the evidence, once each, in
	// ascending order.
	Sessions []int64
}

// SessionTitleQueries lists the active knowledge entries that have evidence, with
// a title of at least minChars characters (without the whitespace around it),
// and the sessions of their evidence, in the order of the entries. It is the
// query set of the measurement of the session search that needs no question
// written by anybody. Most of these titles were written by a model that had read
// the session, so they are summaries and not questions.
func (s *Store) SessionTitleQueries(minChars int) ([]SessionTitleQuery, error) {
	if s.reader != nil {
		return s.reader.SessionTitleQueries(minChars)
	}
	rows, err := s.db.Query(`SELECT k.id, k.title, e.session_id
		FROM knowledge k JOIN knowledge_evidence e ON e.knowledge_id = k.id JOIN sessions s ON s.id = e.session_id
		WHERE k.status = 'active' AND length(trim(k.title)) >= ?
		GROUP BY k.id, e.session_id ORDER BY k.id, e.session_id`, minChars)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionTitleQuery
	for rows.Next() {
		var id, session int64
		var title string
		if err := rows.Scan(&id, &title, &session); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].KnowledgeID != id {
			out = append(out, SessionTitleQuery{KnowledgeID: id, Title: title})
		}
		out[len(out)-1].Sessions = append(out[len(out)-1].Sessions, session)
	}
	return out, rows.Err()
}
