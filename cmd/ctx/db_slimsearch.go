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

const dbSlimSearchUsage = `usage: ctx db slim-search --db <path> [--dry-run] [--batch N] [--pause D]
                           [--after-id N] [--skip-optimize] [--json]

Leave one search path over sessions (REQ-622). The words of every transcript
line are indexed in chunk_fts, a full-text index that keeps no copy of the
text; snippets are made from the stored line when a page of hits is shown.
Older rows of chunk_index still hold four text fields (messages, thinking,
tool input, tool output) as the content of sess_fts. This command moves them:
each row's words go to chunk_fts and out of sess_fts with the stored values,
and the row keeps only the text of a prompt. Nothing is derived again from the
transcript lines, and session_chunks, the vectors and the distillations stay
as they are, so no distillation and no embedding is started again.

When no row is left in sess_fts it is emptied with 'delete-all'. Then the
trigger chunks_ai is dropped and chunks_fts, the first search index over
sessions that no reader uses any more, is emptied. At the end chunk_fts is
merged with 'optimize' (--skip-optimize leaves it out).

It may run while the server is up; the server must already run the version
that brings this command, since it creates chunk_fts and the column
chunk_index.fts. It walks chunk_index in chunk_id order in short transactions
of --batch rows, waits out a long write lock of the server, and pauses at
least as long as a transaction held the lock (never less than --pause). Rows
already moved are skipped: a second run finds nothing, an interrupted run may
be started again. --dry-run writes nothing and reports what would move.

The file does not shrink by itself. Take a backup before, and afterwards stop
the server and run "ctx db vacuum --db <path>".`

func cmdDBSlimSearch(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db slim-search", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "report what would change without writing")
	batch := fs.Int("batch", 500, "rows of chunk_index per transaction")
	pause := fs.Duration("pause", 20*time.Millisecond, "least pause between transactions")
	afterID := fs.Int64("after-id", 0, "continue after this chunk id")
	skipOptimize := fs.Bool("skip-optimize", false, "do not merge chunk_fts at the end")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbSlimSearchUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *pause < 0 || *afterID < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbSlimSearchUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun && !*asJSON {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	lastLine := time.Now()
	rep, err := store.SlimSearch(ctx, *dbPath, store.SlimSearchOptions{
		DryRun:       *dryRun,
		Batch:        *batch,
		Pause:        *pause,
		AfterID:      *afterID,
		SkipOptimize: *skipOptimize,
		Progress: func(r store.SlimSearchReport) {
			if *asJSON || time.Since(lastLine) < 5*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to chunk id %d: %d rows, %s of fields, %s\n", r.LastID, r.Moved+r.MovedEmpty,
				humanSize(r.FieldBytes), r.Duration.Round(time.Second))
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		if err != nil && !interrupted {
			fmt.Fprintf(stdout, "slim-search failed: %v\n", err)
		}
		reportSlimSearch(stdout, rep, interrupted)
	}
	if err != nil {
		return 1
	}
	return 0
}

func reportSlimSearch(stdout io.Writer, r store.SlimSearchReport, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted after chunk id %d; run it again to continue\n", r.LastID)
	}
	would := ""
	if r.DryRun {
		would = "would be "
	}
	fmt.Fprintf(stdout, "chunk_index: %d rows, %d %smoved to chunk_fts (%d of them without words)\n",
		r.Rows, r.Moved+r.MovedEmpty, would, r.MovedEmpty)
	fmt.Fprintf(stdout, "  text fields %sgiven up: %s; prompt text kept: %s\n", would, humanSize(r.FieldBytes), humanSize(r.KeptPromptBytes))
	switch {
	case r.SessFTSEmptied:
		fmt.Fprintln(stdout, "sess_fts: emptied")
	case r.DryRun:
		fmt.Fprintln(stdout, "sess_fts: would be emptied")
	default:
		fmt.Fprintf(stdout, "sess_fts: %d rows still in it (written by an older binary); run it again\n", r.Remaining)
	}
	switch {
	case r.ChunksFTSEmptied:
		fmt.Fprintf(stdout, "chunks_fts: trigger dropped, %d entries emptied\n", r.ChunksFTSRows)
	case r.DryRun && r.ChunksFTSRows > 0:
		fmt.Fprintf(stdout, "chunks_fts: %d entries would be emptied\n", r.ChunksFTSRows)
	default:
		fmt.Fprintln(stdout, "chunks_fts: already empty")
	}
	if len(r.Optimized) > 0 {
		fmt.Fprintf(stdout, "full-text indexes merged: %v\n", r.Optimized)
	}
	fmt.Fprintf(stdout, "took %s, of which %s holding the write lock\n", r.Duration.Round(time.Millisecond), r.Write.Round(time.Millisecond))
	if !r.DryRun && (r.Moved+r.MovedEmpty > 0 || r.ChunksFTSEmptied) {
		fmt.Fprintln(stdout, "the file shrinks only after VACUUM: stop the server, then run ctx db vacuum --db <db>")
	}
}
