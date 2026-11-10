package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// SlimSearchOptions steuert SlimSearch.
type SlimSearchOptions struct {
	// DryRun zählt, was umgetragen und geleert würde, und schreibt nichts.
	DryRun bool
	// Batch ist die Zahl der Zeilen von chunk_index je Transaktion (Standard 500).
	Batch int
	// Pause wartet nach jedem Schritt mindestens so lange, wie seine
	// Transaktion die Schreibsperre hielt, und nie kürzer als Pause (Standard
	// 20 ms).
	Pause time.Duration
	// AfterID setzt nach dieser Zeile fort (ein abgebrochener Lauf nennt sie).
	AfterID int64
	// SkipOptimize lässt 'optimize' von chunk_fts am Ende weg.
	SkipOptimize bool
	// Progress wird nach jedem Schritt mit dem bisherigen Stand aufgerufen.
	Progress func(SlimSearchReport)
}

// SlimSearchReport ist das Ergebnis, auch das eines abgebrochenen Laufs.
type SlimSearchReport struct {
	DryRun bool `json:"dry_run"`
	// Rows ist die Zahl der Zeilen von chunk_index zu Beginn.
	Rows int64 `json:"rows"`
	// Moved sind die Zeilen, die von sess_fts nach chunk_fts wanderten (mit
	// Wörtern), MovedEmpty die ohne Wörter, die nur umgestellt wurden.
	Moved      int64 `json:"moved"`
	MovedEmpty int64 `json:"moved_empty"`
	// FieldBytes sind die Bytes der vier Felder, die chunk_index abgibt;
	// KeptPromptBytes der Text von Prompts, den es behält.
	FieldBytes      int64 `json:"field_bytes"`
	KeptPromptBytes int64 `json:"kept_prompt_bytes"`
	// Remaining sind Zeilen, die am Ende noch in sess_fts stehen (ein älteres
	// Binary schrieb sie hinter dem Lauf); dann bleibt sess_fts gefüllt.
	Remaining int64 `json:"remaining"`
	// SessFTSEmptied: sess_fts hatte keine Zeile mehr und wurde mit 'delete-all'
	// geleert.
	SessFTSEmptied bool `json:"sess_fts_emptied"`
	// ChunksFTSRows ist die Zahl der Einträge in chunks_fts; ChunksFTSEmptied, ob
	// der Auslöser chunks_ai entfernt und der Index geleert wurde.
	ChunksFTSRows    int64 `json:"chunks_fts_rows"`
	ChunksFTSEmptied bool  `json:"chunks_fts_emptied"`
	// LastID ist die letzte festgeschriebene Zeile.
	LastID    int64         `json:"last_id"`
	Optimized []string      `json:"optimized,omitempty"`
	Duration  time.Duration `json:"duration_ns"`
	// Write ist die Zeit, in der der Lauf die Schreibsperre hielt.
	Write time.Duration `json:"write_ns"`
}

// slimSearchDSN öffnet die Datenbank für den Lauf; Tests setzen ein kurzes busy_timeout.
var slimSearchDSN = func(path string) string { return maintenanceDSN(path) }

