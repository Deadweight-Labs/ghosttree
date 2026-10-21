package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/transcript"
)

// RedactSessionsOptions steuert RedactSessions.
type RedactSessionsOptions struct {
	// DryRun zählt nur, was geschwärzt würde, und schreibt nichts.
	DryRun bool
	// Batch ist die Zahl der Zeilen je Transaktion (Standard 200).
	Batch int
	// BatchBytes begrenzt die Daten, die ein Schritt im Speicher hält
	// (Standard 64 MiB); eine einzelne größere Zeile geht allein durch.
	BatchBytes int
	// Sleep wartet nach jedem Schritt, damit der laufende Server Luft behält.
	Sleep time.Duration
	// AfterID setzt den Lauf hinter einer früher gemeldeten LastID fort.
	AfterID int64
	// SkipOptimize lässt das Zusammenführen der Volltextindizes am Ende aus.
	// Solange es fehlt, stehen die entfernten Wörter noch in alten Segmenten.
	SkipOptimize bool
	// Literals nennt bekannte Werte, die überall, auch ohne Beschriftung, durch
	// [REDACTED:literal] ersetzt werden: in text und raw (dort auch in der
	// JSON-escapten Schreibweise), in den davon abgeleiteten Indizes, in den
	// Titeln und in den Zitaten der Belege. nil lässt alles, wie es war.
	Literals *redact.Literals
	// Progress wird nach jedem Schritt mit dem bisherigen Stand aufgerufen.
	Progress func(RedactSessionsReport)
}

// RedactHolders zählt Einträge, die einen bekannten Wert enthalten, ohne ihn zu
// ändern.
type RedactHolders struct {
	// Rows sind die Zeilen der Tabelle, Owners die Nummern der Einträge, zu denen
	// sie gehören (bei knowledge_versions der Wissenseintrag, nicht die Fassung).
	Rows   int64
	Owners []int64
}

// RedactSessionsReport ist das Ergebnis, auch das eines abgebrochenen Laufs.
// Es enthält nie einen Wert, nur Zahlen und Namen.
type RedactSessionsReport struct {
	// Chunks ist die Zahl der Zeilen in session_chunks zu Beginn.
	Chunks int64
	// Scanned sind die geprüften Zeilen, Changed die mit mindestens einer
	// Ersetzung in text oder raw.
	Scanned, Changed int64
	// LastID ist die letzte geprüfte Zeile.
	LastID int64
	// Tables zählt je Tabelle und Spalte, was bereinigt wurde (bei DryRun: würde).
	Tables map[string]int64
	// Kinds zählt Ersetzungen je Feld und Art, etwa "raw/labeled:password".
	Kinds map[string]int64
	// CitedKnowledge und CitedRequests sind Wissenseinträge und Aufträge, deren
	// Beleg aus einem geänderten Chunk stammt. Ihr Text wird nicht angefasst;
	// sie sind zu prüfen.
	CitedKnowledge, CitedRequests []int64
	// Optimized nennt die Volltextindizes, die zusammengeführt wurden.
	Optimized []string
	// Checkpoint ist das Ergebnis des WAL-Checkpoints am Ende ("" wenn keiner lief).
	Checkpoint string
	// Skipped nennt Tabellen, die in dieser Datenbank fehlen.
	Skipped []string
	// Holders zählt, je Tabelle, Texte von Menschen und Agenten (Wissen, Fassungen
	// des Wissens, Aufträge), die einen bekannten Wert enthalten. Nur gezählt:
	// sie haben Verlauf, Suchkopien und Schnappschüsse (siehe RedactSessions).
	// Nur mit Literals gefüllt, auch im Probelauf.
	Holders map[string]RedactHolders
}

const redactQuoteMarker = redact.QuoteMarker

