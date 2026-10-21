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

// Embedding is the stored vector of one object of a kind (see EmbedKinds).
type Embedding struct {
	Kind     string
	ObjectID int64
	Model    string
	// PrefixVersion is the version of the query and document formats.
	PrefixVersion string
	// TextHash is the hash of the document text that was embedded.
	TextHash string
	Dim      int
	// Scale turns the int8 values in Vec back into the unit vector.
	Scale float64
	Vec   []byte
	// ReadAt is the time the job took the text from the object, as given with the
	// item (EmbedDue.ReadAt). An object changed at or after it is looked at again,
	// so a change made while the vector was being computed is not lost.
	ReadAt string
	// Source is the hash of the stored text the document was made from, as given
	// with the item (EmbedItem.Source). Only the sessions have one, and need it: the
	// writer compares it with the text that is stored now before it stores the vector.
	Source string
}

// KnowledgeEmbedding is the stored vector of one knowledge entry.
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

func (e KnowledgeEmbedding) generic() Embedding {
	return Embedding{Kind: KindKnowledge, ObjectID: e.KnowledgeID, Model: e.Model, PrefixVersion: e.PrefixVersion,
		TextHash: e.TextHash, Dim: e.Dim, Scale: e.Scale, Vec: e.Vec, ReadAt: e.ReadAt}
}

// EmbedItem is an object whose vector is missing or out of date: the finished
// document text to embed and its hash.
type EmbedItem struct {
	ID   int64
	Text string
	Hash string
	// Attempts is how often this very text has failed before; zero for a text
	// without a failure on record.
	Attempts int
	// Source is the hash of the stored text the document was made from, for the
	// kinds whose document is made of a large stored text by a long chain (the line
	// of a chunk of a session). The job hands it back with the vector
	// (Embedding.Source), and the writer then only has to see that the stored text is
	// still that one, not to make the document again. Empty for the other kinds.
	Source string
}

