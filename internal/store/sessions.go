package store

import (
	"context"
	"database/sql"
	"errors"

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
	// AccountID stempelt der Server beim Upload aus dem Token; ein Client kann
	// ihn nicht setzen (json:"-"). 0 heißt Altbestand und gehört dem
	// Instanz-Owner. Owner ist der Kontoname, nur in Antworten.
	AccountID int64  `json:"-"`
	Owner     string `json:"owner,omitempty"`
	// Shared: der Besitzer hat das Transkript für die Mitglieder des Projekts
	// freigegeben (Spec 8.1). Owner und Lead brauchen die Freigabe nicht.
	// Gesetzt wird es nur über SetSessionShared, nie aus einem Upload.
	Shared bool `json:"shared,omitempty"`
}

type Chunk struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"` // user|assistant|other
	Text string `json:"text"` // extracted, redacted text ('' if not understood)
	Raw  string `json:"raw"`  // full redacted JSONL line
}

type ChunkBatch struct {
	SessionID int64
	Chunks    []Chunk
}

type SessionHit struct {
	Session Session `json:"session"`
	Seq     int     `json:"seq"`
	Snippet string  `json:"snippet"`
}

const sessionCols = `id, harness, external_id, project, branch, machine, cwd, started_at, last_seen_at, account_id, shared`

func (s *Store) UpsertSession(sess Session) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{sess}, func(d *Store, p []any) (int64, error) { return d.UpsertSession(p[0].(Session)) })
	}
	if sess.StartedAt == "" {
		sess.StartedAt = now()
	}
	owner := instanceOwnerID(s.db)
	account := sess.AccountID
	if account == 0 {
		account = owner
	}
	// Die Kollisionsregel steht im WHERE des Upserts: gleiche Zeile nur für
	// dasselbe Konto und dieselbe Maschine (eine leere gespeicherte Maschine
	// darf gesetzt werden). Sonst liefert RETURNING keine Zeile.
	var id int64
	err := s.db.QueryRow(`INSERT INTO sessions(harness, external_id, project, branch, machine, cwd, started_at, last_seen_at, account_id)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(harness, external_id) DO UPDATE SET
		  project = excluded.project, branch = excluded.branch, machine = excluded.machine,
		  cwd = excluded.cwd, last_seen_at = excluded.last_seen_at, account_id = excluded.account_id
		WHERE (CASE WHEN sessions.account_id = 0 THEN ? ELSE sessions.account_id END) = excluded.account_id
		  AND (sessions.machine = '' OR sessions.machine = excluded.machine)
		RETURNING id`,
		sess.Harness, sess.ExternalID, sess.Scope.Project, sess.Scope.Branch, sess.Scope.Machine,
		sess.CWD, sess.StartedAt, now(), account, owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionCollision
	}
	return id, err
}

func (s *Store) AppendChunks(sessionID int64, chunks []Chunk) error {
	if s.writer != nil {
		r, err := s.writer.admitChunks(context.Background(), ChunkBatch{SessionID: sessionID, Chunks: chunks})
		if err != nil {
			return err
		}
		return <-r.done
	}
	return s.AppendChunkBatches([]ChunkBatch{{SessionID: sessionID, Chunks: chunks}})
}

