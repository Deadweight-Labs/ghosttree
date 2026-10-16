package store

import (
	"fmt"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// RecordExplainBatch stores a batch that was submitted to the provider, with
// the objects (and the text hashes) it carries.
func (s *Store) RecordExplainBatch(providerID, model, language string, items []explain.BatchItem) (int64, error) {
	if providerID == "" || len(items) == 0 {
		return 0, fmt.Errorf("batch needs a provider id and at least one item")
	}
	if s.writer != nil {
		return queueValue(s, []any{providerID, model, language, items}, func(d *Store, p []any) (int64, error) {
			return d.RecordExplainBatch(p[0].(string), p[1].(string), p[2].(string), p[3].([]explain.BatchItem))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO explain_batches(provider_batch_id,state,model,language,created_at,updated_at) VALUES(?,'open',?,?,?,?)`,
		providerID, model, language, ts, ts)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		if _, err := tx.Exec(`INSERT INTO explain_batch_items(batch_id,custom_id,kind,object_id,content_hash,updated_at) VALUES(?,?,?,?,?,?)`,
			id, it.CustomID, it.Kind, it.ObjectID, it.Hash, ts); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// OpenExplainBatches lists the batches that were sent and not collected yet,
// oldest first. Items is the number still waiting for a result.
func (s *Store) OpenExplainBatches() ([]explain.OpenBatch, error) {
	if s.reader != nil {
		return s.reader.OpenExplainBatches()
	}
	rows, err := s.db.Query(`SELECT b.id, b.provider_batch_id, b.model, b.language, b.created_at,
		(SELECT COUNT(*) FROM explain_batch_items i WHERE i.batch_id = b.id AND i.state = 'pending')
		FROM explain_batches b WHERE b.state = 'open' ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []explain.OpenBatch
	for rows.Next() {
		var b explain.OpenBatch
		if err := rows.Scan(&b.ID, &b.ProviderID, &b.Model, &b.Language, &b.CreatedAt, &b.Pending); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PendingExplainBatchItems returns the items of a batch that still wait for a
// result.
func (s *Store) PendingExplainBatchItems(batchID int64) ([]explain.BatchItem, error) {
	if s.reader != nil {
		return s.reader.PendingExplainBatchItems(batchID)
	}
	rows, err := s.db.Query(`SELECT custom_id, kind, object_id, content_hash FROM explain_batch_items
		WHERE batch_id = ? AND state = 'pending' ORDER BY rowid`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []explain.BatchItem
	for rows.Next() {
		var it explain.BatchItem
		if err := rows.Scan(&it.CustomID, &it.Kind, &it.ObjectID, &it.Hash); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// FinishExplainBatchItem records the outcome of one item and the usage it
// caused in one transaction, so that a run that dies in between neither loses
// the item's usage nor counts it twice.
func (s *Store) FinishExplainBatchItem(batchID int64, customID, state, errText, day string, u llmusage.Usage) error {
	switch state {
	case "saved", "stale", "superseded", "failed":
	default:
		return fmt.Errorf("invalid explain batch item state %q", state)
	}
	if len(day) != 10 {
		return fmt.Errorf("usage needs a day (YYYY-MM-DD)")
	}
	if s.writer != nil {
		return queueWrite(s, []any{batchID, customID, state, errText, day, u}, func(d *Store, p []any) error {
			return d.FinishExplainBatchItem(p[0].(int64), p[1].(string), p[2].(string), p[3].(string), p[4].(string), p[5].(llmusage.Usage))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE explain_batch_items SET state=?, error=?, updated_at=? WHERE batch_id=? AND custom_id=? AND state='pending'`,
		state, errText, now(), batchID, customID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already finished: the usage was counted then
	}
	if u.Calls > 0 || u.Tokens() > 0 {
		if _, err := tx.Exec(`INSERT INTO llm_usage(job, day, calls, failures, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micro_usd)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(job, day) DO UPDATE SET calls=calls+excluded.calls, failures=failures+excluded.failures,
			  input_tokens=input_tokens+excluded.input_tokens, output_tokens=output_tokens+excluded.output_tokens,
			  cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens, cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,
			  cost_micro_usd=cost_micro_usd+excluded.cost_micro_usd`,
			llmusage.JobExplain, day, u.Calls, u.Failures, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CostMicroUSD); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CloseExplainBatch ends a batch: collected, or failed when its results can
// never be read.
func (s *Store) CloseExplainBatch(batchID int64, state string) error {
	if state != "collected" && state != "failed" {
		return fmt.Errorf("invalid terminal batch state %q", state)
	}
	if s.writer != nil {
		return queueWrite(s, []any{batchID, state}, func(d *Store, p []any) error { return d.CloseExplainBatch(p[0].(int64), p[1].(string)) })
	}
	_, err := s.db.Exec(`UPDATE explain_batches SET state=?, updated_at=? WHERE id=?`, state, now(), batchID)
	return err
}

// ExplainBatchBlocked returns the objects (kind:id:hash) that must not be sent
// again: those waiting in an open batch, and those whose entry failed since
// failedSince (RFC 3339), so that an object the model cannot handle is not
// paid for every hour.
func (s *Store) ExplainBatchBlocked(failedSince string) (map[string]bool, error) {
	if s.reader != nil {
		return s.reader.ExplainBatchBlocked(failedSince)
	}
	rows, err := s.db.Query(`SELECT i.kind, i.object_id, i.content_hash FROM explain_batch_items i
		JOIN explain_batches b ON b.id = i.batch_id
		WHERE (i.state = 'pending' AND b.state = 'open') OR (i.state = 'failed' AND i.updated_at >= ?)`, failedSince)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var kind, hash string
		var id int64
		if err := rows.Scan(&kind, &id, &hash); err != nil {
			return nil, err
		}
		out[explain.BatchKey(kind, id, hash)] = true
	}
	return out, rows.Err()
}

// ExplainBatched reports whether one object (at this text) is blocked as
// ExplainBatchBlocked describes.
func (s *Store) ExplainBatched(kind string, id int64, hash, failedSince string) (bool, error) {
	if s.reader != nil {
		return s.reader.ExplainBatched(kind, id, hash, failedSince)
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM explain_batch_items i JOIN explain_batches b ON b.id = i.batch_id
		WHERE i.kind = ? AND i.object_id = ? AND i.content_hash = ?
		  AND ((i.state = 'pending' AND b.state = 'open') OR (i.state = 'failed' AND i.updated_at >= ?))`,
		kind, id, hash, failedSince).Scan(&n)
	return n > 0, err
}

// ExplainCreatedAt is when an object was created, as stored (RFC 3339).
func (s *Store) ExplainCreatedAt(kind string, id int64) (time.Time, error) {
	if s.reader != nil {
		return s.reader.ExplainCreatedAt(kind, id)
	}
	var q string
	switch kind {
	case explain.KindKnowledge:
		q = `SELECT created_at FROM knowledge WHERE id = ?`
	case explain.KindRequest:
		q = `SELECT created_at FROM requests WHERE id = ?`
	case explain.KindPain:
		q = `SELECT created_at FROM pains WHERE id = ?`
	case explain.KindTask:
		q = `SELECT created_at FROM human_tasks WHERE id = ?`
	case explain.KindQuestion:
		q = `SELECT created_at FROM coord_messages WHERE id = ?`
	default:
		return time.Time{}, explain.ErrNotExplainable
	}
	var raw string
	if err := s.db.QueryRow(q, id).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("created_at %q of %s %d is not a time", raw, kind, id)
}