// EmbedFailure parks an object: for a time (a text on which the embedding
// server fails, perhaps) or for good (a text it refuses). It holds for this text,
// this model and this format only; an edit or a new model tries again.
type EmbedFailure struct {
	Kind          string
	ObjectID      int64
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

// EmbeddingFailure is EmbedFailure for a knowledge entry.
type EmbeddingFailure struct {
	KnowledgeID   int64
	Model         string
	PrefixVersion string
	TextHash      string
	Attempts      int
	Permanent     bool
	RetryAt       string
	Error         string
}

func (f EmbeddingFailure) generic() EmbedFailure {
	return EmbedFailure{Kind: KindKnowledge, ObjectID: f.KnowledgeID, Model: f.Model, PrefixVersion: f.PrefixVersion,
		TextHash: f.TextHash, Attempts: f.Attempts, Permanent: f.Permanent, RetryAt: f.RetryAt, Error: f.Error}
}

// EmbedJobState is how the background job last fared.
type EmbedJobState struct {
	LastOKAt    string
	LastError   string
	LastErrorAt string
	Failures    int
}

// UpsertEmbedding stores the vector of an object, replacing an earlier one, and
// clears the failure on record for it. The object need not still be active or
// unchanged: a vector of an old text carries that text's hash and is replaced
// when the job sees the difference.
func (s *Store) UpsertEmbedding(e Embedding) error {
	if s.writer != nil {
		return queueWrite(s, []any{e}, func(d *Store, p []any) error { return d.UpsertEmbedding(p[0].(Embedding)) })
	}
	return s.putEmbedding(e)
}

// UpsertKnowledgeEmbedding is UpsertEmbedding for a knowledge entry.
func (s *Store) UpsertKnowledgeEmbedding(e KnowledgeEmbedding) error {
	if s.writer != nil {
		return queueWrite(s, []any{e}, func(d *Store, p []any) error { return d.UpsertKnowledgeEmbedding(p[0].(KnowledgeEmbedding)) })
	}
	return s.putEmbedding(e.generic())
}

func (s *Store) putEmbedding(e Embedding) error {
	k, err := embedKindOf(e.Kind)
	if err != nil {
		return err
	}
	if e.ObjectID <= 0 || e.Model == "" || e.PrefixVersion == "" || e.TextHash == "" || e.ReadAt == "" ||
		e.Dim <= 0 || len(e.Vec) != e.Dim || math.IsNaN(e.Scale) || math.IsInf(e.Scale, 0) || e.Scale <= 0 {
		return errors.New("invalid embedding")
	}
	if k.name == KindSession && e.Source == "" {
		return errors.New("invalid embedding: a vector of a session needs the hash of the line it was made from")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if k.name == KindSession {
		// The text of a chunk can change while the job is embedding it: a redaction
		// that came after the job read the chunk finds no vector to drop yet, and the
		// vector that arrives is the one of the text before. So the line is looked at
		// again here, as it is stored now, and the vector is kept only if it is the
		// line the job made the document from. Inside the transaction and after taking
		// the write lock, so that nothing can change it between the check and the
		// write; a comparison of hashes, since the writer is no place to make the
		// document again (see verifySessionSource).
		keep, err := verifySessionSource(tx, e.ObjectID, e.Source)
		if err != nil {
			return err
		}
		if !keep {
			return nil
		}
	}
	args := append(k.idArgs(e.ObjectID), e.Model, e.PrefixVersion, e.Dim, e.TextHash, e.Scale, e.Vec, e.ReadAt, now())
	if _, err := tx.Exec(`INSERT INTO `+k.emb+`(`+k.idCols()+`,model,prefix_version,dim,text_hash,scale,vec,read_at,updated_at)
		VALUES(`+k.idVals()+`,?,?,?,?,?,?,?,?)
		ON CONFLICT(`+k.idCols()+`) DO UPDATE SET model=excluded.model, prefix_version=excluded.prefix_version,
			dim=excluded.dim, text_hash=excluded.text_hash, scale=excluded.scale, vec=excluded.vec,
			read_at=excluded.read_at, updated_at=excluded.updated_at`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM `+k.fail+` WHERE `+k.keyWhere(k.fail)+k.idCol+`=?`, e.ObjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchEmbeddings says that the vectors of these objects were checked against the
// text as of readAt and are still right.
func (s *Store) TouchEmbeddings(kind string, ids []int64, readAt string) error {
	if s.writer != nil {
		return queueWrite(s, []any{kind, ids, readAt}, func(d *Store, p []any) error {
			return d.TouchEmbeddings(p[0].(string), p[1].([]int64), p[2].(string))
		})
	}
	return s.touchEmbeddings(kind, ids, readAt)
}

// TouchKnowledgeEmbeddings is TouchEmbeddings for knowledge entries.
func (s *Store) TouchKnowledgeEmbeddings(ids []int64, readAt string) error {
	if s.writer != nil {
		return queueWrite(s, []any{ids, readAt}, func(d *Store, p []any) error {
			return d.TouchKnowledgeEmbeddings(p[0].([]int64), p[1].(string))
		})
	}
	return s.touchEmbeddings(KindKnowledge, ids, readAt)
}

func (s *Store) touchEmbeddings(kind string, ids []int64, readAt string) error {
	k, err := embedKindOf(kind)
	if err != nil {
		return err
	}
	if len(ids) == 0 || readAt == "" {
		return nil
	}
	if k.name == KindSession {
		// The mark of a session holds the model, which this call does not know.
		return errors.New("sessions are touched with TouchSessionEmbeddings")
	}
	// In pieces: an id list that grows with the stock would reach the limit on the
	// number of variables of one statement.
	return s.eachChunk(ids, func(part []any) error {
		_, err := s.db.Exec(`UPDATE `+k.emb+` SET read_at=? WHERE `+k.keyWhere(k.emb)+k.idCol+` IN (`+placeholders(len(part))+`)`,
			append([]any{readAt}, part...)...)
		return err
	})
}

// RecordEmbedFailure parks an object; see EmbedFailure.
func (s *Store) RecordEmbedFailure(f EmbedFailure) error {
	if s.writer != nil {
		return queueWrite(s, []any{f}, func(d *Store, p []any) error { return d.RecordEmbedFailure(p[0].(EmbedFailure)) })
	}
	return s.putEmbedFailure(f)
}

// RecordEmbeddingFailure is RecordEmbedFailure for a knowledge entry.
func (s *Store) RecordEmbeddingFailure(f EmbeddingFailure) error {
	if s.writer != nil {
		return queueWrite(s, []any{f}, func(d *Store, p []any) error { return d.RecordEmbeddingFailure(p[0].(EmbeddingFailure)) })
	}
	return s.putEmbedFailure(f.generic())
}

func (s *Store) putEmbedFailure(f EmbedFailure) error {
	k, err := embedKindOf(f.Kind)
	if err != nil {
		return err
	}
	if f.ObjectID <= 0 || f.Model == "" || f.PrefixVersion == "" || f.TextHash == "" || f.Attempts <= 0 || f.RetryAt == "" {
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
	args := append(k.idArgs(f.ObjectID), f.Model, f.PrefixVersion, f.TextHash, f.Attempts, permanent, f.RetryAt, msg, now())
	_, err = s.db.Exec(`INSERT INTO `+k.fail+`(`+k.idCols()+`,model,prefix_version,text_hash,attempts,permanent,retry_at,error,failed_at)
		VALUES(`+k.idVals()+`,?,?,?,?,?,?,?,?)
		ON CONFLICT(`+k.idCols()+`) DO UPDATE SET model=excluded.model, prefix_version=excluded.prefix_version,
			text_hash=excluded.text_hash, attempts=excluded.attempts, permanent=excluded.permanent,
			retry_at=excluded.retry_at, error=excluded.error, failed_at=excluded.failed_at`, args...)
	return err
}

// PurgeEmbeddings removes the vectors and failures of objects that are gone or no
// longer count (an archived document, a deleted ghost description). The search
// never reads a vector but through its object, so they could not be hits anyway;
// this keeps the table from growing with what is left behind. It returns the
// number of vectors removed. Knowledge keeps what it has always kept: its
// vectors outlive an archived entry, and nothing is removed.
func (s *Store) PurgeEmbeddings(kind string) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{kind}, func(d *Store, p []any) (int, error) { return d.PurgeEmbeddings(p[0].(string)) })
	}
	k, err := embedKindOf(kind)
	if err == nil && k.name == KindSession {
		return s.purgeSessionEmbeddings()
	}
	if err != nil || !k.shared {
		return 0, err
	}
	gone := func(table string) string {
		return k.keyWhere(table) + table + "." + k.idCol + ` NOT IN (SELECT ` + k.id + ` FROM ` + k.from + ` WHERE ` + k.active + `)`
	}
	var stale int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + k.emb + ` WHERE ` + gone(k.emb)).Scan(&stale); err != nil {
		return 0, err
	}
	if stale > 0 {
		if _, err := s.db.Exec(`DELETE FROM ` + k.emb + ` WHERE ` + gone(k.emb)); err != nil {
			return 0, err
		}
	}
	var parked int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + k.fail + ` WHERE ` + gone(k.fail)).Scan(&parked); err != nil {
		return stale, err
	}
	if parked > 0 {
		if _, err := s.db.Exec(`DELETE FROM ` + k.fail + ` WHERE ` + gone(k.fail)); err != nil {
			return stale, err
		}
	}
	return stale, nil
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
