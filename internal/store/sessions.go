package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

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
	// Visibility ist die Freigabestufe: private, project (Mitglieder) oder
	// guests (Mitglieder und Gäste). Shared ist true ab project.
	Visibility string `json:"visibility,omitempty"`
	// PublicID ist die Adresse in der Weboberfläche: zufällig, ohne Zählfolge.
	PublicID string `json:"public_id,omitempty"`
	// Title: ai-title der Session, sonst die erste Nutzernachricht (gekürzt).
	Title string `json:"title,omitempty"`
	// Messages zählt Nutzer- und Assistententexte (aus dem Index).
	Messages int `json:"messages,omitempty"`
}

// SessionRef ist, womit ein Client eine Session anspricht: Mitglieder kennen die
// laufende Nummer, Gäste nur die zufällige Adresse.
type SessionRef struct {
	ID       int64  `json:"id,omitempty"`
	PublicID string `json:"public_id,omitempty"`
}

// PathSegment ist der Teil des Pfads /api/sessions/{ref}.
func (r SessionRef) PathSegment() string {
	if r.ID != 0 {
		return strconv.FormatInt(r.ID, 10)
	}
	return r.PublicID
}

// Zero: weder Nummer noch Adresse bekannt.
func (r SessionRef) Zero() bool { return r.ID == 0 && r.PublicID == "" }

// Freigabestufen einer Session.
const (
	VisPrivate = "private"
	VisProject = "project"
	VisGuests  = "guests"
)

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

const sessionCols = `id, harness, external_id, project, branch, machine, cwd, started_at, last_seen_at, account_id, shared, visibility, public_id, title, msg_count`

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
	publicID, err := newPublicID()
	if err != nil {
		return 0, err
	}
	err = s.db.QueryRow(`INSERT INTO sessions(harness, external_id, project, branch, machine, cwd, started_at, last_seen_at, account_id, public_id)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(harness, external_id) DO UPDATE SET
		  project = excluded.project, branch = excluded.branch, machine = excluded.machine,
		  cwd = excluded.cwd, last_seen_at = excluded.last_seen_at, account_id = excluded.account_id
		WHERE (CASE WHEN sessions.account_id = 0 THEN ? ELSE sessions.account_id END) = excluded.account_id
		  AND (sessions.machine = '' OR sessions.machine = excluded.machine)
		RETURNING id`,
		sess.Harness, sess.ExternalID, sess.Scope.Project, sess.Scope.Branch, sess.Scope.Machine,
		sess.CWD, sess.StartedAt, now(), account, publicID, owner).Scan(&id)
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
	harness := map[int64]string{}
	var fresh []chunkRow
	for _, batch := range batches {
		h, ok := harness[batch.SessionID]
		if !ok {
			if err := tx.QueryRow(`SELECT harness FROM sessions WHERE id = ?`, batch.SessionID).Scan(&h); err != nil {
				return err
			}
			harness[batch.SessionID] = h
		}
		for _, c := range batch.Chunks {
			res, err := stmt.Exec(batch.SessionID, c.Seq, c.Role, c.Text, c.Raw)
			if err != nil {
				return err
			}
			// Nur ein wirklich neuer Chunk wird indiziert.
			if n, _ := res.RowsAffected(); n == 1 {
				id, err := res.LastInsertId()
				if err != nil {
					return err
				}
				fresh = append(fresh, chunkRow{id: id, sessionID: batch.SessionID, seq: c.Seq, harness: h, raw: c.Raw})
			}
		}
	}
	if err := indexChunks(tx, fresh); err != nil {
		return err
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

// ListSessionsVisible liefert die neuesten Sessions, für die keep gilt, höchstens
// limit viele. Die Sichtbarkeit entscheidet vor dem Abschneiden: wer nur einen
// Teil des Bestands sehen darf, bekommt dieselbe Antwort, egal wie viele und wie
// aktuelle verborgene Sessions es gibt (#2447). pre schränkt die Abfrage schon
// in SQL auf die lesbare Menge ein; keep prüft die übrigen Zeilen (nil: alle).
func (s *Store) ListSessionsVisible(filter scope.Axes, limit int, ownerPrincipalID string, keep func(Session) bool, pre SessionPrefilter) ([]Session, error) {
	if s.reader != nil {
		return s.reader.ListSessionsVisible(filter, limit, ownerPrincipalID, keep, pre)
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
	where, args = pre.apply(where, args)
	out := []Session{}
	err := s.eachSession(where, args, func(sess Session) (bool, error) {
		if keep == nil || keep(sess) {
			out = append(out, sess)
		}
		return len(out) < limit, nil
	})
	return out, err
}

// visibleSessionIDs sind die Nummern der Sessions, für die keep gilt.
func (s *Store) visibleSessionIDs(filter scope.Axes, keep func(Session) bool, pre SessionPrefilter) ([]int64, error) {
	where, args := filter.FilterWhere()
	where, args = pre.apply(where, args)
	ids := []int64{}
	err := s.eachSession(where, args, func(sess Session) (bool, error) {
		if keep == nil || keep(sess) {
			ids = append(ids, sess.ID)
		}
		return true, nil
	})
	return ids, err
}

func idsJSONOf(ids []int64) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

// eachSession ruft fn für die Sessions, neueste zuerst, bis fn false liefert.
// Die Reihenfolge steht mit einer einzigen Abfrage fest (nur die Nummern, eine
// Anweisung ist ein Schnappschuss); die Zeilen kommen danach seitenweise nach
// Nummer. So verschiebt ein Upload, der last_seen_at ändert, während gelesen
// wird, weder Zeilen noch doppelt sie, und es bleibt keine Abfrage offen,
// während fn läuft.
func (s *Store) eachSession(where string, args []any, fn func(Session) (bool, error)) error {
	const page = 500
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE `+where+` ORDER BY last_seen_at DESC, id DESC`, args...)
	if err != nil {
		return err
	}
	var order []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for start := 0; start < len(order); start += page {
		end := min(start+page, len(order))
		ids := order[start:end]
		rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE id IN (SELECT value FROM json_each(?))`, idsJSONOf(ids))
		if err != nil {
			return err
		}
		got, err := scanSessions(rows)
		if err != nil {
			return err
		}
		if err := s.fillSessionOwners(got); err != nil {
			return err
		}
		byID := make(map[int64]Session, len(got))
		for _, sess := range got {
			byID[sess.ID] = sess
		}
		for _, id := range ids {
			sess, ok := byID[id]
			if !ok {
				continue
			}
			more, err := fn(sess)
			if err != nil || !more {
				return err
			}
		}
	}
	return nil
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
	return s.SearchSessionsVisible(q, filter, excludeSession, limit, nil, SessionPrefilter{})
}

