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

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
)

// SlimTextOptions steuert SlimText.
type SlimTextOptions struct {
	// DryRun leitet für jede Zeile mit Text den Text ab, zählt nach Klasse und
	// schreibt nichts.
	DryRun bool
	// Restore schreibt den Text zurück (für den Rückbau auf ein älteres Binary),
	// statt ihn zu leeren.
	Restore bool
	// Force setzt sich über einen offenen Destillier-Stapel hinweg.
	Force bool
	// Batch ist die Zahl der Zeilen von session_chunks je Transaktion (Standard 500).
	Batch int
	// Pause wartet nach jedem Stapel mindestens so lange, wie seine Transaktion
	// die Schreibsperre hielt, und nie kürzer als Pause (Standard 20 ms).
	Pause time.Duration
	// AfterID setzt nach dieser Zeile fort (ein abgebrochener Lauf nennt sie).
	AfterID int64
	// Progress wird nach jedem Stapel mit dem bisherigen Stand aufgerufen.
	Progress func(SlimTextReport)
}

// SlimTextReport ist das Ergebnis, auch das eines abgebrochenen Laufs. Es
// enthält Zahlen, keinen Inhalt.
type SlimTextReport struct {
	DryRun  bool `json:"dry_run"`
	Restore bool `json:"restore"`
	// Rows ist die Zahl der Zeilen von session_chunks zu Beginn.
	Rows int64 `json:"rows"`
	// Blocked nennt, was einen echten Lauf verweigern ließe. Beim Probelauf ist
	// es eine Auskunft, sonst endet der Lauf mit einem Fehler.
	Blocked []string `json:"blocked,omitempty"`

	// Probelauf, nur Zeilen mit gespeichertem Text: Same hat abgeleitet genau den
	// gespeicherten Text, Cut weicht ab und eine Kürzungsmarke ist beteiligt
	// (lange Werkzeugausgaben, die raw an anderer Stelle kürzt als der Sammler),
	// Other weicht sonst ab. StoredBytes ist die Länge des gespeicherten Textes
	// dieser Zeilen, DerivedBytes die des abgeleiteten.
	WithText     int64 `json:"with_text"`
	Same         int64 `json:"same"`
	Cut          int64 `json:"cut"`
	Other        int64 `json:"other"`
	StoredBytes  int64 `json:"stored_bytes"`
	DerivedBytes int64 `json:"derived_bytes"`

	// Emptied sind die Zeilen, deren Text der Lauf geleert hat, Chars ihre Zeichen
	// (in text_chars festgehalten). Restored sind die Zeilen, denen --restore den
	// Text zurückgab.
	Emptied  int64 `json:"emptied"`
	Chars    int64 `json:"chars"`
	Restored int64 `json:"restored"`

	// LastID ist die letzte festgeschriebene Zeile.
	LastID   int64         `json:"last_id"`
	Duration time.Duration `json:"duration_ns"`
	// Batches ist die Zahl der schreibenden Transaktionen, Write die Zeit, in der
	// der Lauf die Schreibsperre hielt, LockP50, LockP95 und LockMax die Dauer
	// der Sperre je Stapel.
	Batches int           `json:"batches"`
	Write   time.Duration `json:"write_ns"`
	LockP50 time.Duration `json:"lock_p50_ns"`
	LockP95 time.Duration `json:"lock_p95_ns"`
	LockMax time.Duration `json:"lock_max_ns"`
}

// slimTextDSN öffnet die Datenbank für den Lauf; Tests setzen ein kurzes busy_timeout.
var slimTextDSN = func(path string) string { return maintenanceDSN(path) }

// Kürzungsmarken, die in einem Text stehen können: die von slim.Cap und die des
// Sammlers für ein sehr langes Werkzeugergebnis.
var cutMarkers = []string{"[ghosttree: gekürzt, ", "…(tool result truncated)"}

