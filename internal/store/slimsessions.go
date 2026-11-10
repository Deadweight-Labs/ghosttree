package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// SlimSessionsOptions steuert SlimSessions.
type SlimSessionsOptions struct {
	// DryRun rechnet alles durch und schreibt nichts, auch kein Wörterbuch.
	DryRun bool
	// Batch ist die Zahl der Zeilen je Transaktion (Standard 200).
	Batch int
	// BatchBytes begrenzt die Rohdaten eines Schritts im Speicher (Standard
	// 64 MiB, entpackt gezählt); eine einzelne größere Zeile geht allein durch.
	BatchBytes int
	// Pause wartet nach jedem Schritt mindestens so lange, wie seine
	// Transaktion die Schreibsperre hielt, und nie kürzer als Pause (Standard
	// 20 ms). So bekommt der Schreiber des Servers mindestens die Hälfte der
	// Zeit.
	Pause time.Duration
	// AfterID setzt nach dieser Zeile fort (ein abgebrochener Lauf nennt sie).
	AfterID int64
	// NewDict lernt ein neues Wörterbuch, auch wenn schon eines da ist.
	NewDict bool
	// SkipOptimize lässt 'optimize' der Volltextindizes am Ende weg (es hält
	// die Schreibsperre am Stück; ctx db vacuum --fts-optimize holt es nach).
	SkipOptimize bool
	// Seed macht die Stichprobe des Wörterbuchs wiederholbar (0: Uhrzeit).
	Seed uint64
	// Progress wird nach jedem Schritt mit dem bisherigen Stand aufgerufen.
	Progress func(SlimSessionsReport)
}

// SlimSessionsReport ist das Ergebnis, auch das eines abgebrochenen Laufs.
// Bytes sind die gespeicherten Längen von raw und text.
type SlimSessionsReport struct {
	DryRun bool `json:"dry_run"`
	// Chunks ist die Zahl der Zeilen zu Beginn, Current die davon, die schon
	// nach der aktuellen Regel stehen und übersprungen werden.
	Chunks  int64 `json:"chunks"`
	Current int64 `json:"current"`
	// Scanned sind die bearbeiteten Zeilen.
	Scanned int64 `json:"scanned"`
	// Slimmed: raw hat sich inhaltlich geändert (Bilder, Schwärzung, Regel).
	Slimmed int64 `json:"slimmed"`
	// Packed: raw steht danach gepackt. Kurze Zeilen bleiben Klartext.
	Packed int64 `json:"packed"`
	// Deleted: Zeilen, die ganz wegfielen, samt Index und Volltext.
	Deleted int64 `json:"deleted"`
	// KeptCited: Zeilen, die wegfallen würden, aber ein Beleg (Wissen oder
	// Auftrag) zitiert; sie bleiben, nur gepackt.
	KeptCited int64 `json:"kept_cited"`
	// TextCut: text war länger als slim.CapOver und wurde wie raw gekürzt.
	TextCut int64 `json:"text_cut"`
	// TextKeptOpenBatch: text nicht gekürzt, weil die Session in einem offenen
	// Destillier-Auftrag steckt (dessen Zitate werden beim Einsammeln geprüft).
	TextKeptOpenBatch int64 `json:"text_kept_open_batch"`
	RawBefore         int64 `json:"raw_bytes_before"`
	RawAfter          int64 `json:"raw_bytes_after"`
	LineBefore        int64 `json:"line_bytes_before"`
	LineAfter         int64 `json:"line_bytes_after"`
	TextBefore        int64 `json:"text_bytes_before"`
	TextAfter         int64 `json:"text_bytes_after"`
	// Tables zählt, was in den abgeleiteten Tabellen geändert oder gelöscht
	// wurde (chunk_index, sess_fts, chunks_fts, session_embeddings,
	// sessions.msg_count).
	Tables map[string]int64 `json:"tables"`
	// Dict ist das Wörterbuch, mit dem gepackt wurde; DictTrained, ob der Lauf
	// es gelernt hat.
	Dict        RawDictInfo `json:"dict"`
	DictTrained bool        `json:"dict_trained"`
	// LastID ist die letzte festgeschriebene Zeile.
	LastID    int64         `json:"last_id"`
	Optimized []string      `json:"optimized,omitempty"`
	Duration  time.Duration `json:"duration_ns"`
	// Write ist die Zeit, in der der Lauf die Schreibsperre hielt.
	Write time.Duration `json:"write_ns"`
}

