package store

import "fmt"

// DistillBatch is one submission to the provider's asynchronous endpoint.
// It exists because the result arrives up to 24 hours later, in a different
// process run than the one that sent it: without a record on this side, the
// hourly timer cannot tell a session that is being worked on from one that
// was never started.
type DistillBatch struct {
	ProviderID string `json:"provider_id"`
	State      string `json:"state"` // open|collected|failed
	ID         int64  `json:"id"`
	Items      int    `json:"items"`
	CreatedAt  string `json:"created_at"`
}

// DistillBatchItem ties one line of the submitted JSONL back to its session.
// Digest is the transcript digest as it stood at submission time, so a
// transcript that grew in the meantime is still recorded against what was
// actually sent.
type DistillBatchItem struct {
	CustomID  string `json:"custom_id"`
	Digest    string `json:"digest"`
	SessionID int64  `json:"session_id"`
}

func (s *Store) RecordDistillBatch(providerID string, items []DistillBatchItem) (int64, error) {
	if providerID == "" || len(items) == 0 {
		return 0, fmt.Errorf("batch needs a provider id and at least one item")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO distill_batches(provider_batch_id,state,created_at,updated_at)
		VALUES(?,'open',?,?)`, providerID, ts, ts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if _, err := tx.Exec(`INSERT INTO distill_batch_items(batch_id,custom_id,session_id,digest)
			VALUES(?,?,?,?)`, id, item.CustomID, item.SessionID, item.Digest); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) OpenDistillBatches() ([]DistillBatch, error) {
	rows, err := s.db.Query(`SELECT b.id, b.provider_batch_id, b.state, b.created_at,
		(SELECT COUNT(*) FROM distill_batch_items i WHERE i.batch_id = b.id)
		FROM distill_batches b WHERE b.state = 'open' ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DistillBatch{}
	for rows.Next() {
		var b DistillBatch
		if err := rows.Scan(&b.ID, &b.ProviderID, &b.State, &b.CreatedAt, &b.Items); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DistillBatchItems(batchID int64) ([]DistillBatchItem, error) {
	rows, err := s.db.Query(`SELECT custom_id, session_id, digest FROM distill_batch_items
		WHERE batch_id = ? ORDER BY session_id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DistillBatchItem{}
	for rows.Next() {
		var i DistillBatchItem
		if err := rows.Scan(&i.CustomID, &i.SessionID, &i.Digest); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RecordDistillBatchUsage stores the provider's own token count for one item.
// A local character estimate decides what to send; only this figure says what
// it cost.
func (s *Store) RecordDistillBatchUsage(batchID int64, customID string, prompt, completion int) error {
	_, err := s.db.Exec(`UPDATE distill_batch_items SET prompt_tokens=?, completion_tokens=?
		WHERE batch_id=? AND custom_id=?`, prompt, completion, batchID, customID)
	return err
}

func (s *Store) DistillBatchUsage(batchID int64) (prompt, completion int, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0)
		FROM distill_batch_items WHERE batch_id=?`, batchID).Scan(&prompt, &completion)
	return prompt, completion, err
}

func (s *Store) CloseDistillBatch(batchID int64, state string) error {
	if state != "collected" && state != "failed" {
		return fmt.Errorf("invalid terminal batch state %q", state)
	}
	_, err := s.db.Exec(`UPDATE distill_batches SET state=?, updated_at=? WHERE id=?`, state, now(), batchID)
	return err
}