// RedactSessions schwärzt Geheimnisse in den gespeicherten Sitzungen, wie
// AppendChunks es bei neuen Chunks tut, und entfernt sie aus jeder Kopie:
//
//   - session_chunks.text und .raw
//   - chunks_fts: Volltextindex über text (content='session_chunks'). Das UPDATE
//     fasst ihn nicht an; der alte Eintrag wird mit dem Befehl 'delete' und dem
//     alten Text gelöscht, der neue eingefügt.
//   - chunk_index (aus raw abgeleitete Felder) und sess_fts darüber, auf dieselbe
//     Weise
//   - sessions.title, soweit er aus dem Prompt stammt
//   - knowledge_evidence.quote und request_sightings.quote: wörtliche Zitate aus
//     text
//
// Mit opts.Literals kommt vor der Beschriftungsregel hinzu, dass bekannte Werte
// überall in diesen Kopien ersetzt werden. Der Lauf liest dafür ohnehin jede
// Zeile von session_chunks; der Abgleich geschieht in Go auf dem, was schon
// gelesen ist, und nicht mit instr oder MATCH in SQL: raw trägt Inhalt, der in
// text fehlt (Denkblöcke, Werkzeugergebnisse), und der Wert steht dort auch
// JSON-escapt, was instr nicht findet. Die Werte gelangen nie in eine
// SQL-Anweisung. Zitate werden zusätzlich tabellenweit geprüft, auch wenn ihr
// Chunk nicht mehr da ist.
//
// Wissen, Fassungen des Wissens und Aufträge werden bei Literals nur gezählt
// (Holders), nicht geändert. Ein sauberes Ersetzen gibt es dort nicht: der
// Wert steht auch in knowledge_versions, in search_documents und beiden
// Volltextindizes, in Titeln und Beschreibungen von Kriterien und Aktivitäten,
// in Erklärungen, Koordinationsnachrichten und Dokumenten, und in den
// unveränderlichen Schnappschüssen, die keinen Löschweg haben. Einen Teil davon
// zu ändern, ließe den Rest als falsche Sicherheit stehen; den Wert aus dem
// Text zu nehmen ist dort eine Entscheidung des Menschen, nicht des Befehls.
//
// Danach werden beide Volltextindizes mit 'optimize' zusammengeführt. Das
// 'delete' eines externen Inhalts schreibt die Wörter noch einmal als
// Löschmarke in ein neues Segment; erst das Zusammenführen aller Segmente lässt
// sie verschwinden. Das ist eine einzige Anweisung und hält die
// Schreibsperre, solange sie läuft.
//
// Die Datenbank wird ohne die Migrationen von Open geöffnet, mit
// secure_delete: was der Lauf freigibt, wird überschrieben, nicht nur
// ausgetragen. Was schon vorher auf der Freiliste lag, bleibt bis zu einem
// VACUUM stehen.
func RedactSessions(ctx context.Context, path string, opts RedactSessionsOptions) (RedactSessionsReport, error) {
	rep := RedactSessionsReport{Tables: map[string]int64{}, Kinds: map[string]int64{}, LastID: opts.AfterID}
	if opts.Batch <= 0 {
		opts.Batch = defaultStripBatch
	}
	if opts.BatchBytes <= 0 {
		opts.BatchBytes = defaultStripBatchBytes
	}
	info, err := os.Stat(path)
	if err != nil {
		return rep, err
	}
	if !info.Mode().IsRegular() {
		return rep, fmt.Errorf("database path %q is not a regular file", path)
	}
	var pragmas []string
	if !opts.DryRun {
		pragmas = append(pragmas, "secure_delete(1)")
	}
	db, err := sql.Open("sqlite", maintenanceDSN(path, pragmas...))
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
	if ok, err := has("session_chunks"); err != nil {
		return rep, err
	} else if !ok {
		return rep, errors.New("no session_chunks table: not a ghosttree database")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_chunks`).Scan(&rep.Chunks); err != nil {
		return rep, err
	}
	w := redactWork{db: db, ctx: ctx, write: !opts.DryRun, lits: opts.Literals, has: has,
		harness: map[int64]string{}, retitled: map[int64]struct{}{}, requoted: map[string]struct{}{}}
	if w.sessionVectors, err = hasSessionEmbeddingTables(ctx, db); err != nil {
		return rep, err
	}
	for _, t := range []struct {
		names []string
		flag  *bool
	}{
		{[]string{"chunks_fts"}, &w.chunksFTS},
		{[]string{"chunk_index", "sess_fts"}, &w.index},
		{[]string{"knowledge_evidence"}, &w.evidence},
		{[]string{"request_sightings"}, &w.sightings},
	} {
		*t.flag = true
		for _, n := range t.names {
			ok, err := has(n)
			if err != nil {
				return rep, err
			}
			if !ok {
				*t.flag = false
				rep.Skipped = append(rep.Skipped, n)
			}
		}
	}

	fail := func(err error) (RedactSessionsReport, error) {
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	cited, citedRq := map[int64]struct{}{}, map[int64]struct{}{}
	stats := redact.Stats{}
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		// Das Lesen schließt, bevor geschrieben wird: es gibt nur eine Verbindung.
		var edits []redactEdit
		var read int
		var held int
		last := rep.LastID
		rows, err := db.QueryContext(ctx, `SELECT id, session_id, seq, text, raw FROM session_chunks WHERE id > ? ORDER BY id LIMIT ?`, rep.LastID, opts.Batch)
		if err != nil {
			return fail(err)
		}
		for rows.Next() {
			var e redactEdit
			if err := rows.Scan(&e.id, &e.session, &e.seq, &e.oldText, &e.oldRaw); err != nil {
				rows.Close()
				return fail(err)
			}
			read++
			last = e.id
			held += len(e.oldText) + len(e.oldRaw)
			clear(stats)
			e.newText = w.redactField(e.oldText, redact.Text, stats)
			e.textKinds = take(stats)
			e.newRaw = w.redactField(e.oldRaw, redact.JSON, stats)
			e.rawKinds = take(stats)
			if e.newText != e.oldText || e.newRaw != e.oldRaw {
				edits = append(edits, e)
			}
			if held >= opts.BatchBytes {
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail(err)
		}
		if read == 0 {
			break
		}
		res, err := w.apply(edits)
		if err != nil {
			return fail(err)
		}
		// Erst nach dem Schreiben: LastID und die Zähler beschreiben nur, was
		// auch festgeschrieben ist.
		rep.LastID = last
		rep.Scanned += int64(read)
		rep.Changed += res.changed
		for k, n := range res.tables {
			rep.Tables[k] += n
		}
		for k, n := range res.kinds {
			rep.Kinds[k] += n
		}
		for _, id := range res.cited {
			cited[id] = struct{}{}
		}
		for _, id := range res.citedRq {
			citedRq[id] = struct{}{}
		}
		if opts.Progress != nil {
			opts.Progress(rep)
		}
		if opts.Sleep > 0 {
			select {
			case <-ctx.Done():
				return rep, ctx.Err()
			case <-time.After(opts.Sleep):
			}
		}
	}
	rep.CitedKnowledge, rep.CitedRequests = sortedIDs(cited), sortedIDs(citedRq)

	titles, err := w.titles()
	if err != nil {
		return fail(err)
	}
	if titles > 0 {
		rep.Tables["sessions.title"] += titles
	}
	if w.lits != nil {
		quotes, err := w.literalQuotes()
		if err != nil {
			return fail(err)
		}
		for k, n := range quotes {
			rep.Tables[k] += n
		}
		if rep.Holders, err = w.holders(); err != nil {
			return fail(err)
		}
	}

	if opts.DryRun {
		return rep, nil
	}
	if !opts.SkipOptimize {
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
	rep.Checkpoint = checkpoint(ctx, db)
	return rep, nil
}

func sortedIDs(set map[int64]struct{}) []int64 {
	var ids []int64
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// take returns the counters and empties them.
func take(s redact.Stats) map[string]int64 {
	if len(s) == 0 {
		return nil
	}
	out := make(map[string]int64, len(s))
	for k, v := range s {
		out[k] = v
	}
	clear(s)
	return out
}

// checkpoint folds the write-ahead log back into the file and truncates it, so
// that the old page images do not outlive the run in the -wal file. It cannot
// finish while a reader (the server) holds an old snapshot; then the next
// checkpoint of the server does it.
func checkpoint(ctx context.Context, db *sql.DB) string {
	var busy, logFrames, done int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err != nil {
		return "not run: " + err.Error()
	}
	if busy != 0 {
		return "partial: a reader keeps part of the log; the server's next checkpoint finishes it"
	}
	return "truncated"
}

type redactEdit struct {
	id, session int64
	seq         int
	oldText     string
	oldRaw      string
	newText     string
	newRaw      string
	textKinds   map[string]int64
	rawKinds    map[string]int64
}

type redactBatch struct {
	changed        int64
	tables         map[string]int64
	kinds          map[string]int64
	cited, citedRq []int64
}

type maintQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type redactWork struct {
	db  *sql.DB
	ctx context.Context
	// write is false for a dry run: the same steps run, nothing is written.
	write                                 bool
	chunksFTS, index, evidence, sightings bool
	// sessionVectors says what the database has of the vectors of the sessions: the
	// vector of a chunk that is changed was made from the text before and goes.
	sessionVectors sessionVectorSchema
	// lits are the known values (nil: none). has says whether a table exists.
	lits     *redact.Literals
	has      func(name string) (bool, error)
	harness  map[int64]string
	retitled map[int64]struct{}
	// requoted holds the quote rows (table/id) that requote has counted, so that
	// a dry run does not count them a second time in literalQuotes.
	requoted map[string]struct{}
}

// redactField applies the known values first, then the rules of the labels: a
// value behind a label that is known stays a [REDACTED:literal] and is not read
// again as a labeled one.
func (w *redactWork) redactField(s string, mode redact.Mode, stats redact.Stats) string {
	return redact.Apply(w.lits.Apply(s, mode, stats), mode, stats)
}

// redactPlain is redactField for a short plain text such as a title or a quote.
func (w *redactWork) redactPlain(s string) string {
	return w.redactField(s, redact.Text, nil)
}

func (w *redactWork) harnessOf(q maintQueryer, session int64) (string, error) {
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

func (w *redactWork) exec(q maintQueryer, query string, args ...any) (int64, error) {
	if !w.write {
		return 1, nil
	}
	res, err := q.ExecContext(w.ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// apply schreibt die Änderungen eines Schritts in einer Transaktion. Die erste
// Anweisung schreibt schon, damit die Sperre mit Wartezeit (busy_timeout)
// erworben wird und nicht erst beim Hochstufen einer Lesesperre sofort scheitert.
func (w *redactWork) apply(edits []redactEdit) (redactBatch, error) {
	res := redactBatch{tables: map[string]int64{}, kinds: map[string]int64{}}
	if len(edits) == 0 {
		return res, nil
	}
	var q maintQueryer = w.db
	var tx *sql.Tx
	if w.write {
		var err error
		if tx, err = w.db.BeginTx(w.ctx, nil); err != nil {
			return res, err
		}
		defer tx.Rollback()
		q = tx
	}
	for _, e := range edits {
		textChanged, rawChanged := e.newText != e.oldText, e.newRaw != e.oldRaw
		// Nur wenn die Zeile noch so ist, wie sie gelesen wurde: ein zweiter
		// Lauf oder ein Wiederholen würde sonst den Volltextindex doppelt löschen.
		var n int64
		var err error
		switch {
		case textChanged && rawChanged:
			n, err = w.exec(q, `UPDATE session_chunks SET text=?, raw=? WHERE id=? AND text=? AND raw=?`, e.newText, e.newRaw, e.id, e.oldText, e.oldRaw)
		case textChanged:
			n, err = w.exec(q, `UPDATE session_chunks SET text=? WHERE id=? AND text=?`, e.newText, e.id, e.oldText)
		default:
			n, err = w.exec(q, `UPDATE session_chunks SET raw=? WHERE id=? AND raw=?`, e.newRaw, e.id, e.oldRaw)
		}
		if err != nil {
			return res, err
		}
		if n == 0 {
			continue
		}
		res.changed++
		if w.sessionVectors != noSessionVectors {
			dropped, err := dropSessionEmbeddings(w.ctx, q, w.sessionVectors, e.id, w.write)
			if err != nil {
				return res, err
			}
			if dropped > 0 {
				res.tables["session_embeddings"] += dropped
			}
		}
		if textChanged {
			res.tables["session_chunks.text"]++
			for k, v := range e.textKinds {
				res.kinds["text/"+k] += v
			}
			if w.chunksFTS {
				if _, err := w.exec(q, `INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', ?, ?)`, e.id, e.oldText); err != nil {
					return res, err
				}
				if _, err := w.exec(q, `INSERT INTO chunks_fts(rowid, text) VALUES(?, ?)`, e.id, e.newText); err != nil {
					return res, err
				}
				res.tables["chunks_fts"]++
			}
		}
		if rawChanged {
			res.tables["session_chunks.raw"]++
			for k, v := range e.rawKinds {
				res.kinds["raw/"+k] += v
			}
			if err := w.reindex(q, e, &res); err != nil {
				return res, err
			}
			if err := w.retitle(q, e, &res); err != nil {
				return res, err
			}
		}
		if textChanged {
			if err := w.requote(q, e, &res); err != nil {
				return res, err
			}
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}
	return res, nil
}

