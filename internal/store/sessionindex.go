package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// Suchindex der Sessions (REQ-435 P10).
//
// chunk_index hält je Chunk die aus raw abgeleiteten Felder (Nachrichten,
// Denkblöcke, Werkzeug-Eingabe, Werkzeug-Ergebnis), jedes auf 8 KB gekürzt;
// sess_fts ist der Volltextindex darüber. raw bleibt unverändert. Neue Chunks
// werden beim Schreiben in derselben Transaktion indiziert. Den Bestand holt
// ein Hintergrundlauf in kleinen Schritten nach (RunIndexBackfill); sein
// Fortschritt steht in index_state und überlebt einen Neustart.

const sessionIndexDDL = `
CREATE TABLE IF NOT EXISTS chunk_index(
  chunk_id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL DEFAULT '',
  ts TEXT NOT NULL DEFAULT '',
  is_prompt INTEGER NOT NULL DEFAULT 0,
  tool_calls INTEGER NOT NULL DEFAULT 0,
  text TEXT NOT NULL DEFAULT '',
  thinking TEXT NOT NULL DEFAULT '',
  tool_input TEXT NOT NULL DEFAULT '',
  tool_output TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS chunk_index_session ON chunk_index(session_id, seq);
CREATE VIRTUAL TABLE IF NOT EXISTS sess_fts USING fts5(text, thinking, tool_input, tool_output,
  content='chunk_index', content_rowid='chunk_id');
CREATE TABLE IF NOT EXISTS index_state(key TEXT PRIMARY KEY, val INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS session_share_events(
  id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL,
  old_level TEXT NOT NULL, new_level TEXT NOT NULL,
  actor TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS session_share_events_no_update BEFORE UPDATE ON session_share_events
  BEGIN SELECT RAISE(ABORT, 'session_share_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS session_share_events_no_delete BEFORE DELETE ON session_share_events
  BEGIN SELECT RAISE(ABORT, 'session_share_events is append-only'); END;`

// publicIDAlphabet lässt die leicht verwechselbaren Zeichen l, o, 0 und 1 weg.
const publicIDAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// newPublicID erzeugt die zufällige Adresse einer Session (12 Zeichen, 60 Bit).
func newPublicID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = publicIDAlphabet[int(v)%len(publicIDAlphabet)]
	}
	return string(out), nil
}