func hasCutMarker(s string) bool {
	for _, m := range cutMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// SlimText leert session_chunks.text, ohne dass ein Leser etwas merkt (IDEA-25):
// wer den Text braucht, leitet ihn aus raw ab (chunktext). Je Stapel von
// Zeilen steht in einer kurzen Transaktion
//
//	UPDATE session_chunks SET text_chars = length(text), text = ''
//	 WHERE id > ? AND id <= ? AND text != ''
//
// Der Lauf geht in der Reihenfolge von id (Keyset) und ist wiederholbar: eine
// geleerte Zeile erfüllt text != ” nicht mehr, ein zweiter Lauf schreibt
// nichts, ein abgebrochener setzt an derselben Stelle fort (oder mit AfterID).
// Zeilen, die ein Server daneben schreibt, werden mit dem Keyset erfasst oder
// bleiben mit ihrem Text stehen, was die Leseregel verträgt.
//
// Vorbedingungen für einen echten Lauf, sonst ein Fehler mit Hinweis:
//   - text_chars und chunk_index sind da (der Server dieser Version lief einmal);
//   - ctx db slim-search ist gelaufen: der Auslöser chunks_ai ist entfernt und
//     chunks_fts leer, denn chunks_fts nennt die Spalte text;
//   - kein Destillier-Stapel ist offen (Force übersteuert): ein Zitat wird beim
//     Einsammeln gegen den Text geprüft, und bei den wenigen Zeilen, die
//     abgeleitet abweichen, könnte es scheitern.
//
// Der Probelauf prüft nichts davon, nennt es aber in Blocked, und zählt die
// Zeilen nach Klasse (Same, Cut, Other).
//
// Restore schreibt für jede Zeile mit leerem Text und text_chars > 0 den
// abgeleiteten Text zurück. Für Zeilen, bei denen die Ableitung gleich ist, ist
// er bytegleich mit dem früheren, für die anderen die abgeleitete Fassung.
// Davor ist der Server auf -session-text=stored zu stellen, sonst entstehen
// hinter dem Lauf wieder leere Zeilen.
func SlimText(ctx context.Context, path string, opts SlimTextOptions) (SlimTextReport, error) {
	start := time.Now()
	rep := SlimTextReport{DryRun: opts.DryRun, Restore: opts.Restore, LastID: opts.AfterID}
	if opts.Batch <= 0 {
		opts.Batch = 500
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
	db, err := sql.Open("sqlite", slimTextDSN(path))
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
	fail := func(err error) (SlimTextReport, error) {
		rep.Duration = time.Since(start)
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	if ok, err := has("session_chunks"); err != nil {
		return rep, err
	} else if !ok {
		return rep, errors.New("no session_chunks table: start the new ghosttree server once so it migrates the database")
	}
	if ok, err := hasColumn(db, "session_chunks", "text_chars"); err != nil {
		return rep, err
	} else if !ok {
		return rep, errors.New("session_chunks has no text_chars column: start the new ghosttree server once so it migrates the database")
	}
	if rep.Blocked, err = slimTextBlockers(ctx, db, has, opts); err != nil {
		return rep, err
	}
	if len(rep.Blocked) > 0 && !opts.DryRun && !opts.Restore {
		return rep, fmt.Errorf("not safe to run yet: %s", strings.Join(rep.Blocked, "; "))
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_chunks`).Scan(&rep.Rows); err != nil {
		return fail(err)
	}
	raw := rawStateFor(db)
	var locks []time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		var hi int64
		var bound sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT max(id) FROM (SELECT id FROM session_chunks WHERE id > ? ORDER BY id LIMIT ?)`,
			rep.LastID, opts.Batch).Scan(&bound); err != nil {
			return fail(err)
		}
		if !bound.Valid {
			break
		}
		hi = bound.Int64
		var held time.Duration
		var err error
		switch {
		case opts.Restore:
			held, err = slimTextRestore(ctx, db, raw, opts, &rep, hi)
		case opts.DryRun:
			err = slimTextCount(ctx, db, raw, &rep, hi)
		default:
			held, err = slimTextEmpty(ctx, db, &rep, hi)
		}
		if err != nil {
			return fail(err)
		}
		rep.LastID = hi
		if held > 0 {
			rep.Batches++
			rep.Write += held
			locks = append(locks, held)
		}
		if opts.Progress != nil {
			rep.Duration = time.Since(start)
			opts.Progress(rep)
		}
		if opts.DryRun {
			continue
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(max(opts.Pause, held)):
		}
	}
	rep.LockP50, rep.LockP95, rep.LockMax = lockPercentiles(locks)
	if !opts.DryRun {
		checkpoint(ctx, db)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

// slimTextBlockers nennt, was einen Lauf verweigern lässt.
func slimTextBlockers(ctx context.Context, db *sql.DB, has func(string) (bool, error), opts SlimTextOptions) ([]string, error) {
	if opts.Restore {
		return nil, nil
	}
	var out []string
	if ok, err := hasWordIndex(ctx, db, has); err != nil {
		return nil, err
	} else if !ok {
		out = append(out, "no chunk_index table: start the new ghosttree server once so it migrates the database")
	}
	live, err := chunksFTSLive(ctx, db)
	if err != nil {
		return nil, err
	}
	var indexed int64
	if ok, err := has("chunks_fts_docsize"); err != nil {
		return nil, err
	} else if ok {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunks_fts_docsize`).Scan(&indexed); err != nil {
			return nil, err
		}
	}
	if live || indexed > 0 {
		out = append(out, "the old search index chunks_fts still stands on text (trigger chunks_ai or entries left): run ctx db slim-search first")
	}
	if !opts.Force {
		if ok, err := has("distill_batches"); err != nil {
			return nil, err
		} else if ok {
			var open int64
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM distill_batches WHERE state = 'open'`).Scan(&open); err != nil {
				return nil, err
			}
			if open > 0 {
				out = append(out, fmt.Sprintf("%d distillation batch(es) still open: their quotes are checked against the text when collected; wait for them or pass --force", open))
			}
		}
	}
	return out, nil
}

// slimTextEmpty leert den Text der Zeilen bis hi in einer Transaktion und gibt
// zurück, wie lange sie die Schreibsperre hielt.
func slimTextEmpty(ctx context.Context, db *sql.DB, rep *SlimTextReport, hi int64) (time.Duration, error) {
	for attempt := 0; ; attempt++ {
		held, emptied, chars, err := slimTextEmptyOnce(ctx, db, rep.LastID, hi)
		if err == nil {
			rep.Emptied += emptied
			rep.Chars += chars
			return held, nil
		}
		if !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(slimBusyBackoff[attempt]):
		}
	}
}

func slimTextEmptyOnce(ctx context.Context, db *sql.DB, lo, hi int64) (held time.Duration, emptied, chars int64, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	// Die Sperre beginnt mit dem ersten Schreibzugriff; die Zeit läuft von dort
	// bis zum Festschreiben.
	began := time.Now()
	rows, err := tx.QueryContext(ctx, `UPDATE session_chunks SET text_chars = length(text), text = ''
		WHERE id > ? AND id <= ? AND text != '' RETURNING text_chars`, lo, hi)
	if err != nil {
		return 0, 0, 0, err
	}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		emptied++
		chars += n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO index_state(key, val) VALUES('slim_text_cursor', ?)`, hi); err != nil {
		// index_state fehlt in einer Datei ohne Suchindex: der Lauf braucht es nicht.
		if !isNoSuchTable(err) {
			return 0, 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return time.Since(began), emptied, chars, nil
}

type slimTextRow struct {
	id      int64
	text    string
	raw     rawcodec.Value
	harness string
}

// slimTextRead liest die Zeilen bis hi, ohne zu sperren, mit entpackter Zeile.
func slimTextRead(ctx context.Context, db *sql.DB, raw *rawState, query string, lo, hi int64) ([]slimTextRow, []string, error) {
	rows, err := db.QueryContext(ctx, query, lo, hi)
	if err != nil {
		return nil, nil, err
	}
	var out []slimTextRow
	for rows.Next() {
		var r slimTextRow
		if err := rows.Scan(&r.id, &r.text, &r.raw, &r.harness); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	lines := make([]string, len(out))
	for i, r := range out {
		if lines[i], err = raw.line(ctx, db, r.raw); err != nil {
			return nil, nil, fmt.Errorf("chunk %d: %w", r.id, err)
		}
	}
	return out, lines, nil
}

// slimTextCount ist der Probelauf eines Stapels.
func slimTextCount(ctx context.Context, db *sql.DB, raw *rawState, rep *SlimTextReport, hi int64) error {
	rows, lines, err := slimTextRead(ctx, db, raw, `SELECT c.id, c.text, c.raw, se.harness FROM session_chunks c
		JOIN sessions se ON se.id = c.session_id WHERE c.id > ? AND c.id <= ? AND c.text != ''`, rep.LastID, hi)
	if err != nil {
		return err
	}
	for i, r := range rows {
		_, derived := chunktext.Derive(r.harness, lines[i])
		rep.WithText++
		rep.StoredBytes += int64(len(r.text))
		rep.DerivedBytes += int64(len(derived))
		switch {
		case derived == r.text:
			rep.Same++
		case hasCutMarker(r.text) || hasCutMarker(derived):
			rep.Cut++
		default:
			rep.Other++
		}
	}
	return nil
}

// slimTextRestore gibt den Zeilen bis hi ihren Text zurück.
func slimTextRestore(ctx context.Context, db *sql.DB, raw *rawState, opts SlimTextOptions, rep *SlimTextReport, hi int64) (time.Duration, error) {
	rows, lines, err := slimTextRead(ctx, db, raw, `SELECT c.id, c.text, c.raw, se.harness FROM session_chunks c
		JOIN sessions se ON se.id = c.session_id WHERE c.id > ? AND c.id <= ? AND c.text = '' AND c.text_chars > 0`, rep.LastID, hi)
	if err != nil {
		return 0, err
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		_, texts[i] = chunktext.Derive(r.harness, lines[i])
	}
	if opts.DryRun {
		for _, t := range texts {
			if t != "" {
				rep.Restored++
			}
		}
		return 0, nil
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for attempt := 0; ; attempt++ {
		held, restored, err := slimTextRestoreOnce(ctx, db, rows, texts)
		if err == nil {
			rep.Restored += restored
			return held, nil
		}
		if !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(slimBusyBackoff[attempt]):
		}
	}
}

func slimTextRestoreOnce(ctx context.Context, db *sql.DB, rows []slimTextRow, texts []string) (time.Duration, int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	began := time.Now()
	stmt, err := tx.PrepareContext(ctx, `UPDATE session_chunks SET text = ? WHERE id = ? AND text = ''`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()
	var restored int64
	for i, r := range rows {
		if texts[i] == "" {
			continue
		}
		res, err := stmt.ExecContext(ctx, texts[i], r.id)
		if err != nil {
			return 0, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		restored += n
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return time.Since(began), restored, nil
}

func lockPercentiles(d []time.Duration) (p50, p95, top time.Duration) {
	if len(d) == 0 {
		return 0, 0, 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return at(0.5), at(0.95), s[len(s)-1]
}
