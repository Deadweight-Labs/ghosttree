package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
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
	if err := s.db.QueryRow(`SELECT count(*) FROM index_state WHERE key='backfill_bound'`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return s.raiseBackfillBound()
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

// raiseBackfillBound holt Chunks hinter der alten Grenze ein: ein zurückgerolltes
// Binary schreibt ohne Index, und nach dem nächsten Update liegen sie jenseits
// von backfill_bound. Gibt es dort Chunks ohne Indexzeile, wandert die Grenze
// auf die größte Nummer und der Lauf beginnt wieder; der Stand davor bleibt.
func (s *Store) raiseBackfillBound() error {
	var bound int64
	if err := s.db.QueryRow(`SELECT COALESCE((SELECT val FROM index_state WHERE key='backfill_bound'),0)`).Scan(&bound); err != nil {
		return err
	}
	var missing int
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM session_chunks c WHERE c.id > ? AND NOT EXISTS (SELECT 1 FROM chunk_index x WHERE x.chunk_id = c.id))`, bound).Scan(&missing); err != nil || missing == 0 {
		return err
	}
	var max int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM session_chunks`).Scan(&max); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES('backfill_bound',?),('backfill_done',0)`, max); err != nil {
		return err
	}
	return tx.Commit()
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
	return runIndexBackfill(ctx, opts, s.IndexBackfillStep)
}

// transientBackfillError: ein voller Schreiber oder eine gesperrte Datenbank
// vergeht von selbst; der Lauf wartet und macht weiter.
func transientBackfillError(err error) bool {
	if errors.Is(err, ErrWriterOperationsFull) || errors.Is(err, ErrWriterBytesFull) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// backfillRetryMax ist die längste Wartezeit nach einem vorübergehenden Fehler.
var backfillRetryMax = 5 * time.Second

func runIndexBackfill(ctx context.Context, opts BackfillOptions, step func(int) (bool, error)) error {
	if opts.Batch <= 0 {
		opts.Batch = 300
	}
	if opts.Pause <= 0 {
		opts.Pause = 25 * time.Millisecond
	}
	backoff := opts.Pause
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		began := time.Now()
		done, err := step(opts.Batch)
		if err != nil {
			if !transientBackfillError(err) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, backfillRetryMax)
			continue
		}
		backoff = opts.Pause
		if done {
			return nil
		}
		// Die Pause wächst mit der Arbeitszeit des Schritts: der Schreiber ist
		// höchstens etwa ein Drittel der Zeit belegt.
		pause := max(opts.Pause, 2*time.Since(began))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// maxStepBytes begrenzt den Rohtext, den ein Schritt im Speicher hält. Eine
// Variable, damit ein Test sie senken kann.
var maxStepBytes = 8 << 20

// backfillStepBudget ist die Zeit, die ein Schritt den Schreiber höchstens
// belegt; danach endet er nach der laufenden Gruppe und der Rest folgt im
// nächsten Schritt. backfillGroup Chunks gelten als unteilbar.
var backfillStepBudget = 50 * time.Millisecond

const backfillGroup = 10

// chunksRead meldet in Tests, wie viele Chunks ein Schritt gelesen hat.
var chunksRead func(n int)

// indexGroups liest und indiziert Chunks gruppenweise in tx, höchstens batch
// Stück, solange der Schritt (seit began) noch Zeit und Speicher hat. Es liest
// nie mehr, als es verarbeitet: die nächste Gruppe wird erst geholt, wenn die
// vorige indiziert und das Budget noch nicht verbraucht ist. next liefert die
// Abfrage für die nächsten n Chunks hinter after. exhausted heißt: die Abfrage
// hatte weniger Zeilen als gefragt, es gibt nichts mehr.
func indexGroups(tx *sql.Tx, began time.Time, batch int, after int64, next func(after int64, n int) (string, []any)) (last int64, processed int, exhausted bool, err error) {
	last = after
	size := 0
	for processed < batch {
		n := min(backfillGroup, batch-processed)
		inner, args := next(last, n)
		// Die Gruppe endet vor der Zeile, die das Byte-Budget des Schritts
		// sprengen würde (die erste Zeile kommt immer mit); cnt ist die Zahl
		// der Zeilen vor der Kürzung.
		query := `SELECT id, session_id, seq, harness, raw, cnt FROM (
			SELECT *, SUM(length(raw)) OVER (ORDER BY id) AS cum, COUNT(*) OVER () AS cnt FROM (` + inner + `))
			WHERE cum - length(raw) < ? ORDER BY id`
		args = append(args, max(maxStepBytes-size, 1))
		rows, err := tx.Query(query, args...)
		if err != nil {
			return last, processed, false, err
		}
		var group []chunkRow
		cnt := 0
		for rows.Next() {
			var r chunkRow
			if err := rows.Scan(&r.id, &r.sessionID, &r.seq, &r.harness, &r.raw, &cnt); err != nil {
				rows.Close()
				return last, processed, false, err
			}
			group = append(group, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return last, processed, false, err
		}
		if chunksRead != nil {
			chunksRead(len(group))
		}
		if len(group) > 0 {
			if err := indexChunks(tx, group); err != nil {
				return last, processed, false, err
			}
			processed += len(group)
			last = group[len(group)-1].id
			for _, r := range group {
				size += len(r.raw)
			}
		}
		if cnt < n && len(group) == cnt {
			return last, processed, true, nil
		}
		if len(group) < cnt || time.Since(began) >= backfillStepBudget || size >= maxStepBytes {
			break
		}
	}
	return last, processed, false, nil
}

// IndexBackfillStep indiziert den nächsten Block des Bestands und sagt, ob der
// Lauf damit fertig ist.
func (s *Store) IndexBackfillStep(batch int) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{batch}, func(d *Store, p []any) (bool, error) { return d.IndexBackfillStep(p[0].(int)) })
	}
	began := time.Now()
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
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	last, _, exhausted, err := indexGroups(tx, began, batch, cursor, func(after int64, n int) (string, []any) {
		return `SELECT c.id, c.session_id, c.seq, se.harness, c.raw FROM session_chunks c
			JOIN sessions se ON se.id = c.session_id
			WHERE c.id > ? AND c.id <= ? ORDER BY c.id LIMIT ?`, []any{after, bound, n}
	})
	if err != nil {
		return false, err
	}
	finished := exhausted
	if finished {
		// Bis zur Grenze ist alles indiziert. Chunks dahinter, die ein
		// zurückgerolltes Binary ohne Index schrieb, verschieben die Grenze
		// ans Ende des Bestands und der Lauf geht weiter; sonst steht die
		// Grenze am Ende fest auf der größten Nummer.
		var max int64
		var behind int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM session_chunks`).Scan(&max); err != nil {
			return false, err
		}
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM session_chunks c WHERE c.id > ? AND NOT EXISTS (SELECT 1 FROM chunk_index x WHERE x.chunk_id = c.id))`, bound).Scan(&behind); err != nil {
			return false, err
		}
		if max > bound {
			if behind != 0 {
				finished, last = false, bound
			} else {
				last = max
			}
			bound = max
		} else {
			last = bound
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES('backfill_bound',?)`, bound); err != nil {
			return false, err
		}
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