type indexFields struct{ text, thinking, toolInput, toolOutput string }

func (f indexFields) empty() bool {
	return f.text == "" && f.thinking == "" && f.toolInput == "" && f.toolOutput == ""
}

// reindex rebuilds the row of chunk_index and the entry of sess_fts from the
// new raw. sess_fts has its content in chunk_index; its entry is deleted with
// the values it was made from, which is the row as it is stored now. Index
// entries only exist for rows with at least one non-empty field.
func (w *redactWork) reindex(q maintQueryer, e redactEdit, res *redactBatch) error {
	if !w.index {
		return nil
	}
	var old indexFields
	err := q.QueryRowContext(w.ctx, `SELECT text, thinking, tool_input, tool_output FROM chunk_index WHERE chunk_id=?`, e.id).
		Scan(&old.text, &old.thinking, &old.toolInput, &old.toolOutput)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not indexed yet: the backfill reads the redacted raw
	}
	if err != nil {
		return err
	}
	h, err := w.harnessOf(q, e.session)
	if err != nil {
		return err
	}
	f := transcript.Fields(transcript.Parse(h, e.seq, e.newRaw))
	nf := indexFields{f.Text, f.Thinking, f.ToolInput, f.ToolOutput}
	if nf == old {
		return nil
	}
	if !old.empty() {
		if _, err := w.exec(q, `INSERT INTO sess_fts(sess_fts, rowid, text, thinking, tool_input, tool_output) VALUES('delete', ?, ?, ?, ?, ?)`,
			e.id, old.text, old.thinking, old.toolInput, old.toolOutput); err != nil {
			return err
		}
	}
	if _, err := w.exec(q, `UPDATE chunk_index SET text=?, thinking=?, tool_input=?, tool_output=? WHERE chunk_id=?`,
		nf.text, nf.thinking, nf.toolInput, nf.toolOutput, e.id); err != nil {
		return err
	}
	if !nf.empty() {
		if _, err := w.exec(q, `INSERT INTO sess_fts(rowid, text, thinking, tool_input, tool_output) VALUES(?, ?, ?, ?, ?)`,
			e.id, nf.text, nf.thinking, nf.toolInput, nf.toolOutput); err != nil {
			return err
		}
	}
	res.tables["chunk_index"]++
	if !old.empty() || !nf.empty() {
		res.tables["sess_fts"]++
	}
	return nil
}

