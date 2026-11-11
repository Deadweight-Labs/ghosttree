package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbSlimTextUsage = `usage: ctx db slim-text --db <path> [--dry-run] [--restore] [--force] [--batch N]
                         [--pause D] [--after-id N] [--json]

Stop storing the text of transcript chunks (IDEA-25). session_chunks.text is
the text of a line as the collector read it; it can be derived from the stored
line (session_chunks.raw), and readers do so whenever the column is empty. This
command empties the column: per batch of --batch rows, in one short transaction,
text_chars takes the length of the text (ctx cost counts it) and text becomes
empty. Searching, the session page, the API and quotes stay as they are. A row
is emptied only if something can be derived from its stored line; a row whose
line the parsers do not understand keeps its text (class underivable), since
that text is the only copy.

Before it runs, ctx db slim-search must have run (the old search index
chunks_fts stands on text), and no distillation batch may be open (--force
overrides that one). The server should run with --session-text=derived, so that
new chunks are written without text.

--dry-run writes nothing. It derives the text of every row that still has one
and counts by class: same (derived equals stored), cut (differs, a truncation
mark is involved: long tool outputs that the stored line cuts somewhere else
than the collector did), other, underivable (nothing can be derived; the real
run leaves these rows alone). Only numbers are printed. Run it on a copy of the
production database first.

It may run while the server is up; the server must already run the version that
brings this command, since that one creates the column text_chars. It walks
session_chunks in id order and pauses at least as long as a transaction held
the lock (never less than --pause). A second run finds nothing to do; an
interrupted run may be started again.

--restore goes back: it writes the derived text into every row that is empty
but had text (text_chars > 0). Rows whose derived text equals the old one get
it byte for byte. For cut and other the old text is not kept anywhere else
than in the backup of the database: --restore gives the derived version, so
take that backup before the run. Do this before going back to
a binary older than this command (it would read empty transcripts), and set
--session-text=stored first, or new rows are written empty again.

The file does not shrink by itself. Take a backup before, and afterwards stop
the server and run "ctx db vacuum --db <path>".`

func cmdDBSlimText(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db slim-text", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count by class without writing")
	restore := fs.Bool("restore", false, "write the derived text back")
	force := fs.Bool("force", false, "run although a distillation batch is open")
	batch := fs.Int("batch", 500, "rows of session_chunks per transaction")
	pause := fs.Duration("pause", 20*time.Millisecond, "least pause between transactions")
	afterID := fs.Int64("after-id", 0, "continue after this chunk id")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbSlimTextUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *pause < 0 || *afterID < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbSlimTextUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun && !*asJSON {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	lastLine := time.Now()
	rep, err := store.SlimText(ctx, *dbPath, store.SlimTextOptions{
		DryRun:  *dryRun,
		Restore: *restore,
		Force:   *force,
		Batch:   *batch,
		Pause:   *pause,
		AfterID: *afterID,
		Progress: func(r store.SlimTextReport) {
			if *asJSON || time.Since(lastLine) < 5*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to chunk id %d of %d rows, %s\n", r.LastID, r.Rows, r.Duration.Round(time.Second))
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		if err != nil && !interrupted {
			fmt.Fprintf(stdout, "slim-text failed: %v\n", err)
		}
		reportSlimText(stdout, rep, interrupted)
	}
	if err != nil {
		return 1
	}
	return 0
}

func reportSlimText(stdout io.Writer, r store.SlimTextReport, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted after chunk id %d; run it again to continue\n", r.LastID)
	}
	for _, b := range r.Blocked {
		fmt.Fprintf(stdout, "blocked: %s\n", b)
	}
	fmt.Fprintf(stdout, "session_chunks: %d rows\n", r.Rows)
	switch {
	case r.Restore && r.DryRun:
		fmt.Fprintf(stdout, "rows that would get their text back: %d\n", r.Restored)
	case r.Restore:
		fmt.Fprintf(stdout, "text written back: %d rows\n", r.Restored)
	case r.DryRun:
		fmt.Fprintf(stdout, "rows with text: %d, text %s\n", r.WithText, humanSize(r.StoredBytes))
		fmt.Fprintf(stdout, "  derived equal to stored: %d\n", r.Same)
		fmt.Fprintf(stdout, "  derived differs, truncation mark involved: %d\n", r.Cut)
		fmt.Fprintf(stdout, "  derived differs otherwise: %d\n", r.Other)
		fmt.Fprintf(stdout, "  nothing derivable, text stays: %d\n", r.Underivable)
		if r.Cut+r.Other > 0 {
			fmt.Fprintf(stdout, "  %d rows (cut, other) would read the derived text; only the database backup brings the old text back\n", r.Cut+r.Other)
		}
		fmt.Fprintf(stdout, "  derived text of these rows: %s\n", humanSize(r.DerivedBytes))
	default:
		fmt.Fprintf(stdout, "text emptied in %d rows (%d characters kept in text_chars)\n", r.Emptied, r.Chars)
		if r.Underivable > 0 {
			fmt.Fprintf(stdout, "%d rows keep their text: nothing can be derived from their line\n", r.Underivable)
		}
		if r.Batches > 0 {
			fmt.Fprintf(stdout, "write lock per batch: p50 %s, p95 %s, max %s (%d batches)\n",
				r.LockP50.Round(10*time.Microsecond), r.LockP95.Round(10*time.Microsecond), r.LockMax.Round(10*time.Microsecond), r.Batches)
		}
	}
	fmt.Fprintf(stdout, "took %s, of which %s holding the write lock\n", r.Duration.Round(time.Millisecond), r.Write.Round(time.Millisecond))
	switch {
	case !r.DryRun && !r.Restore && r.Emptied > 0:
		fmt.Fprintln(stdout, "the file shrinks only after VACUUM: stop the server, then run ctx db vacuum --db <db>")
		fmt.Fprintln(stdout, "going back to a binary older than this command: run ctx db slim-text --restore first")
	case !r.DryRun && r.Restore && r.Restored > 0:
		fmt.Fprintln(stdout, "set --session-text=stored on the server, or new rows are written without text again")
	}
}
