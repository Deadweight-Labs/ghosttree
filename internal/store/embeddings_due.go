package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// EmbedDue is what the background job has to do next for one kind.
type EmbedDue struct {
	// Items are the objects to embed, most recently edited first.
	Items []EmbedItem
	// Unchanged are objects that were edited after their vector was made, but not
	// in the text that is embedded (a change of confidence, say). Their vectors
	// are right; the job says so with TouchEmbeddings(kind, ids, ReadAt) so that
	// they are not looked at again. For sessions, whose items are chunks, these are
	// the numbers of the sessions that were looked at and found complete.
	Unchanged []int64
	// ReadAt is the time taken before any text was read. Pass it back with the
	// vectors and with Unchanged.
	ReadAt string
}

// EmbedStatus counts the vectors of the objects of one kind.
type EmbedStatus struct {
	// Active is the number of objects that get a vector.
	Active int
	// Current have a vector of the configured model, prefix version and text.
	Current int
	// Due is Active - Current: missing, made by another model or format, or
	// made from a text that has changed since.
	Due int
	// OtherModel is the part of Due that has a vector of another model or text
	// format, which a change of configuration produces.
	OtherModel int
	// Parked is the part of Due that is left alone for now: the embedding server
	// fails on this text, or refuses it.
	Parked int
	Job    EmbedJobState
}

type dueCandidate struct {
	id int64
	// stored is the hash of the text a current-looking vector was made from; empty
	// for an object that has no usable vector.
	stored string
}

// KnowledgeEmbeddingDue is EmbeddingDue for knowledge entries.
func (s *Store) KnowledgeEmbeddingDue(m EmbedModel, limit int, at time.Time, full bool) (EmbedDue, error) {
	if s.reader != nil {
		return s.reader.KnowledgeEmbeddingDue(m, limit, at, full)
	}
	return s.embeddingDue(knowledgeKind(), m, limit, at, full)
}

// EmbeddingDue returns up to limit objects of a kind whose vector is missing,
// made by another model or text format, or made from a text that has changed,
// newest edit first so that a fresh object does not wait behind a backlog. at is
// the time of the call; an object parked after failures is left out until its
// retry time.
//
// The texts are read in pages of a few objects, never all at once, and only for
// objects that can be due: the ones without a current vector, and the ones that
// were edited at or after the time their vector was made (updated_at is the one
// place where the text of an object changes, apart from the parts of a text that
// live in another table, which the kind names). With full, the second group is
// every object that has a vector, which catches a text changed some other way;
// the job asks for it once per kind, when it starts.
func (s *Store) EmbeddingDue(kind string, m EmbedModel, limit int, at time.Time, full bool) (EmbedDue, error) {
	if s.reader != nil {
		return s.reader.EmbeddingDue(kind, m, limit, at, full)
	}
	k, err := embedKindOf(kind)
	if err != nil {
		return EmbedDue{}, err
	}
	return s.embeddingDue(k, m, limit, at, full)
}

func (s *Store) embeddingDue(k embedKind, m EmbedModel, limit int, at time.Time, full bool) (EmbedDue, error) {
	if limit <= 0 {
		limit = 8
	}
	if k.name == KindSession {
		return s.sessionEmbeddingDue(k, m, limit, at)
	}
	due := EmbedDue{ReadAt: now()}
	order := ` ORDER BY ` + k.updated + ` DESC, ` + k.id + ` DESC`
	missing, err := s.dueCandidates(`SELECT `+k.id+`, '' FROM `+k.from+`
		LEFT JOIN `+k.vecJoin()+`
		WHERE `+k.active+` AND (e.`+k.idCol+` IS NULL OR e.model != ? OR e.prefix_version != ?)`+order, m.Model, m.PrefixVersion)
	if err != nil {
		return due, err
	}
	if err := s.collectDue(k, &due, missing, m, limit, at, full); err != nil || len(due.Items) >= limit {
		return due, err
	}
	changed := `SELECT ` + k.id + `, e.text_hash FROM ` + k.from + `
		JOIN ` + k.vecJoin() + `
		WHERE ` + k.active + ` AND e.model = ? AND e.prefix_version = ?`
	if !full {
		changed += ` AND (` + k.updated + ` >= e.read_at`
		if k.alsoChanged != "" {
			changed += ` OR ` + k.alsoChanged
		}
		changed += `)`
	}
	maybe, err := s.dueCandidates(changed+order, m.Model, m.PrefixVersion)
	if err != nil {
		return due, err
	}
	err = s.collectDue(k, &due, maybe, m, limit, at, full)
	return due, err
}