// retitle sets the session title again where it was cut from the prompt or
// the AI title of the chunk that has just changed. Cut means shortened and
// closed with an ellipsis, so a partial value in it would survive a redaction
// of the title alone.
func (w *redactWork) retitle(q maintQueryer, e redactEdit, res *redactBatch) error {
	h, err := w.harnessOf(q, e.session)
	if err != nil {
		return err
	}
	po, pn := transcript.Parse(h, e.seq, e.oldRaw), transcript.Parse(h, e.seq, e.newRaw)
	fo, fn := transcript.Fields(po), transcript.Fields(pn)
	// The two places indexChunks takes a title from: the first prompt, cut to
	// 80 characters, and the AI title, cut to 120.
	var pairs [][2]string
	if fo.Prompt {
		pairs = append(pairs, [2]string{transcript.OneLine(fo.Text, 80), transcript.OneLine(fn.Text, 80)})
	}
	if po.Title != "" {
		pairs = append(pairs, [2]string{transcript.OneLine(po.Title, 120), transcript.OneLine(pn.Title, 120)})
	}
	if len(pairs) == 0 {
		return nil
	}
	var title string
	if err := q.QueryRowContext(w.ctx, `SELECT title FROM sessions WHERE id=?`, e.session).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	for _, p := range pairs {
		if p[0] == "" || p[0] == p[1] || p[0] != title {
			continue
		}
		if _, err := w.exec(q, `UPDATE sessions SET title=? WHERE id=? AND title=?`, p[1], e.session, p[0]); err != nil {
			return err
		}
		res.tables["sessions.title"]++
		w.retitled[e.session] = struct{}{}
		break
	}
	return nil
}