// ensureSessionIndex legt Spalten, Tabellen und Adressen an. Alles hier ist
// klein oder eine reine Metadaten-Änderung; das Nachindizieren der Chunks läuft
// getrennt (startIndexBackfill, RunIndexBackfill).
func ensureSessionIndex(db *sql.DB) error {
	for _, c := range []struct{ name, ddl string }{
		{"title", `TEXT NOT NULL DEFAULT ''`},
		{"public_id", `TEXT NOT NULL DEFAULT ''`},
		{"visibility", `TEXT NOT NULL DEFAULT 'private'`},
		{"msg_count", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := ensureColumn(db, "sessions", c.name, c.ddl); err != nil {
			return err
		}
	}
	if _, err := db.Exec(sessionIndexDDL); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS sessions_public_id ON sessions(public_id) WHERE public_id != ''`); err != nil {
		return err
	}
	// Die alte Freigabe (shared=1) war die Stufe "Mitglieder".
	if _, err := db.Exec(`UPDATE sessions SET visibility='project' WHERE shared=1 AND visibility='private'`); err != nil {
		return err
	}
	return assignPublicIDs(db)
}

// assignPublicIDs vergibt Adressen an Zeilen ohne (Altbestand, zurückgerollte
// Binarys). Die Tabelle sessions ist klein; wiederholbar.
func assignPublicIDs(db *sql.DB) error {
	rows, err := db.Query(`SELECT id FROM sessions WHERE public_id = ''`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		pid, err := newPublicID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE sessions SET public_id=? WHERE id=? AND public_id=''`, pid, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// chunkRow ist ein zu indizierender Chunk.
type chunkRow struct {
	id, sessionID int64
	seq           int
	harness, raw  string
}

type sessionDelta struct {
	messages    int
	aiTitle     string
	firstPrompt string
}

// indexChunks schreibt die Indexzeilen. Ein Chunk, der schon eine Zeile hat,
// wird übersprungen und nicht noch einmal gezählt; deshalb dürfen Schreibpfad,
// Hintergrundlauf und IndexSession sich überschneiden.
func indexChunks(tx *sql.Tx, rows []chunkRow) error {
	if len(rows) == 0 {
		return nil
	}
	ins, err := tx.Prepare(`INSERT OR IGNORE INTO chunk_index(chunk_id, session_id, seq, role, ts, is_prompt, tool_calls, text, thinking, tool_input, tool_output)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	fts, err := tx.Prepare(`INSERT INTO sess_fts(rowid, text, thinking, tool_input, tool_output) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer fts.Close()
	deltas := map[int64]*sessionDelta{}
	var order []int64
	for _, r := range rows {
		p := transcript.Parse(r.harness, r.seq, r.raw)
		f := transcript.Fields(p)
		prompt, role := 0, f.Role
		if f.Prompt {
			prompt = 1
		}
		res, err := ins.Exec(r.id, r.sessionID, r.seq, role, f.Time, prompt, f.ToolCalls, f.Text, f.Thinking, f.ToolInput, f.ToolOutput)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if f.Text != "" || f.Thinking != "" || f.ToolInput != "" || f.ToolOutput != "" {
			if _, err := fts.Exec(r.id, f.Text, f.Thinking, f.ToolInput, f.ToolOutput); err != nil {
				return err
			}
		}
		d := deltas[r.sessionID]
		if d == nil {
			d = &sessionDelta{}
			deltas[r.sessionID] = d
			order = append(order, r.sessionID)
		}
		d.messages += f.Messages
		if p.Title != "" {
			d.aiTitle = p.Title
		}
		if f.Prompt && d.firstPrompt == "" {
			d.firstPrompt = transcript.OneLine(f.Text, 80)
		}
	}
	for _, id := range order {
		d := deltas[id]
		if d.messages != 0 {
			if _, err := tx.Exec(`UPDATE sessions SET msg_count = msg_count + ? WHERE id = ?`, d.messages, id); err != nil {
				return err
			}
		}
		switch {
		case d.aiTitle != "":
			_, err = tx.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, transcript.OneLine(d.aiTitle, 120), id)
		case d.firstPrompt != "":
			_, err = tx.Exec(`UPDATE sessions SET title = ? WHERE id = ? AND title = ''`, d.firstPrompt, id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------ Nachindizieren

// startIndexBackfill setzt den Nachindizierlauf auf, falls er noch nie lief:
// alles bis zur größten heutigen Chunk-Nummer ist nachzuholen, später
// geschriebene Chunks sind es schon.
func (s *Store) startIndexBackfill() error {
	if s.writer != nil {
		return queueWrite(s, nil, func(d *Store, _ []any) error { return d.startIndexBackfill() })
	}
	var have int
	if err := s.db.QueryRow(`SELECT count(*) FROM index_state WHERE key='backfill_bound'`).Scan(&have); err != nil || have > 0 {
		return err
	}
	var bound int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM session_chunks`).Scan(&bound); err != nil {
		return err
	}
	done := 0
	if bound == 0 {
		done = 1
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO index_state(key,val) VALUES('backfill_bound',?),('backfill_cursor',0),('backfill_done',?)`, bound, done)
	return err
}

// IndexProgress ist der sichtbare Stand des Nachindizierens.
type IndexProgress struct {
	Done    bool
	Percent int
}

func (s *Store) IndexProgress() IndexProgress {
	if s.reader != nil {
		return s.reader.IndexProgress()
	}
	state := map[string]int64{}
	rows, err := s.db.Query(`SELECT key, val FROM index_state`)
	if err != nil {
		return IndexProgress{Done: true, Percent: 100}
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v int64
		if rows.Scan(&k, &v) == nil {
			state[k] = v
		}
	}
	if _, ok := state["backfill_bound"]; !ok || state["backfill_done"] == 1 {
		return IndexProgress{Done: true, Percent: 100}
	}
	pct := 0
	if b := state["backfill_bound"]; b > 0 {
		pct = int(state["backfill_cursor"] * 100 / b)
	}
	return IndexProgress{Percent: min(pct, 99)}
}

// BackfillOptions steuert RunIndexBackfill.
type BackfillOptions struct {
	// Batch: Chunks je Schritt (Standard 300).
	Batch int
	// Pause zwischen zwei Schritten, damit Schreiber und Leser Luft haben
	// (Standard 25 ms).
	Pause time.Duration
}

// RunIndexBackfill arbeitet den Bestand in Schritten ab, bis alles indiziert
// ist oder ctx endet. Ein Abbruch verliert nichts: der nächste Lauf macht beim
// gespeicherten Stand weiter.
func (s *Store) RunIndexBackfill(ctx context.Context, opts BackfillOptions) error {
	if opts.Batch <= 0 {
		opts.Batch = 300
	}
	if opts.Pause <= 0 {
		opts.Pause = 25 * time.Millisecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := s.IndexBackfillStep(opts.Batch)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.Pause):
		}
	}
}

// maxStepBytes begrenzt den Rohtext, den ein Schritt im Speicher hält.
const maxStepBytes = 8 << 20

// IndexBackfillStep indiziert den nächsten Block des Bestands und sagt, ob der
// Lauf damit fertig ist.
func (s *Store) IndexBackfillStep(batch int) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{batch}, func(d *Store, p []any) (bool, error) { return d.IndexBackfillStep(p[0].(int)) })
	}
	if batch <= 0 {
		batch = 300
	}
	var cursor, bound, done int64
	state := map[string]*int64{"backfill_cursor": &cursor, "backfill_bound": &bound, "backfill_done": &done}
	for k, dst := range state {
		if err := s.db.QueryRow(`SELECT COALESCE((SELECT val FROM index_state WHERE key=?),0)`, k).Scan(dst); err != nil {
			return false, err
		}
	}
	if done == 1 || cursor >= bound {
		_, err := s.db.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES('backfill_done',1)`)
		return true, err
	}
	rows, err := s.db.Query(`SELECT c.id, c.session_id, c.seq, se.harness, c.raw FROM session_chunks c
		JOIN sessions se ON se.id = c.session_id
		WHERE c.id > ? AND c.id <= ? ORDER BY c.id LIMIT ?`, cursor, bound, batch)
	if err != nil {
		return false, err
	}
	var chunks []chunkRow
	size, last := 0, cursor
	for rows.Next() {
		var r chunkRow
		if err := rows.Scan(&r.id, &r.sessionID, &r.seq, &r.harness, &r.raw); err != nil {
			rows.Close()
			return false, err
		}
		chunks = append(chunks, r)
		last = r.id
		if size += len(r.raw); size > maxStepBytes {
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := indexChunks(tx, chunks); err != nil {
		return false, err
	}
	finished := len(chunks) < batch && size <= maxStepBytes
	if finished {
		last = bound
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES('backfill_cursor',?)`, last); err != nil {
		return false, err
	}
	if finished {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES('backfill_done',1)`); err != nil {
			return false, err
		}
	}
	return finished, tx.Commit()
}

// IndexSession holt die noch nicht indizierten Chunks einer Session nach. Die
// Sessionansicht ruft es, solange der Hintergrundlauf nicht durch ist, damit
// Gliederung und Titel nicht auf ihn warten.
func (s *Store) IndexSession(id int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id}, func(d *Store, p []any) error { return d.IndexSession(p[0].(int64)) })
	}
	for {
		rows, err := s.db.Query(`SELECT c.id, c.session_id, c.seq, se.harness, c.raw FROM session_chunks c
			JOIN sessions se ON se.id = c.session_id
			LEFT JOIN chunk_index x ON x.chunk_id = c.id
			WHERE c.session_id = ? AND x.chunk_id IS NULL ORDER BY c.id LIMIT 300`, id)
		if err != nil {
			return err
		}
		var chunks []chunkRow
		for rows.Next() {
			var r chunkRow
			if err := rows.Scan(&r.id, &r.sessionID, &r.seq, &r.harness, &r.raw); err != nil {
				rows.Close()
				return err
			}
			chunks = append(chunks, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if err := indexChunks(tx, chunks); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
}

// setVisibility schreibt die Stufe und einen Audit-Eintrag; shared bleibt als
// Spiegel erhalten, damit ein zurückgerolltes Binary die Freigabe weiter versteht.
func (s *Store) setVisibility(id int64, level, actor string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, level, actor}, func(d *Store, p []any) error {
			return d.setVisibility(p[0].(int64), p[1].(string), p[2].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old string
	var shared int
	if err := tx.QueryRow(`SELECT visibility, shared FROM sessions WHERE id=?`, id).Scan(&old, &shared); err != nil {
		return err
	}
	if shared != 0 && old == VisPrivate {
		old = VisProject
	}
	mirror := 1
	if level == VisPrivate {
		mirror = 0
	}
	if _, err := tx.Exec(`UPDATE sessions SET visibility=?, shared=? WHERE id=?`, level, mirror, id); err != nil {
		return err
	}
	if old != level {
		if _, err := tx.Exec(`INSERT INTO session_share_events(session_id, old_level, new_level, actor, created_at) VALUES(?,?,?,?,?)`,
			id, old, level, actor, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var errBadVisibility = errors.New("unknown visibility level")

// SetSessionVisibility stellt die Freigabestufe ein. Das darf nur der Besitzer
// der Session und der Owner ihres Projekts; die Prüfung gilt auch im Log-Modus,
// denn sie ist keine Lesesperre, sondern eine Änderung.
func (s *Store) SetSessionVisibility(id int64, pa *ProjectAccess, level string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, pa.Principal(), level}, func(d *Store, p []any) error {
			return d.SetSessionVisibility(p[0].(int64), d.Access(p[1].(Principal)), p[2].(string))
		})
	}
	switch level {
	case VisPrivate, VisProject, VisGuests:
	default:
		return errBadVisibility
	}
	sess, err := s.SessionByID(id)
	if err != nil {
		return ErrAccessNotFound
	}
	d := pa.Decide(sess.Scope.Project, ResTranscript, ActShare, pa.transcriptObject(sess))
	if !d.Allowed {
		if d.Hidden {
			return ErrAccessNotFound
		}
		return ErrAccessForbidden
	}
	return s.setVisibility(id, level, pa.Principal().ID)
}

// SessionByPublicID liest eine Session über ihre Adresse.
func (s *Store) SessionByPublicID(pid string) (Session, error) {
	if s.reader != nil {
		return s.reader.SessionByPublicID(pid)
	}
	if pid == "" {
		return Session{}, sql.ErrNoRows
	}
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE public_id = ?`, pid)
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

// cleanSnippet faltet Whitespace in einem Ausschnitt zu einem Leerzeichen
// zusammen; Leerzeichen an den Rändern bleiben, damit markierte und
// unmarkierte Teile zusammen wieder den Satz ergeben.
func cleanSnippet(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
