package store

import "strings"

// recordKnowledgeUse marks entries as having been put in front of an agent.
//
// Delivery counts as use, not only a search hit: the bootstrap is how most
// knowledge is actually read, and counting only searches would rate the
// best-placed entries as the least used ones. Operator views — the staged
// preview and the admin search — deliberately do not call this, because a
// measurement that is created by looking at it measures nothing.
//
// A failure here is dropped on purpose. This is a counter beside a read path,
// and no read should fail because its bookkeeping did.
func (s *Store) recordKnowledgeUse(ks []Knowledge) {
	if len(ks) == 0 {
		return
	}
	placeholders := make([]string, len(ks))
	args := make([]any, 0, len(ks)+1)
	args = append(args, now())
	for i, k := range ks {
		placeholders[i] = "?"
		args = append(args, k.ID)
	}
	s.db.Exec(`UPDATE knowledge SET last_used_at = ?, hit_count = hit_count + 1
		WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
}

// KnowledgeUsage reports how often one entry has been delivered or hit, and
// when that last happened.
func (s *Store) KnowledgeUsage(id int64) (hits int, lastUsed string, err error) {
	err = s.db.QueryRow(`SELECT hit_count, last_used_at FROM knowledge WHERE id = ?`, id).Scan(&hits, &lastUsed)
	return hits, lastUsed, err
}

// KnowledgeUnusedSince returns active entries that were never delivered or last
// used before the cutoff — the entries a usage-based staleness rule would act
// on, and the ones a ranked bootstrap would drop first.
func (s *Store) KnowledgeUnusedSince(cutoff string) ([]Knowledge, error) {
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge
		WHERE status = 'active' AND (last_used_at = '' OR last_used_at < ?)
		ORDER BY hit_count ASC, last_used_at ASC, id ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	return s.scanKnowledge(rows)
}