// SlimSearch baut den doppelten Suchpfad über Sessions ab (REQ-622 Kriterium 4):
//
//  1. Jede Zeile von chunk_index, deren Wörter noch in sess_fts stehen (fts = 0),
//     wandert nach chunk_fts: Eintrag in chunk_fts mit den gespeicherten Feldern,
//     'delete' in sess_fts mit denselben Werten, danach behält die Zeile nur den
//     Text eines Prompts (fts = 1). Keine Zeile wird aus raw neu abgeleitet,
//     nichts an session_chunks, den Vektoren oder den Destillaten ändert sich.
//  2. Steht keine Zeile mehr in sess_fts, wird es mit 'delete-all' geleert.
//  3. Der Auslöser chunks_ai wird entfernt und chunks_fts geleert; kein Leser
//     nutzt es mehr.
//  4. 'optimize' von chunk_fts.
//
// Der Lauf geht in der Reihenfolge von chunk_id (Keyset) in kurzen
// Transaktionen; jede beginnt mit einem Schreibzugriff, damit sie liest, was sie
// danach schreibt. Er ist wiederholbar: umgetragene Zeilen (fts = 1) werden
// übersprungen. Er öffnet die Datenbank ohne Migrationen: der Server dieser
// Version muss einmal gelaufen sein.
func SlimSearch(ctx context.Context, path string, opts SlimSearchOptions) (SlimSearchReport, error) {
	start := time.Now()
	rep := SlimSearchReport{DryRun: opts.DryRun, LastID: opts.AfterID}
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
	db, err := sql.Open("sqlite", slimSearchDSN(path))
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
	if ok, err := hasWordIndex(ctx, db, has); err != nil {
		return rep, err
	} else if !ok {
		return rep, errors.New("no chunk_index table: start the new ghosttree server once so it migrates the database")
	}
	fail := func(err error) (SlimSearchReport, error) {
		rep.Duration = time.Since(start)
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunk_index`).Scan(&rep.Rows); err != nil {
		return fail(err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		began := time.Now()
		n, err := slimSearchStep(ctx, db, opts, &rep)
		if err != nil {
			return fail(err)
		}
		if n == 0 {
			break
		}
		took := time.Since(began)
		if !opts.DryRun {
			rep.Write += took
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
		case <-time.After(max(opts.Pause, took)):
		}
	}

	if err := slimSearchFinish(ctx, db, opts, &rep); err != nil {
		return fail(err)
	}
	if !opts.DryRun {
		checkpoint(ctx, db)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

type movedRow struct {
	id     int64
	prompt bool
	f      indexFields
}

// slimSearchStep trägt einen Stapel um und gibt zurück, wie viele Zeilen er
// fand. Beim Probelauf liest er nur.
func slimSearchStep(ctx context.Context, db *sql.DB, opts SlimSearchOptions, rep *SlimSearchReport) (int, error) {
	read := func(q maintQueryer) ([]movedRow, error) {
		rows, err := q.QueryContext(ctx, `SELECT chunk_id, is_prompt, text, thinking, tool_input, tool_output FROM chunk_index
			WHERE chunk_id > ? AND fts = 0 ORDER BY chunk_id LIMIT ?`, rep.LastID, opts.Batch)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []movedRow
		for rows.Next() {
			var r movedRow
			var prompt int
			if err := rows.Scan(&r.id, &prompt, &r.f.text, &r.f.thinking, &r.f.toolInput, &r.f.toolOutput); err != nil {
				return nil, err
			}
			r.prompt = prompt == 1
			out = append(out, r)
		}
		return out, rows.Err()
	}
	count := func(batch []movedRow, r *SlimSearchReport) {
		for _, m := range batch {
			kept := 0
			if m.prompt {
				kept = len(m.f.text)
			}
			r.FieldBytes += int64(len(m.f.text)+len(m.f.thinking)+len(m.f.toolInput)+len(m.f.toolOutput)) - int64(kept)
			r.KeptPromptBytes += int64(kept)
			if m.f.empty() {
				r.MovedEmpty++
			} else {
				r.Moved++
			}
		}
		if len(batch) > 0 {
			r.LastID = batch[len(batch)-1].id
		}
	}
	if opts.DryRun {
		batch, err := read(db)
		if err != nil {
			return 0, err
		}
		count(batch, rep)
		return len(batch), nil
	}
	// Wie slim-sessions: hält der Server die Sperre länger als busy_timeout,
	// wird derselbe Schritt nach einer Pause wiederholt.
	for attempt := 0; ; attempt++ {
		n, err := slimSearchMove(ctx, db, read, count, rep)
		if err == nil || !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return n, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(slimBusyBackoff[attempt]):
		}
	}
}

func slimSearchMove(ctx context.Context, db *sql.DB, read func(maintQueryer) ([]movedRow, error),
	count func([]movedRow, *SlimSearchReport), rep *SlimSearchReport) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Zuerst schreiben: die Transaktion hält damit die Sperre, und was sie liest,
	// kann sich bis zum Festschreiben nicht mehr ändern.
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO index_state(key, val) VALUES('slim_search_cursor', ?)`, rep.LastID); err != nil {
		return 0, err
	}
	batch, err := read(tx)
	if err != nil {
		return 0, err
	}
	exec := func(q maintQueryer, query string, args ...any) (int64, error) {
		res, err := q.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	for _, m := range batch {
		if err := rewriteWords(exec, tx, m.id, indexRow{fields: m.f, prompt: m.prompt}, m.f, m.prompt); err != nil {
			return 0, fmt.Errorf("chunk %d: %w", m.id, err)
		}
	}
	if len(batch) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO index_state(key, val) VALUES('slim_search_cursor', ?)`, batch[len(batch)-1].id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	count(batch, rep)
	return len(batch), nil
}

// slimSearchFinish leert sess_fts, wenn keine Zeile mehr darin steht, entfernt
// chunks_ai und leert chunks_fts, und führt chunk_fts zusammen.
func slimSearchFinish(ctx context.Context, db *sql.DB, opts SlimSearchOptions, rep *SlimSearchReport) error {
	var live bool
	err := retryBusy(ctx, opts.DryRun, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if !opts.DryRun {
			// Die Sperre zuerst, wie in slimSearchMove.
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO index_state(key, val) VALUES('slim_search_cursor', ?)`, rep.LastID); err != nil {
				return err
			}
		}
		// Ein voller Durchlauf; nach dem Umtragen sind die Zeilen schmal. Der
		// Probelauf hat jede Zeile mit fts = 0 schon gezählt.
		if !opts.DryRun {
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chunk_index WHERE fts = 0`).Scan(&rep.Remaining); err != nil {
				return err
			}
		}
		if live, err = chunksFTSLive(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chunks_fts_docsize`).Scan(&rep.ChunksFTSRows); err != nil {
			return err
		}
		if opts.DryRun {
			return nil
		}
		if rep.Remaining == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sess_fts(sess_fts) VALUES('delete-all')`); err != nil {
				return err
			}
			rep.SessFTSEmptied = true
		}
		if live || rep.ChunksFTSRows > 0 {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS chunks_ai`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES('delete-all')`); err != nil {
				return err
			}
			rep.ChunksFTSEmptied = true
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	if opts.DryRun || opts.SkipOptimize || rep.Moved == 0 {
		return nil
	}
	began := time.Now()
	err = retryBusy(ctx, false, func() error {
		_, err := db.ExecContext(ctx, `INSERT INTO chunk_fts(chunk_fts) VALUES('optimize')`)
		return err
	})
	rep.Write += time.Since(began)
	if err != nil {
		return err
	}
	rep.Optimized = append(rep.Optimized, "chunk_fts")
	return nil
}

// retryBusy wiederholt fn nach den Pausen von slimBusyBackoff, solange es an der
// Schreibsperre scheitert (beim Probelauf nicht).
func retryBusy(ctx context.Context, dryRun bool, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || dryRun || !isBusy(err) || attempt >= len(slimBusyBackoff) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(slimBusyBackoff[attempt]):
		}
	}
}
