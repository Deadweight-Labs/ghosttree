package store

import (
	"database/sql"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type Session struct {
	ID         int64      `json:"id"`
	Harness    string     `json:"harness"` // claude-code|codex
	ExternalID string     `json:"external_id"`
	Scope      scope.Axes `json:"scope"`
	CWD        string     `json:"cwd"`
	StartedAt  string     `json:"started_at"`
	LastSeenAt string     `json:"last_seen_at"`
}

type Chunk struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"` // user|assistant|other
	Text string `json:"text"` // extracted, redacted text ('' if not understood)
	Raw  string `json:"raw"`  // full redacted JSONL line
}

type SessionHit struct {
	Session Session `json:"session"`
	Seq     int     `json:"seq"`
	Snippet string  `json:"snippet"`
}

const sessionCols = `id, harness, external_id, project, branch, machine, cwd, started_at, last_seen_at`

func (s *Store) UpsertSession(sess Session) (int64, error) {
	if sess.StartedAt == "" {
		sess.StartedAt = now()
	}
	var id int64
	err := s.db.QueryRow(`INSERT INTO sessions(harness, external_id, project, branch, machine, cwd, started_at, last_seen_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(harness, external_id) DO UPDATE SET
		  project = excluded.project, branch = excluded.branch, machine = excluded.machine,
		  cwd = excluded.cwd, last_seen_at = excluded.last_seen_at
		RETURNING id`,
		sess.Harness, sess.ExternalID, sess.Scope.Project, sess.Scope.Branch, sess.Scope.Machine,
		sess.CWD, sess.StartedAt, now()).Scan(&id)
	return id, err
}

func (s *Store) AppendChunks(sessionID int64, chunks []Chunk) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		if _, err := stmt.Exec(sessionID, c.Seq, c.Role, c.Text, c.Raw); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now(), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListSessions(filter scope.Axes, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := filter.FilterWhere()
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE `+where+`
		ORDER BY last_seen_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanSessions(rows)
}

// SessionsPendingDistillation returns sessions that have never been distilled
// and have been idle since idleBefore, oldest first. Ordering matters: the
// distiller must drain the archive rather than revisit the newest window.
//
// Sessions sitting in an open batch are held back. Their result is up to 24
// hours away and no distillation row exists yet, so without this an hourly
// timer would resubmit — and pay for — the same transcript all day.
func (s *Store) SessionsPendingDistillation(filter scope.Axes, idleBefore string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := filter.FilterWhere()
	args = append(args, idleBefore, limit)
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions
		WHERE `+where+` AND last_seen_at < ?
		  AND NOT EXISTS (SELECT 1 FROM session_distillations d WHERE d.session_id = sessions.id)
		  AND NOT EXISTS (SELECT 1 FROM distill_batch_items i
		                  JOIN distill_batches b ON b.id = i.batch_id
		                  WHERE i.session_id = sessions.id AND b.state = 'open')
		ORDER BY last_seen_at ASC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanSessions(rows)
}

// SessionByID reads one session. The batch collector needs the scope hours
// after submission, and reads it fresh rather than from its own record: a
// scope that was re-canonicalized in the meantime should file the result under
// the corrected project, not the one that was current at submission time.
func (s *Store) SessionByID(id int64) (Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id)
	if err != nil {
		return Session{}, err
	}
	found, err := scanSessions(rows)
	if err != nil {
		return Session{}, err
	}
	if len(found) == 0 {
		return Session{}, sql.ErrNoRows
	}
	return found[0], nil
}

func (s *Store) ReadSession(id int64, fromSeq, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT seq, role, text, raw FROM session_chunks
		WHERE session_id = ? AND seq >= ? ORDER BY seq LIMIT ?`, id, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Seq, &c.Role, &c.Text, &c.Raw); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SessionRaw returns every stored JSONL line of a session in file order.
// Deliberately unpaginated: it reconstructs the original transcript, and a
// partial transcript is not an archive.
func (s *Store) SessionRaw(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT raw FROM session_chunks WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

func (s *Store) SearchSessions(q string, filter scope.Axes, limit int) ([]SessionHit, error) {
	if limit <= 0 {
		limit = 20
	}
	where, args := filter.FilterWhere()
	args = append([]any{ftsQuery(q)}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(sessionCols, "se.")+`, c.seq,
		snippet(chunks_fts, 0, '', '', '…', 12)
		FROM chunks_fts f
		JOIN session_chunks c ON c.id = f.rowid
		JOIN sessions se ON se.id = c.session_id
		WHERE chunks_fts MATCH ? AND `+where+`
		ORDER BY f.rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHit{}
	for rows.Next() {
		var h SessionHit
		if err := rows.Scan(&h.Session.ID, &h.Session.Harness, &h.Session.ExternalID,
			&h.Session.Scope.Project, &h.Session.Scope.Branch, &h.Session.Scope.Machine,
			&h.Session.CWD, &h.Session.StartedAt, &h.Session.LastSeenAt,
			&h.Seq, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Harness, &s.ExternalID,
			&s.Scope.Project, &s.Scope.Branch, &s.Scope.Machine,
			&s.CWD, &s.StartedAt, &s.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