// SlimSessions bringt den Bestand von session_chunks auf den Stand neuer
// Zeilen (REQ-622): jede Zeile geht durch denselben Weg wie in AppendChunks
// (Bilder ersetzen, schwärzen, schlanken) und wird gepackt (rawcodec). Zeilen,
// die ganz wegfallen, verschwinden samt chunk_index, sess_fts, chunks_fts und
// ihren Vektoren. text wird wie raw gekürzt, außer in Sessions mit einem
// offenen Destillier-Auftrag.
//
// Fehlt ein Wörterbuch (oder mit NewDict), lernt der Lauf zuerst eines aus
// einer Stichprobe. Zeilen mit slim_version >= slim.Version überspringt er: ein
// zweiter Lauf findet nichts mehr, ein abgebrochener setzt mit AfterID fort
// oder fängt von vorn an und überspringt das Erledigte.
//
// Er geht in der Reihenfolge von id (Keyset), liest einen Schritt ohne Sperre,
// rechnet ihn in Go und schreibt ihn in einer kurzen Transaktion; jede Änderung
// steht unter "raw ist noch, wie gelesen", sodass ein Schreiber daneben
// (Schwärzung, Server) nichts verliert. Die Datenbank wird ohne die Migrationen
// von Open geöffnet: der Server muss in der neuen Version schon einmal
// gelaufen sein.
//
// Was nicht angefasst wird: text wird nicht noch einmal geschwärzt (das macht
// ctx redact-sessions in allen Kopien), Titel und Zitate bleiben. Vektoren
// gehen nur, wo das Ersetzen von Bildern oder die Schwärzung raw geändert hat;
// nach reinem Schlanken prüft der Vektorlauf selbst, ob sich das Dokument
// geändert hat (Hash über das Dokument), sonst bliebe nach #3404 jeder Vektor
// zum Nachrechnen.
func SlimSessions(ctx context.Context, path string, opts SlimSessionsOptions) (SlimSessionsReport, error) {
	start := time.Now()
	rep := SlimSessionsReport{DryRun: opts.DryRun, Tables: map[string]int64{}, LastID: opts.AfterID}
	if opts.Batch <= 0 {
		opts.Batch = defaultStripBatch
	}
	if opts.BatchBytes <= 0 {
		opts.BatchBytes = defaultStripBatchBytes
	}
	if opts.Pause <= 0 {
		opts.Pause = 20 * time.Millisecond
	}
	info, err := os.Stat(path)
	if err != nil {
		return rep, err
	}
	if !info.Mode().IsRegular() {
		return rep, fmt.Errorf("database path %q is not a regular file", path)
	}
	db, err := sql.Open("sqlite", maintenanceDSN(path))
	if err != nil {
		return rep, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	has := func(name string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name=?`, name).Scan(&n)
		return n > 0, err
	}
	for _, need := range []string{"session_chunks", "session_raw_dicts"} {
		if ok, err := has(need); err != nil {
			return rep, err
		} else if !ok {
			return rep, fmt.Errorf("no %s table: start the new ghosttree server once so it migrates the database, then run this again", need)
		}
	}
	if ok, err := hasColumn(db, "session_chunks", "slim_version"); err != nil {
		return rep, err
	} else if !ok {
		return rep, errors.New("session_chunks has no slim_version column: start the new ghosttree server once so it migrates the database")
	}
	w := slimWork{ctx: ctx, db: db, write: !opts.DryRun, raw: rawStateFor(db), harness: map[int64]string{}}
	for _, t := range []struct {
		names []string
		flag  *bool
	}{
		{[]string{"chunks_fts"}, &w.chunksFTS},
		{[]string{"chunk_index", "sess_fts"}, &w.index},
	} {
		*t.flag = true
		for _, n := range t.names {
			if ok, err := has(n); err != nil {
				return rep, err
			} else if !ok {
				*t.flag = false
			}
		}
	}
	if w.vectors, err = hasSessionEmbeddingTables(ctx, db); err != nil {
		return rep, err
	}
	if w.cited, err = citedChunks(ctx, db, has); err != nil {
		return rep, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_chunks`).Scan(&rep.Chunks); err != nil {
		return rep, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_chunks WHERE slim_version >= ?`, slim.Version).Scan(&rep.Current); err != nil {
		return rep, err
	}

	// Das Wörterbuch zuerst: jede Zeile des Laufs wird damit gepackt.
	fail := func(err error) (SlimSessionsReport, error) {
		rep.Duration = time.Since(start)
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	latest, err := latestRawDict(ctx, db)
	if err != nil {
		return fail(err)
	}
	seed := opts.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	if latest.Version == 0 || opts.NewDict {
		samples, total, err := sampleRawLines(ctx, db, w.raw, seed)
		if err != nil {
			return fail(err)
		}
		if dict := rawcodec.Train(samples, rawcodec.DictSize); len(dict) > 0 {
			if opts.DryRun {
				// Nicht ablegen; eine Version, die es nicht gibt, packt nur
				// zum Zählen.
				latest = rawcodec.Dict{Version: latest.Version + 1, Data: dict}
				rep.Dict = RawDictInfo{Version: latest.Version, Bytes: len(dict), SampleLines: len(samples), SampleBytes: total}
			} else {
				if rep.Dict, err = insertRawDict(ctx, db, dict, len(samples), total); err != nil {
					return fail(err)
				}
				w.raw.forget()
				latest = rawcodec.Dict{Version: rep.Dict.Version, Data: dict}
			}
			rep.DictTrained = true
		}
	}
	if !rep.DictTrained {
		if dicts, err := listRawDicts(ctx, db); err != nil {
			return fail(err)
		} else if len(dicts) > 0 {
			rep.Dict = dicts[0]
		}
	}
	if w.enc, err = rawcodec.NewEncoder(latest); err != nil {
		return fail(err)
	}
	if latest.Version > 0 {
		w.raw.dec.Add(latest.Version, latest.Data)
	}

	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		open, err := openBatchSessions(ctx, db, has)
		if err != nil {
			return fail(err)
		}
		// Das Lesen schließt, bevor geschrieben wird: es gibt nur eine
		// Verbindung, und eine offene Abfrage hielte sie fest.
		rows, err := db.QueryContext(ctx, `SELECT c.id, c.session_id, c.seq, c.text, c.raw FROM session_chunks c
			WHERE c.id > ? AND c.slim_version < ? ORDER BY c.id LIMIT ?`, rep.LastID, slim.Version, opts.Batch)
		if err != nil {
			return fail(err)
		}
		var edits []slimEdit
		held := 0
		for rows.Next() {
			var e slimEdit
			if err := rows.Scan(&e.id, &e.session, &e.seq, &e.oldText, &e.stored); err != nil {
				rows.Close()
				return fail(err)
			}
			if e.oldRaw, err = w.raw.line(ctx, db, e.stored); err != nil {
				rows.Close()
				return fail(fmt.Errorf("chunk %d: %w", e.id, err))
			}
			w.plan(&e, open[e.session])
			edits = append(edits, e)
			if held += len(e.oldRaw) + len(e.oldText); held >= opts.BatchBytes {
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail(err)
		}
		if len(edits) == 0 {
			break
		}
		began := time.Now()
		if err := w.apply(edits, &rep); err != nil {
			return fail(err)
		}
		took := time.Since(began)
		rep.Write += took
		rep.LastID = edits[len(edits)-1].id
		if opts.Progress != nil {
			rep.Duration = time.Since(start)
			opts.Progress(rep)
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(max(opts.Pause, took)):
		}
	}

	if !opts.DryRun && !opts.SkipOptimize && rep.Deleted+rep.Slimmed+rep.TextCut > 0 {
		for _, idx := range []struct {
			name string
			on   bool
		}{{"chunks_fts", w.chunksFTS}, {"sess_fts", w.index}} {
			if !idx.on {
				continue
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO `+idx.name+`(`+idx.name+`) VALUES('optimize')`); err != nil {
				return fail(err)
			}
			rep.Optimized = append(rep.Optimized, idx.name)
		}
	}
	if !opts.DryRun {
		checkpoint(ctx, db)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

type slimEdit struct {
	id, session int64
	seq         int
	oldText     string
	oldRaw      string
	stored      rawcodec.Value

	drop     bool // die Zeile fällt weg
	newRaw   string
	newStore any // newRaw gepackt
	newText  string
	// private: Bilder oder Geheimnisse wurden ersetzt; die Vektoren gehen.
	private bool
	// keptCited: fiele weg, bleibt aber, weil ein Beleg sie zitiert.
	keptCited bool
}

type slimWork struct {
	ctx       context.Context
	db        *sql.DB
	write     bool
	raw       *rawState
	enc       *rawcodec.Encoder
	chunksFTS bool
	index     bool
	vectors   sessionVectorSchema
	cited     map[[2]int64]bool
	harness   map[int64]string
}

// plan rechnet aus, was mit einer Zeile geschieht. Kein Zugriff auf die
// Datenbank.
func (w *slimWork) plan(e *slimEdit, openBatch bool) {
	stripped, _ := blobstrip.Strip(e.oldRaw)
	redacted := redact.RedactJSON(stripped)
	line, keep := slim.Line(redacted)
	e.private = redacted != e.oldRaw
	if !keep {
		if !w.cited[[2]int64{e.session, int64(e.seq)}] {
			e.drop = true
			return
		}
		// Ein Beleg zitiert die Zeile: sie bleibt, wie sie ist.
		line, e.keptCited = redacted, true
	}
	e.newRaw = line
	e.newStore = w.enc.Encode(line)
	e.newText = e.oldText
	if !openBatch {
		e.newText, _ = slim.Cap(e.oldText)
	}
}

// citedChunks sind die Zeilen (Session, Nummer), die ein Beleg zitiert.
func citedChunks(ctx context.Context, db *sql.DB, has func(string) (bool, error)) (map[[2]int64]bool, error) {
	out := map[[2]int64]bool{}
	for _, t := range []string{"knowledge_evidence", "request_sightings"} {
		if ok, err := has(t); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT session_id, chunk_seq FROM `+t)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k [2]int64
			if err := rows.Scan(&k[0], &k[1]); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// openBatchSessions sind die Sessions in einem offenen Destillier-Auftrag. Ihr
// Prompt ist gebaut, die Antwort kommt später und wird gegen text geprüft.
func openBatchSessions(ctx context.Context, db *sql.DB, has func(string) (bool, error)) (map[int64]bool, error) {
	out := map[int64]bool{}
	if ok, err := has("distill_batch_items"); err != nil || !ok {
		return out, err
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT i.session_id FROM distill_batch_items i
		JOIN distill_batches b ON b.id = i.batch_id WHERE b.state = 'open'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (w *slimWork) exec(q maintQueryer, query string, args ...any) (int64, error) {
	if !w.write {
		return 1, nil
	}
	res, err := q.ExecContext(w.ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// apply schreibt einen Schritt in einer Transaktion (beim Probelauf ohne).
// Jede Änderung steht unter "raw ist noch, wie gelesen": hat ein anderer
// Schreiber die Zeile inzwischen geändert, bleibt sie für einen späteren Lauf.
func (w *slimWork) apply(edits []slimEdit, rep *SlimSessionsReport) error {
	var q maintQueryer = w.db
	if w.write {
		tx, err := w.db.BeginTx(w.ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		q = tx
		if err := w.applyIn(q, edits, rep); err != nil {
			return err
		}
		return tx.Commit()
	}
	return w.applyIn(q, edits, rep)
}

func (w *slimWork) applyIn(q maintQueryer, edits []slimEdit, rep *SlimSessionsReport) error {
	for i := range edits {
		e := &edits[i]
		if e.drop {
			if err := w.remove(q, e, rep); err != nil {
				return err
			}
			continue
		}
		n, err := w.exec(q, `UPDATE session_chunks SET raw=?, text=?, slim_version=? WHERE id=? AND raw=?`,
			e.newStore, e.newText, slim.Version, e.id, e.stored.Stored())
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		rep.Scanned++
		rep.RawBefore += int64(e.stored.StoredLen())
		rep.RawAfter += int64(storedLen(e.newStore))
		rep.LineBefore += int64(len(e.oldRaw))
		rep.LineAfter += int64(len(e.newRaw))
		rep.TextBefore += int64(len(e.oldText))
		rep.TextAfter += int64(len(e.newText))
		if _, ok := e.newStore.([]byte); ok {
			rep.Packed++
		}
		if e.newRaw != e.oldRaw {
			rep.Slimmed++
		}
		if e.keptCited {
			rep.KeptCited++
		}
		if e.newText != e.oldText {
			rep.TextCut++
			if w.chunksFTS {
				if _, err := w.exec(q, `INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', ?, ?)`, e.id, e.oldText); err != nil {
					return err
				}
				if _, err := w.exec(q, `INSERT INTO chunks_fts(rowid, text) VALUES(?, ?)`, e.id, e.newText); err != nil {
					return err
				}
				rep.Tables["chunks_fts"]++
			}
		} else if len(e.oldText) > slim.CapOver {
			rep.TextKeptOpenBatch++
		}
		if e.newRaw != e.oldRaw {
			if err := w.reindex(q, e, rep); err != nil {
				return err
			}
			if e.private && w.vectors != noSessionVectors {
				n, err := dropSessionEmbeddings(w.ctx, q, w.vectors, e.id, w.write)
				if err != nil {
					return err
				}
				rep.Tables["session_embeddings"] += n
			}
		}
	}
	return nil
}