// SearchSessionsVisible sucht nur in Sessions, für die keep gilt, und schneidet
// erst danach auf limit ab. Die lesbare Menge steht vor der Suche fest und geht
// als Liste in die Abfrage: der Rang verborgener Treffer kann so weder
// verdrängen noch verraten, wie viele es gibt (#2447). Ohne keep sucht sie im
// ganzen Bestand.
func (s *Store) SearchSessionsVisible(q string, filter scope.Axes, excludeSession string, limit int, keep func(Session) bool, pre SessionPrefilter) ([]SessionHit, error) {
	if s.reader != nil {
		return s.reader.SearchSessionsVisible(q, filter, excludeSession, limit, keep, pre)
	}
	if limit <= 0 {
		limit = 20
	}
	where, args := filter.FilterWhere()
	args = append([]any{ftsQuery(q), excludeSession, excludeSession}, args...)
	if keep != nil || pre.Where != "" {
		ids, err := s.visibleSessionIDs(filter, keep, pre)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []SessionHit{}, nil
		}
		where += ` AND se.id IN (SELECT value FROM json_each(?))`
		args = append(args, idsJSONOf(ids))
	}
	args = append(args, limit)
	// bm25 (f.rank) hängt vom ganzen Index ab, auch von verborgenen Sessions:
	// wer nur eine Teilmenge lesen darf, bekommt die Reihenfolge allein aus
	// der lesbaren Menge.
	order := "h.r"
	if !pre.Unrestricted && (keep != nil || pre.Where != "") {
		order = "COUNT(*) OVER (PARTITION BY se.id) DESC, se.last_seen_at DESC, se.id DESC, c.seq"
	}
	// snippet() liest den Text jeder Zeile, die es liefert: in der CTE liefe es
	// für jeden Treffer des ganzen Index. Sie führt nur Zeile und Rang; den
	// Ausschnitt gibt es danach für die höchstens limit Ergebniszeilen.
	rows, err := s.db.Query(`WITH h AS MATERIALIZED (SELECT rowid AS cid, rank AS r FROM chunks_fts WHERE chunks_fts MATCH ?)
		SELECT `+prefix(sessionCols, "se.")+`, c.seq, h.cid
		FROM h
		JOIN session_chunks c ON c.id = h.cid
		JOIN sessions se ON se.id = c.session_id
		WHERE (? = '' OR se.external_id != ?) AND `+where+`
		ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionHit{}
	var cids []int64
	for rows.Next() {
		var h SessionHit
		var cid int64
		if err := scanSession(rows, &h.Session, &h.Seq, &cid); err != nil {
			return nil, err
		}
		out = append(out, h)
		cids = append(cids, cid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(cids) > 0 {
		snips := map[int64]string{}
		srows, err := s.db.Query(`SELECT rowid, snippet(chunks_fts, 0, '', '', '…', 12) FROM chunks_fts
			WHERE rowid IN (SELECT value FROM json_each(?)) AND chunks_fts MATCH ?`, idsJSONOf(cids), ftsQuery(q))
		if err != nil {
			return nil, err
		}
		for srows.Next() {
			var id int64
			var snip string
			if err := srows.Scan(&id, &snip); err != nil {
				srows.Close()
				return nil, err
			}
			snips[id] = snip
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return nil, err
		}
		for i := range out {
			out[i].Snippet = snips[cids[i]]
		}
	}
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

// scanSession liest die Spalten von sessionCols und danach extra.
func scanSession(rows interface{ Scan(...any) error }, s *Session, extra ...any) error {
	var shared int
	dest := []any{&s.ID, &s.Harness, &s.ExternalID,
		&s.Scope.Project, &s.Scope.Branch, &s.Scope.Machine,
		&s.CWD, &s.StartedAt, &s.LastSeenAt, &s.AccountID, &shared, &s.Visibility, &s.PublicID, &s.Title, &s.Messages}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	// Eine alte Zeile mit shared=1 ohne Stufe ist eine Freigabe für Mitglieder.
	if shared != 0 && (s.Visibility == "" || s.Visibility == VisPrivate) {
		s.Visibility = VisProject
	}
	if s.Visibility == "" {
		s.Visibility = VisPrivate
	}
	s.Shared = s.Visibility != VisPrivate
	return nil
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := scanSession(rows, &s); err != nil {
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
	level := VisPrivate
	if shared {
		level = VisProject
		var cur string
		if err := s.db.QueryRow(`SELECT visibility FROM sessions WHERE id=?`, id).Scan(&cur); err == nil && cur == VisGuests {
			level = VisGuests
		}
	}
	return s.setVisibility(id, level, accountPrincipal)
}

// ErrNotSessionOwner: nur der Besitzer einer Session gibt sie frei.
var ErrNotSessionOwner = errors.New("only the owner of a session may share it")
