package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
)

// EmbedDue is what the background job has to do next.
type EmbedDue struct {
	// Items are the entries to embed, most recently edited first.
	Items []EmbedItem
	// Unchanged are entries that were edited after their vector was made, but not
	// in the text that is embedded (a change of confidence, say). Their vectors
	// are right; the job says so with TouchKnowledgeEmbeddings(ids, ReadAt) so
	// that they are not looked at again.
	Unchanged []int64
	// ReadAt is the time taken before any text was read. Pass it back with the
	// vectors and with Unchanged.
	ReadAt string
}

// EmbedStatus counts the vectors of the active entries.
type EmbedStatus struct {
	// Active is the number of active entries, the ones that get a vector.
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
	// for an entry that has no usable vector.
	stored string
}

// KnowledgeEmbeddingDue returns up to limit active entries whose vector is
// missing, made by another model or text format, or made from a text that has
// changed, newest edit first so that a fresh entry does not wait behind a
// backlog. at is the time of the call; an entry parked after failures is left
// out until its retry time.
//
// The texts are read in pages of a few entries, never all at once, and only for
// entries that can be due: the ones without a current vector, and the ones that
// were edited at or after the time their vector was made (updated_at is the one
// place where the text of an entry changes). With full, the second group is
// every entry that has a vector, which catches a text changed some other way; the
// job asks for it once, when it starts.
func (s *Store) KnowledgeEmbeddingDue(m EmbedModel, limit int, at time.Time, full bool) (EmbedDue, error) {
	if s.reader != nil {
		return s.reader.KnowledgeEmbeddingDue(m, limit, at, full)
	}
	if limit <= 0 {
		limit = 8
	}
	due := EmbedDue{ReadAt: now()}
	missing, err := s.dueCandidates(`SELECT k.id, '' FROM knowledge k
		LEFT JOIN knowledge_embeddings e ON e.knowledge_id = k.id
		WHERE k.status = 'active' AND (e.knowledge_id IS NULL OR e.model != ? OR e.prefix_version != ?)
		ORDER BY k.updated_at DESC, k.id DESC`, m.Model, m.PrefixVersion)
	if err != nil {
		return due, err
	}
	if err := s.collectDue(&due, missing, m, limit, at, full); err != nil || len(due.Items) >= limit {
		return due, err
	}
	changed := `SELECT k.id, e.text_hash FROM knowledge k
		JOIN knowledge_embeddings e ON e.knowledge_id = k.id
		WHERE k.status = 'active' AND e.model = ? AND e.prefix_version = ?`
	if !full {
		changed += ` AND k.updated_at >= e.read_at`
	}
	maybe, err := s.dueCandidates(changed+` ORDER BY k.updated_at DESC, k.id DESC`, m.Model, m.PrefixVersion)
	if err != nil {
		return due, err
	}
	err = s.collectDue(&due, maybe, m, limit, at, full)
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
func (s *Store) collectDue(due *EmbedDue, cands []dueCandidate, m EmbedModel, limit int, at time.Time, full bool) error {
	stamp := at.UTC().Format(time.RFC3339)
	for start := 0; start < len(cands) && len(due.Items) < limit; start += loadChunk {
		part := cands[start:min(start+loadChunk, len(cands))]
		args := make([]any, len(part))
		for i, c := range part {
			args[i] = c.id
		}
		texts, err := s.entryTexts(args)
		if err != nil {
			return err
		}
		failures, err := s.embeddingFailures(args)
		if err != nil {
			return err
		}
		for _, c := range part {
			t, ok := texts[c.id]
			if !ok {
				continue
			}
			doc := embed.DocumentText(t[0], t[1])
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

func (s *Store) entryTexts(ids []any) (map[int64][2]string, error) {
	rows, err := s.db.Query(`SELECT id, title, body FROM knowledge WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][2]string, len(ids))
	for rows.Next() {
		var id int64
		var t [2]string
		if err := rows.Scan(&id, &t[0], &t[1]); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (s *Store) embeddingFailures(ids []any) (map[int64]parkedFailure, error) {
	rows, err := s.db.Query(`SELECT knowledge_id, model, prefix_version, text_hash, attempts, permanent, retry_at
		FROM knowledge_embedding_failures WHERE knowledge_id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]parkedFailure{}
	for rows.Next() {
		var id int64
		var f parkedFailure
		var permanent int
		if err := rows.Scan(&id, &f.model, &f.prefix, &f.hash, &f.attempts, &permanent, &f.retryAt); err != nil {
			return nil, err
		}
		f.permanent = permanent != 0
		out[id] = f
	}
	return out, rows.Err()
}

// KnowledgeEmbeddingStatus counts what is embedded and what is due, and reports
// how the job last fared. It reads the text of every active entry, which is fine
// for a command and not for a hot path.
func (s *Store) KnowledgeEmbeddingStatus(m EmbedModel) (EmbedStatus, error) {
	if s.reader != nil {
		return s.reader.KnowledgeEmbeddingStatus(m)
	}
	var st EmbedStatus
	stamp := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.Query(`SELECT k.title, k.body, e.model, e.prefix_version, e.text_hash,
			f.model, f.prefix_version, f.text_hash, f.permanent, f.retry_at
		FROM knowledge k LEFT JOIN knowledge_embeddings e ON e.knowledge_id = k.id
		LEFT JOIN knowledge_embedding_failures f ON f.knowledge_id = k.id
		WHERE k.status = 'active'`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var title, body string
		var model, prefix, hash, fModel, fPrefix, fHash, fRetry sql.NullString
		var fPermanent sql.NullInt64
		if err := rows.Scan(&title, &body, &model, &prefix, &hash, &fModel, &fPrefix, &fHash, &fPermanent, &fRetry); err != nil {
			return st, err
		}
		st.Active++
		current := embed.TextHash(embed.DocumentText(title, body))
		switch {
		case !model.Valid:
		case model.String != m.Model || prefix.String != m.PrefixVersion:
			st.OtherModel++
		case current == hash.String:
			st.Current++
			continue
		}
		if fModel.Valid && fModel.String == m.Model && fPrefix.String == m.PrefixVersion && fHash.String == current &&
			(fPermanent.Int64 != 0 || fRetry.String > stamp) {
			st.Parked++
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.Due = st.Active - st.Current
	err = s.db.QueryRow(`SELECT last_ok_at,last_error,last_error_at,failures FROM knowledge_embedding_state WHERE id=1`).
		Scan(&st.Job.LastOKAt, &st.Job.LastError, &st.Job.LastErrorAt, &st.Job.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return st, err
}