func (s *Store) AppendChunkBatches(batches []ChunkBatch) error {
	if s.writer != nil {
		return queueWrite(s, []any{batches}, func(d *Store, p []any) error { return d.AppendChunkBatches(p[0].([]ChunkBatch)) })
	}
	if len(batches) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sessions := make(map[int64]struct{}, len(batches))
	ts := now()
	for _, batch := range batches {
		if _, ok := sessions[batch.SessionID]; ok {
			continue
		}
		result, err := tx.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, ts, batch.SessionID)
		if err != nil {
			return err
		}
		matched, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if matched == 0 {
			return sql.ErrNoRows
		}
		sessions[batch.SessionID] = struct{}{}
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO session_chunks(session_id, seq, role, text, raw) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, batch := range batches {
		for _, c := range batch.Chunks {
			if _, err := stmt.Exec(batch.SessionID, c.Seq, c.Role, c.Text, c.Raw); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) ListSessions(filter scope.Axes, limit int) ([]Session, error) {
	if s.reader != nil {
		return s.reader.ListSessions(filter, limit)
	}
	return s.ListSessionsOwned(filter, limit, "")
}

// ListSessionsOwned ist ListSessions, auf Sessions eines Kontos eingegrenzt,
// wenn ownerPrincipalID gesetzt ist (?owner=me). Altbestand zählt zum
// Instanz-Owner.
func (s *Store) ListSessionsOwned(filter scope.Axes, limit int, ownerPrincipalID string) ([]Session, error) {
	if s.reader != nil {
		return s.reader.ListSessionsOwned(filter, limit, ownerPrincipalID)
	}
	if limit <= 0 {
		limit = 50
	}
	where, args := filter.FilterWhere()
	if ownerPrincipalID != "" {
		id, ok := accountNumericID(ownerPrincipalID)
		if !ok {
			return []Session{}, nil
		}
		where += ` AND (CASE WHEN account_id = 0 THEN ? ELSE account_id END) = ?`
		args = append(args, instanceOwnerID(s.db), id)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE `+where+`
		ORDER BY last_seen_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	out, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	if err := s.fillSessionOwners(out); err != nil {
		return nil, err
	}
	return out, nil
}

// SessionsPendingDistillation returns sessions that have never been distilled
// and have been idle since idleBefore, oldest first. Ordering matters: the
// distiller must drain the archive rather than revisit the newest window.
//
// Sessions sitting in an open batch are held back. Their result is up to 24
// hours away and no distillation row exists yet, so without this an hourly
// timer would resubmit — and pay for — the same transcript all day.
// promptVersion scopes the queue to one generation of one mode. Without it a
// session distilled for knowledge counts as done for wishes too — two modes
// read the same transcripts for different things, and the first to run would
// take the whole archive off the second one's queue.
func (s *Store) SessionsPendingDistillation(filter scope.Axes, idleBefore, promptVersion string, limit int) ([]Session, error) {
	if s.reader != nil {
		return s.reader.SessionsPendingDistillation(filter, idleBefore, promptVersion, limit)
	}
	if limit <= 0 {
		limit = 50
	}
	where, args := filter.FilterWhere()
	args = append(args, idleBefore, promptVersion, promptVersion, limit)
	// project != '' belongs in SQL, not in the caller's loop. Findings from a
	// session that ran outside a repository have nowhere to be filed, and
	// discarding them after the LIMIT shrinks the window unpredictably: the
	// oldest sessions are largely project-less, so a run asking for 100
	// candidates skipped 98 and submitted 2.
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions
		WHERE `+where+` AND last_seen_at < ? AND project != ''
		  AND NOT EXISTS (SELECT 1 FROM session_distillations d
		                  WHERE d.session_id = sessions.id AND d.prompt_version = ?)
		  AND NOT EXISTS (SELECT 1 FROM distill_batch_items i
		                  JOIN distill_batches b ON b.id = i.batch_id
		                  WHERE i.session_id = sessions.id AND b.state = 'open'
		                    AND i.prompt_version = ?)
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
	if s.reader != nil {
		return s.reader.SessionByID(id)
	}
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
	if err := s.fillSessionOwners(found); err != nil {
		return Session{}, err
	}
	return found[0], nil
}

func (s *Store) ReadSession(id int64, fromSeq, limit int) ([]Chunk, error) {
	if s.reader != nil {
		return s.reader.ReadSession(id, fromSeq, limit)
	}
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
	if s.reader != nil {
		return s.reader.SessionRaw(id)
	}
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

func (s *Store) SearchSessions(q string, filter scope.Axes, excludeSession string, limit int) ([]SessionHit, error) {
	if s.reader != nil {
		return s.reader.SearchSessions(q, filter, excludeSession, limit)
	}
	if limit <= 0 {
		limit = 20
	}
	where, args := filter.FilterWhere()
	args = append([]any{ftsQuery(q), excludeSession, excludeSession}, args...)
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+prefix(sessionCols, "se.")+`, c.seq,
		snippet(chunks_fts, 0, '', '', '…', 12)
		FROM chunks_fts f
		JOIN session_chunks c ON c.id = f.rowid
		JOIN sessions se ON se.id = c.session_id
		WHERE chunks_fts MATCH ? AND (? = '' OR se.external_id != ?) AND `+where+`
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
			&h.Session.CWD, &h.Session.StartedAt, &h.Session.LastSeenAt, &h.Session.AccountID, &h.Session.Shared,
			&h.Seq, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	one := make([]Session, len(out))
	for i := range out {
		one[i] = out[i].Session
	}
	if err := s.fillSessionOwners(one); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Session = one[i]
	}
	return out, nil
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Harness, &s.ExternalID,
			&s.Scope.Project, &s.Scope.Branch, &s.Scope.Machine,
			&s.CWD, &s.StartedAt, &s.LastSeenAt, &s.AccountID, &s.Shared); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetSessionShared gibt ein Transkript für die Mitglieder seines Projekts frei
// oder nimmt die Freigabe zurück. Nur der Besitzer darf das (ErrNotSessionOwner).
func (s *Store) SetSessionShared(id int64, accountPrincipal string, shared bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, accountPrincipal, shared}, func(d *Store, p []any) error {
			return d.SetSessionShared(p[0].(int64), p[1].(string), p[2].(bool))
		})
	}
	acct, ok := accountNumericID(accountPrincipal)
	if !ok {
		return ErrNotSessionOwner
	}
	var stored int64
	if err := s.db.QueryRow(`SELECT account_id FROM sessions WHERE id=?`, id).Scan(&stored); err != nil {
		return err
	}
	if effectiveOwner(stored, instanceOwnerID(s.db)) != acct {
		return ErrNotSessionOwner
	}
	v := 0
	if shared {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE sessions SET shared=? WHERE id=?`, v, id)
	return err
}

// ErrNotSessionOwner: nur der Besitzer einer Session gibt sie frei.
var ErrNotSessionOwner = errors.New("only the owner of a session may share it")
