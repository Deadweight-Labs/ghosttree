package store

import (
	"errors"
	"math"
	"strings"
)

// EmbedModel says which vectors count as current: the model that made them and
// the version of the text formats they were made with.
type EmbedModel struct {
	Model         string
	PrefixVersion string
}

// KnowledgeEmbedding is the stored vector of one entry.
type KnowledgeEmbedding struct {
	KnowledgeID   int64
	Model         string
	PrefixVersion string
	// TextHash is the hash of the document text that was embedded.
	TextHash string
	Dim      int
	// Scale turns the int8 values in Vec back into the unit vector.
	Scale float64
	Vec   []byte
	// ReadAt is the time the job took the text from the entry, as given with the
	// item (EmbedDue.ReadAt). An entry changed at or after it is looked at again,
	// so a change made while the vector was being computed is not lost.
	ReadAt string
}

// EmbedItem is an entry whose vector is missing or out of date: the finished
// document text to embed and its hash.
type EmbedItem struct {
	ID   int64
	Text string
	Hash string
	// Attempts is how often this very text has failed before; zero for a text
	// without a failure on record.
	Attempts int
}

// EmbeddingFailure parks an entry: for a time (a text on which the embedding
// server fails, perhaps) or for good (a text it refuses). It holds for this text,
// this model and this format only; an edit or a new model tries again.
type EmbeddingFailure struct {
	KnowledgeID   int64
	Model         string
	PrefixVersion string
	TextHash      string
	Attempts      int
	Permanent     bool
	// RetryAt is the time after which a transient failure is tried again
	// (RFC 3339, UTC).
	RetryAt string
	// Error is a short message of the server, never the text.
	Error string
}

// EmbedJobState is how the background job last fared.
type EmbedJobState struct {
	LastOKAt    string
	LastError   string
	LastErrorAt string
	Failures    int
}

// UpsertKnowledgeEmbedding stores the vector of an entry, replacing an earlier
// one, and clears the failure on record for the entry. The entry need not still
// be active or unchanged: a vector of an old text carries that text's hash and is
// replaced when the job sees the difference.
func (s *Store) UpsertKnowledgeEmbedding(e KnowledgeEmbedding) error {
	if s.writer != nil {
		return queueWrite(s, []any{e}, func(d *Store, p []any) error { return d.UpsertKnowledgeEmbedding(p[0].(KnowledgeEmbedding)) })
	}
	if e.KnowledgeID <= 0 || e.Model == "" || e.PrefixVersion == "" || e.TextHash == "" || e.ReadAt == "" ||
		e.Dim <= 0 || len(e.Vec) != e.Dim || math.IsNaN(e.Scale) || math.IsInf(e.Scale, 0) || e.Scale <= 0 {
		return errors.New("invalid knowledge embedding")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO knowledge_embeddings(knowledge_id,model,prefix_version,dim,text_hash,scale,vec,read_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(knowledge_id) DO UPDATE SET model=excluded.model, prefix_version=excluded.prefix_version,
			dim=excluded.dim, text_hash=excluded.text_hash, scale=excluded.scale, vec=excluded.vec,
			read_at=excluded.read_at, updated_at=excluded.updated_at`,
		e.KnowledgeID, e.Model, e.PrefixVersion, e.Dim, e.TextHash, e.Scale, e.Vec, e.ReadAt, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM knowledge_embedding_failures WHERE knowledge_id=?`, e.KnowledgeID); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchKnowledgeEmbeddings says that the vectors of these entries were checked
// against the text as of readAt and are still right.
func (s *Store) TouchKnowledgeEmbeddings(ids []int64, readAt string) error {
	if s.writer != nil {
		return queueWrite(s, []any{ids, readAt}, func(d *Store, p []any) error {
			return d.TouchKnowledgeEmbeddings(p[0].([]int64), p[1].(string))
		})
	}
	if len(ids) == 0 || readAt == "" {
		return nil
	}
	args := []any{readAt}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE knowledge_embeddings SET read_at=? WHERE knowledge_id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// RecordEmbeddingFailure parks an entry; see EmbeddingFailure.
func (s *Store) RecordEmbeddingFailure(f EmbeddingFailure) error {
	if s.writer != nil {
		return queueWrite(s, []any{f}, func(d *Store, p []any) error { return d.RecordEmbeddingFailure(p[0].(EmbeddingFailure)) })
	}
	if f.KnowledgeID <= 0 || f.Model == "" || f.PrefixVersion == "" || f.TextHash == "" || f.Attempts <= 0 || f.RetryAt == "" {
		return errors.New("invalid embedding failure")
	}
	msg := strings.Join(strings.Fields(f.Error), " ")
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300])
	}
	permanent := 0
	if f.Permanent {
		permanent = 1
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_embedding_failures(knowledge_id,model,prefix_version,text_hash,attempts,permanent,retry_at,error,failed_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(knowledge_id) DO UPDATE SET model=excluded.model, prefix_version=excluded.prefix_version,
			text_hash=excluded.text_hash, attempts=excluded.attempts, permanent=excluded.permanent,
			retry_at=excluded.retry_at, error=excluded.error, failed_at=excluded.failed_at`,
		f.KnowledgeID, f.Model, f.PrefixVersion, f.TextHash, f.Attempts, permanent, f.RetryAt, msg, now())
	return err
}

// SetEmbedJobState records how the background job fared.
func (s *Store) SetEmbedJobState(st EmbedJobState) error {
	if s.writer != nil {
		return queueWrite(s, []any{st}, func(d *Store, p []any) error { return d.SetEmbedJobState(p[0].(EmbedJobState)) })
	}
	_, err := s.db.Exec(`INSERT INTO knowledge_embedding_state(id,last_ok_at,last_error,last_error_at,failures,updated_at)
		VALUES(1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET last_ok_at=excluded.last_ok_at, last_error=excluded.last_error,
			last_error_at=excluded.last_error_at, failures=excluded.failures, updated_at=excluded.updated_at`,
		st.LastOKAt, st.LastError, st.LastErrorAt, st.Failures, now())
	return err
}