// requote rewrites the verbatim quotes that evidence rows hold from this chunk.
// A quote that holds the label and the value is redacted like the text and
// stays a substring of it. A quote that cuts the value out of its label is no
// longer in the new text and would not be recognised by itself: it is
// replaced as a whole.
func (w *redactWork) requote(q maintQueryer, e redactEdit, res *redactBatch) error {
	for _, t := range []struct {
		on    bool
		table string
		idCol string
	}{
		{w.evidence, "knowledge_evidence", "knowledge_id"},
		{w.sightings, "request_sightings", "request_id"},
	} {
		if !t.on {
			continue
		}
		type row struct {
			id, owner int64
			quote     string
		}
		var found []row
		rows, err := q.QueryContext(w.ctx, `SELECT id, `+t.idCol+`, quote FROM `+t.table+` WHERE session_id=? AND chunk_seq=?`, e.session, e.seq)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.owner, &r.quote); err != nil {
				rows.Close()
				return err
			}
			found = append(found, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range found {
			if t.table == "knowledge_evidence" {
				res.cited = append(res.cited, r.owner)
			} else {
				res.citedRq = append(res.citedRq, r.owner)
			}
			nq := redactQuote(e.oldText, e.newText, r.quote, w.redactPlain)
			if nq == r.quote {
				continue
			}
			if _, err := w.exec(q, `UPDATE `+t.table+` SET quote=? WHERE id=?`, nq, r.id); err != nil {
				return err
			}
			w.requoted[redactQuoteKey(t.table, r.id)] = struct{}{}
			res.tables[t.table+".quote"]++
		}
	}
	return nil
}