// remove löscht eine weggefallene Zeile mit allem, was aus ihr abgeleitet ist.
// Der Volltext mit externem Inhalt wird mit genau den Werten ausgetragen, mit
// denen er eingetragen wurde (#3273); die Vektoren und der Merker des
// Vektorlaufs gehen, solange die Zeile noch da ist (er sucht ihre Session über
// sie).
func (w *slimWork) remove(q maintQueryer, e *slimEdit, rep *SlimSessionsReport) error {
	var still int
	err := q.QueryRowContext(w.ctx, `SELECT count(*) FROM session_chunks WHERE id=? AND raw=?`, e.id, e.stored.Stored()).Scan(&still)
	if err != nil {
		return err
	}
	if still == 0 {
		return nil
	}
	if w.vectors != noSessionVectors {
		n, err := dropSessionEmbeddings(w.ctx, q, w.vectors, e.id, w.write)
		if err != nil {
			return err
		}
		rep.Tables["session_embeddings"] += n
	}
	if w.index {
		var old indexFields
		var msgs int
		err := q.QueryRowContext(w.ctx, `SELECT text, thinking, tool_input, tool_output FROM chunk_index WHERE chunk_id=?`, e.id).
			Scan(&old.text, &old.thinking, &old.toolInput, &old.toolOutput)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if !old.empty() {
				if _, err := w.exec(q, `INSERT INTO sess_fts(sess_fts, rowid, text, thinking, tool_input, tool_output) VALUES('delete', ?, ?, ?, ?, ?)`,
					e.id, old.text, old.thinking, old.toolInput, old.toolOutput); err != nil {
					return err
				}
				rep.Tables["sess_fts"]++
			}
			if _, err := w.exec(q, `DELETE FROM chunk_index WHERE chunk_id=?`, e.id); err != nil {
				return err
			}
			rep.Tables["chunk_index"]++
			h, err := w.harnessOf(q, e.session)
			if err != nil {
				return err
			}
			msgs = transcript.Fields(transcript.Parse(h, e.seq, e.oldRaw)).Messages
			if msgs != 0 {
				if _, err := w.exec(q, `UPDATE sessions SET msg_count = max(msg_count - ?, 0) WHERE id = ?`, msgs, e.session); err != nil {
					return err
				}
				rep.Tables["sessions.msg_count"]++
			}
		}
	}
	if w.chunksFTS {
		if _, err := w.exec(q, `INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', ?, ?)`, e.id, e.oldText); err != nil {
			return err
		}
		rep.Tables["chunks_fts"]++
	}
	if _, err := w.exec(q, `DELETE FROM session_chunks WHERE id=?`, e.id); err != nil {
		return err
	}
	rep.Scanned++
	rep.Deleted++
	rep.RawBefore += int64(e.stored.StoredLen())
	rep.LineBefore += int64(len(e.oldRaw))
	rep.TextBefore += int64(len(e.oldText))
	return nil
}

