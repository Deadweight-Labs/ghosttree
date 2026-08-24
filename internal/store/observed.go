package store

import "database/sql"

// The observation time of an entry backed by transcripts is the start of the
// earliest session that evidences it. MIN rather than the session at hand,
// because evidence arrives out of order: the distiller works a backlog, so the
// second session to corroborate a finding is often the older one.
const observedFromEvidence = `(SELECT MIN(s.started_at) FROM knowledge_evidence e
	JOIN sessions s ON s.id = e.session_id WHERE e.knowledge_id = knowledge.id)`

// refreshObservedAt recomputes one entry's observation time after its evidence
// changed. It keeps whatever is there when an entry has no evidence at all,
// which is the hand-written case: written down is the same moment as seen.
func refreshObservedAt(tx *sql.Tx, id int64) error {
	_, err := tx.Exec(`UPDATE knowledge SET observed_at = COALESCE(`+observedFromEvidence+`, observed_at) WHERE id = ?`, id)
	return err
}

// BackfillObservedAt dates the entries that predate the column. Nothing needs
// to be collected for it: knowledge_evidence has pointed at sessions all along
// and sessions.started_at has always been set, so the whole archive can be
// dated retroactively. Entries without evidence fall back to created_at, which
// is what their observation time is.
//
// Only empty values are filled, so a rerun is a no-op and a reconfirmation set
// by hand is not overwritten.
func (s *Store) BackfillObservedAt() (int64, error) {
	res, err := s.db.Exec(`UPDATE knowledge
		SET observed_at = COALESCE(` + observedFromEvidence + `, created_at)
		WHERE observed_at = ''`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