// redactQuote returns the quote as it is to be stored. fix redacts a text the
// way the chunk text was redacted.
func redactQuote(oldText, newText, quote string, fix func(string) string) string {
	if quote == "" || !strings.Contains(oldText, quote) {
		return fix(quote)
	}
	if nq := fix(quote); nq != quote && strings.Contains(newText, nq) {
		return nq
	}
	if strings.Contains(newText, quote) {
		return quote
	}
	return redactQuoteMarker
}

// titles redacts the session titles that still hold a secret, whatever they
// were cut from.
func (w *redactWork) titles() (int64, error) {
	type row struct {
		id           int64
		title, fixed string
	}
	var found []row
	rows, err := w.db.QueryContext(w.ctx, `SELECT id, title FROM sessions WHERE title != ''`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title); err != nil {
			rows.Close()
			return 0, err
		}
		if _, done := w.retitled[r.id]; done && !w.write {
			continue // a dry run has not written the title that retitle counted
		}
		if r.fixed = w.redactPlain(r.title); r.fixed != r.title {
			found = append(found, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(found) == 0 {
		return 0, err
	}
	if !w.write {
		return int64(len(found)), nil
	}
	tx, err := w.db.BeginTx(w.ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int64
	for _, r := range found {
		res, err := tx.ExecContext(w.ctx, `UPDATE sessions SET title=? WHERE id=? AND title=?`, r.fixed, r.id, r.title)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += c
	}
	return n, tx.Commit()
}