// reindex leitet die Zeile von chunk_index und den Eintrag in sess_fts aus der
// neuen raw neu ab, wie redactWork.reindex, und gleicht msg_count der Session
// an, falls sich die Zahl der Nachrichten der Zeile ändert.
func (w *slimWork) reindex(q maintQueryer, e *slimEdit, rep *SlimSessionsReport) error {
	if !w.index {
		return nil
	}
	var old indexFields
	err := q.QueryRowContext(w.ctx, `SELECT text, thinking, tool_input, tool_output FROM chunk_index WHERE chunk_id=?`, e.id).
		Scan(&old.text, &old.thinking, &old.toolInput, &old.toolOutput)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // noch nicht indiziert: der Nachindizierlauf liest die neue raw
	}
	if err != nil {
		return err
	}
	h, err := w.harnessOf(q, e.session)
	if err != nil {
		return err
	}
	of := transcript.Fields(transcript.Parse(h, e.seq, e.oldRaw))
	f := transcript.Fields(transcript.Parse(h, e.seq, e.newRaw))
	nf := indexFields{f.Text, f.Thinking, f.ToolInput, f.ToolOutput}
	if d := f.Messages - of.Messages; d != 0 {
		if _, err := w.exec(q, `UPDATE sessions SET msg_count = max(msg_count + ?, 0) WHERE id = ?`, d, e.session); err != nil {
			return err
		}
		rep.Tables["sessions.msg_count"]++
	}
	if nf == old && f.Role == of.Role && f.Time == of.Time && f.Prompt == of.Prompt && f.ToolCalls == of.ToolCalls {
		return nil
	}
	if !old.empty() {
		if _, err := w.exec(q, `INSERT INTO sess_fts(sess_fts, rowid, text, thinking, tool_input, tool_output) VALUES('delete', ?, ?, ?, ?, ?)`,
			e.id, old.text, old.thinking, old.toolInput, old.toolOutput); err != nil {
			return err
		}
	}
	prompt := 0
	if f.Prompt {
		prompt = 1
	}
	if _, err := w.exec(q, `UPDATE chunk_index SET role=?, ts=?, is_prompt=?, tool_calls=?, text=?, thinking=?, tool_input=?, tool_output=? WHERE chunk_id=?`,
		f.Role, f.Time, prompt, f.ToolCalls, nf.text, nf.thinking, nf.toolInput, nf.toolOutput, e.id); err != nil {
		return err
	}
	if !nf.empty() {
		if _, err := w.exec(q, `INSERT INTO sess_fts(rowid, text, thinking, tool_input, tool_output) VALUES(?, ?, ?, ?, ?)`,
			e.id, nf.text, nf.thinking, nf.toolInput, nf.toolOutput); err != nil {
			return err
		}
	}
	rep.Tables["chunk_index"]++
	if !old.empty() || !nf.empty() {
		rep.Tables["sess_fts"]++
	}
	return nil
}

func (w *slimWork) harnessOf(q maintQueryer, session int64) (string, error) {
	if h, ok := w.harness[session]; ok {
		return h, nil
	}
	var h string
	if err := q.QueryRowContext(w.ctx, `SELECT harness FROM sessions WHERE id=?`, session).Scan(&h); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	w.harness[session] = h
	return h, nil
}