func (s *Store) dueCandidates(query string, args ...any) ([]dueCandidate, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dueCandidate
	for rows.Next() {
		var c dueCandidate
		if err := rows.Scan(&c.id, &c.stored); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type parkedFailure struct {
	model, prefix, hash string
	attempts            int
	permanent           bool
	retryAt             string
}

// collectDue reads the texts of the candidates a page at a time and adds the
// ones that are due, until there are limit of them.
func (s *Store) collectDue(k embedKind, due *EmbedDue, cands []dueCandidate, m EmbedModel, limit int, at time.Time, full bool) error {
	stamp := at.UTC().Format(time.RFC3339)
	for start := 0; start < len(cands) && len(due.Items) < limit; start += loadChunk {
		part := cands[start:min(start+loadChunk, len(cands))]
		ids := make([]int64, len(part))
		for i, c := range part {
			ids[i] = c.id
		}
		texts, err := k.texts(s, ids)
		if err != nil {
			return err
		}
		failures, err := s.embeddingFailures(k, ids)
		if err != nil {
			return err
		}
		for _, c := range part {
			doc, ok := texts[c.id]
			if !ok {
				continue
			}
			hash := embed.TextHash(doc)
			if c.stored != "" && hash == c.stored {
				if !full {
					due.Unchanged = append(due.Unchanged, c.id)
				}
				continue
			}
			attempts := 0
			if f, ok := failures[c.id]; ok && f.model == m.Model && f.prefix == m.PrefixVersion && f.hash == hash {
				if f.permanent || f.retryAt > stamp {
					continue
				}
				attempts = f.attempts
			}
			due.Items = append(due.Items, EmbedItem{ID: c.id, Text: doc, Hash: hash, Attempts: attempts})
			if len(due.Items) >= limit {
				break
			}
		}
	}
	return nil
}

func (s *Store) embeddingFailures(k embedKind, ids []int64) (map[int64]parkedFailure, error) {
	out := map[int64]parkedFailure{}
	err := s.eachChunk(ids, func(part []any) error {
		rows, err := s.db.Query(`SELECT `+k.idCol+`, model, prefix_version, text_hash, attempts, permanent, retry_at
			FROM `+k.fail+` WHERE `+k.keyWhere(k.fail)+k.idCol+` IN (`+placeholders(len(part))+`)`, part...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var f parkedFailure
			var permanent int
			if err := rows.Scan(&id, &f.model, &f.prefix, &f.hash, &f.attempts, &permanent, &f.retryAt); err != nil {
				return err
			}
			f.permanent = permanent != 0
			out[id] = f
		}
		return rows.Err()
	})
	return out, err
}

// KnowledgeEmbeddingStatus is EmbeddingStatus for knowledge entries.
func (s *Store) KnowledgeEmbeddingStatus(m EmbedModel) (EmbedStatus, error) {
	if s.reader != nil {
		return s.reader.KnowledgeEmbeddingStatus(m)
	}
	return s.embeddingStatus(knowledgeKind(), m)
}

// EmbeddingStatus counts what is embedded and what is due for a kind, and
// reports how the job last fared. It reads the text of every object that counts,
// which is fine for a command and not for a hot path.
func (s *Store) EmbeddingStatus(kind string, m EmbedModel) (EmbedStatus, error) {
	if s.reader != nil {
		return s.reader.EmbeddingStatus(kind, m)
	}
	k, err := embedKindOf(kind)
	if err != nil {
		return EmbedStatus{}, err
	}
	return s.embeddingStatus(k, m)
}

func (s *Store) embeddingStatus(k embedKind, m EmbedModel) (EmbedStatus, error) {
	if k.name == KindSession {
		return s.sessionEmbeddingStatus(k, m)
	}
	var st EmbedStatus
	stamp := time.Now().UTC().Format(time.RFC3339)
	type row struct {
		id                             int64
		model, prefix, hash            sql.NullString
		fModel, fPrefix, fHash, fRetry sql.NullString
		fPermanent                     sql.NullInt64
	}
	var rows []row
	q, err := s.db.Query(`SELECT ` + k.id + `, e.model, e.prefix_version, e.text_hash,
			f.model, f.prefix_version, f.text_hash, f.permanent, f.retry_at
		FROM ` + k.from + ` LEFT JOIN ` + k.vecJoin() + ` LEFT JOIN ` + k.failJoin() + `
		WHERE ` + k.active)
	if err != nil {
		return st, err
	}
	for q.Next() {
		var r row
		if err := q.Scan(&r.id, &r.model, &r.prefix, &r.hash, &r.fModel, &r.fPrefix, &r.fHash, &r.fPermanent, &r.fRetry); err != nil {
			q.Close()
			return st, err
		}
		rows = append(rows, r)
	}
	if err := q.Err(); err != nil {
		q.Close()
		return st, err
	}
	q.Close()
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	texts, err := k.texts(s, ids)
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		st.Active++
		current := embed.TextHash(texts[r.id])
		switch {
		case !r.model.Valid:
		case r.model.String != m.Model || r.prefix.String != m.PrefixVersion:
			st.OtherModel++
		case current == r.hash.String:
			st.Current++
			continue
		}
		if r.fModel.Valid && r.fModel.String == m.Model && r.fPrefix.String == m.PrefixVersion && r.fHash.String == current &&
			(r.fPermanent.Int64 != 0 || r.fRetry.String > stamp) {
			st.Parked++
		}
	}
	st.Due = st.Active - st.Current
	err = s.db.QueryRow(`SELECT last_ok_at,last_error,last_error_at,failures FROM knowledge_embedding_state WHERE id=1`).
		Scan(&st.Job.LastOKAt, &st.Job.LastError, &st.Job.LastErrorAt, &st.Job.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return st, err
}