// onDemandIndexBudget ist die Zeit, die eine Seitenanfrage insgesamt aufs
// Nachindizieren der Session wartet. Danach wird die Seite mit dem gezeigt, was
// schon indiziert ist; der Hintergrundlauf holt den Rest.
var onDemandIndexBudget = 2 * time.Second

// indexSessionBatch ist ein Schreiberauftrag: ein Schritt mit denselben Grenzen
// wie der Hintergrundlauf (Zeit, Rohtext), höchstens indexBatch Chunks der
// Session. Es sagt, wie viele es waren und ob noch welche fehlen.
const indexBatch = 300

func (s *Store) indexSessionBatch(id int64) (int, bool, error) {
	if s.writer != nil {
		type result struct {
			n    int
			more bool
		}
		r, err := queueValue(s, []any{id}, func(d *Store, p []any) (result, error) {
			n, more, err := d.indexSessionBatch(p[0].(int64))
			return result{n, more}, err
		})
		return r.n, r.more, err
	}
	began := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	_, n, exhausted, err := indexGroups(tx, began, indexBatch, 0, func(after int64, k int) (string, []any) {
		return `SELECT c.id, c.session_id, c.seq, se.harness, c.raw FROM session_chunks c
			JOIN sessions se ON se.id = c.session_id
			LEFT JOIN chunk_index x ON x.chunk_id = c.id
			WHERE c.session_id = ? AND x.chunk_id IS NULL AND c.id > ? ORDER BY c.id LIMIT ?`, []any{id, after, k}
	})
	if err != nil {
		return 0, false, err
	}
	return n, !exhausted, tx.Commit()
}

// IndexSession holt die noch nicht indizierten Chunks einer Session nach. Die
// Sessionansicht ruft es, solange der Hintergrundlauf nicht durch ist, damit
// Gliederung und Titel nicht auf ihn warten.
func (s *Store) IndexSession(id int64) error { return s.IndexSessionContext(context.Background(), id) }

// IndexSessionContext arbeitet die Session in Schritten ab; zwischen den
// Schritten kommen andere Schreiber dran, und ein beendeter ctx (Anfrage
// abgebrochen) hält die Schleife an. Nach onDemandIndexBudget endet sie ohne
// Fehler: die Seite zeigt, was indiziert ist.
func (s *Store) IndexSessionContext(ctx context.Context, id int64) error {
	deadline := time.Now().Add(onDemandIndexBudget)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, more, err := s.indexSessionBatch(id)
		if err != nil || !more || time.Now().After(deadline) {
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

// ensureCanonicalSessionProjects schreibt sessions.project einmalig in die
// kanonische Form (wie scope.CanonicalAxes). Die SQL-Vorauswahl der lesbaren
// Menge vergleicht den Rohwert, die Zeilenprüfung den normalisierten; nur wenn
// beide denselben Text sehen, geben sie dieselbe Menge.
func ensureCanonicalSessionProjects(db *sql.DB) error {
	const key = "sessions_project_canonical"
	var done int
	if err := db.QueryRow(`SELECT COUNT(*) FROM index_state WHERE key=?`, key).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	rows, err := db.Query(`SELECT DISTINCT project FROM sessions WHERE project != ''`)
	if err != nil {
		return err
	}
	var raw []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		raw = append(raw, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range raw {
		if canon := scope.NormalizeRemote(p); canon != p {
			if _, err := tx.Exec(`UPDATE OR IGNORE sessions SET project=? WHERE project=?`, canon, p); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO index_state(key,val) VALUES(?,1)`, key); err != nil {
		return err
	}
	return tx.Commit()
}
