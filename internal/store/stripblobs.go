package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
)

// StripBlobsOptions steuert StripSessionBlobs.
type StripBlobsOptions struct {
	// DryRun zählt nur, was ersetzt würde, und schreibt nichts.
	DryRun bool
	// Batch ist die Zahl der Zeilen je Transaktion (Standard 200).
	Batch int
	// BatchBytes begrenzt die Rohdaten, die ein Schritt im Speicher hält
	// (Standard 64 MiB); eine einzelne größere Zeile geht allein durch.
	BatchBytes int
	// Sleep wartet nach jedem Schritt, damit der laufende Server Luft behält.
	Sleep time.Duration
	// Progress wird nach jedem Schritt mit dem bisherigen Stand aufgerufen.
	Progress func(StripBlobsReport)
}

// StripBlobsReport ist das Ergebnis, auch das eines abgebrochenen Laufs.
type StripBlobsReport struct {
	// Chunks ist die Zahl der Zeilen in session_chunks zu Beginn.
	Chunks int64
	// Candidates sind die Zeilen, deren raw einen Hinweis auf eine Nutzlast
	// enthält und die darum gelesen und geprüft wurden.
	Candidates int64
	// Changed sind die Zeilen, die eine Nutzlast trugen.
	Changed int64
	// SavedBytes ist die Länge von raw vorher minus nachher, über alle Zeilen.
	SavedBytes int64
	// LastID ist die letzte geprüfte Zeile; ein neuer Lauf dürfte hier
	// weitermachen, braucht es aber nicht, weil er ohnehin nichts mehr ändert.
	LastID int64
	// DroppedVectors sind die Vektoren der Sessions (REQ-538), die zu geänderten
	// Chunks gehörten und entfernt wurden; der Server rechnet sie nach.
	DroppedVectors int64
}

const (
	defaultStripBatch      = 200
	defaultStripBatchBytes = 64 << 20
)

// StripSessionBlobs ersetzt in jeder Zeile von session_chunks die base64-Nutzlasten
// (Bilder, PDFs, Audio) durch Platzhalter, wie es AppendChunks bei neuen Zeilen
// schon tut.
//
// Die Datenbank wird ohne die Migrationen von Open geöffnet: der Lauf soll
// gegen einen laufenden Server gehen können. Er liest in der Reihenfolge von id
// (Keyset, nie OFFSET), filtert in SQL auf Zeilen mit einem Hinweis, schreibt
// jeden Schritt in einer kurzen Transaktion und wartet bei einer gesperrten
// Datenbank bis zu 30 Sekunden. Ein zweiter Lauf findet nichts mehr zu ändern.
//
// chunks_fts indiziert text, nicht raw, und hat nur einen INSERT-Trigger; ein
// UPDATE von raw lässt den Volltextindex also unberührt. Die Datei schrumpft
// erst nach VACUUM; bis dahin nimmt sie die frei gewordenen Seiten wieder auf.
func StripSessionBlobs(ctx context.Context, path string, opts StripBlobsOptions) (StripBlobsReport, error) {
	var rep StripBlobsReport
	if opts.Batch <= 0 {
		opts.Batch = defaultStripBatch
	}
	if opts.BatchBytes <= 0 {
		opts.BatchBytes = defaultStripBatchBytes
	}
	// sql.Open legt eine fehlende Datei leer an; ein Tippfehler im Pfad soll
	// keine zweite Datenbank erzeugen.
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

	var table int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_chunks'`).Scan(&table); err != nil {
		return rep, err
	}
	if table == 0 {
		return rep, errors.New("no session_chunks table: not a ghosttree database")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_chunks`).Scan(&rep.Chunks); err != nil {
		return rep, err
	}
	vectors, err := hasSessionEmbeddingTables(ctx, db)
	if err != nil {
		return rep, err
	}

	// Dieselben Hinweise wie blobstrip.Strip; was keinen enthält, kann nichts
	// tragen und wird in SQL ausgesiebt, ohne nach Go übertragen zu werden.
	var hints []string
	var needles []any
	for _, n := range blobstrip.Needles {
		hints = append(hints, `instr(raw, ?) > 0`)
		needles = append(needles, n)
	}
	query := `SELECT id, raw FROM session_chunks WHERE id > ? AND (` + strings.Join(hints, ` OR `) + `) ORDER BY id LIMIT ?`

	// Wird der Lauf abgebrochen, meldet der Treiber mitunter "interrupted"
	// statt des Context-Fehlers; für den Aufrufer ist es ein Abbruch.
	fail := func(err error) (StripBlobsReport, error) {
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		// Das Lesen schließt, bevor geschrieben wird: es gibt nur eine
		// Verbindung, und eine offene Abfrage hielte sie fest.
		var updates []strippedRow
		var batchSaved int64
		var held, read int
		last := rep.LastID
		args := append(append([]any{rep.LastID}, needles...), opts.Batch)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return fail(err)
		}
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return fail(err)
			}
			read++
			last = id
			if out, saved := blobstrip.Strip(raw); saved > 0 {
				updates = append(updates, strippedRow{id, out})
				batchSaved += int64(saved)
			}
			if held += len(raw); held >= opts.BatchBytes {
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail(err)
		}
		if read == 0 {
			return rep, nil
		}
		if !opts.DryRun && len(updates) > 0 {
			dropped, err := writeStripped(ctx, db, updates, vectors)
			if err != nil {
				return fail(err)
			}
			rep.DroppedVectors += dropped
		}
		// Erst nach dem Schreiben: LastID und die Zähler beschreiben nur, was
		// auch festgeschrieben ist.
		rep.LastID = last
		rep.Candidates += int64(read)
		rep.Changed += int64(len(updates))
		rep.SavedBytes += batchSaved
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
}

type strippedRow struct {
	id  int64
	raw string
}

// writeStripped führt die Änderungen eines Schritts in einer Transaktion aus.
// Die erste Anweisung schreibt schon, damit die Sperre mit Wartezeit
// (busy_timeout) erworben wird und nicht erst beim Hochstufen einer Lesesperre
// sofort scheitert.
//
// Mit vectors (die Datenbank hat die Vektoren der Sessions) verschwinden in
// derselben Transaktion die Vektoren der geänderten Chunks: der Server rechnet
// sie aus dem neuen Text nach. Die Zahl der entfernten Vektoren geht zurück.
func writeStripped(ctx context.Context, db *sql.DB, rows []strippedRow, vectors bool) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE session_chunks SET raw = ? WHERE id = ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var dropped int64
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.raw, r.id); err != nil {
			return 0, err
		}
		if vectors {
			n, err := dropSessionEmbeddings(ctx, tx, r.id, true)
			if err != nil {
				return 0, err
			}
			dropped += n
		}
	}
	return dropped, tx.Commit()
}

// maintenanceDSN öffnet eine Datenbank für einen Wartungslauf neben dem Server:
// nur ein längeres busy_timeout, kein Eingriff in Journal oder Schema. Weitere
// PRAGMAs (zum Beispiel "secure_delete(1)") hängen sich an.
func maintenanceDSN(path string, pragmas ...string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := path + sep + "_pragma=busy_timeout(30000)"
	for _, p := range pragmas {
		dsn += "&_pragma=" + p
	}
	return dsn
}
