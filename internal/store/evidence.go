package store

// Evidence is one place in a transcript that supports a knowledge entry.
// It is what makes a distilled claim checkable instead of merely plausible.
type Evidence struct {
	SessionID int64  `json:"session_id"`
	ChunkSeq  int    `json:"chunk_seq"`
	Quote     string `json:"quote"`
}

func (s *Store) AddEvidence(knowledgeID int64, ev []Evidence) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO knowledge_evidence(knowledge_id, session_id, chunk_seq, quote)
		VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range ev {
		if _, err := stmt.Exec(knowledgeID, e.SessionID, e.ChunkSeq, e.Quote); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EvidenceFor(knowledgeID int64) ([]Evidence, error) {
	rows, err := s.db.Query(`SELECT session_id, chunk_seq, quote FROM knowledge_evidence
		WHERE knowledge_id = ? ORDER BY session_id, chunk_seq`, knowledgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.SessionID, &e.ChunkSeq, &e.Quote); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Recurrence counts independent sessions, not evidence rows: the same claim
// found twice in one conversation is one observation, not two.
func (s *Store) Recurrence(knowledgeID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM knowledge_evidence
		WHERE knowledge_id = ?`, knowledgeID).Scan(&n)
	return n, err
}
