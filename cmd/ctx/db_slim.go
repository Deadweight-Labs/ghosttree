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
	"sort"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbSlimSessionsUsage = `usage: ctx db slim-sessions --db <path> [--dry-run] [--batch N] [--pause D]
                             [--after-id N] [--new-dict] [--skip-optimize] [--json]

Bring the stored transcripts to the form new lines are stored in (REQ-622):
every line of session_chunks goes the way AppendChunks takes for a new one
(images replaced, secrets redacted, copies and ballast dropped by the slim
rule) and is then packed with deflate and a trained dictionary. Lines the rule
drops entirely are deleted together with their rows in chunk_index, sess_fts,
chunks_fts and their session vectors; a line a knowledge or request quote cites
stays. The text column is cut like long outputs (6 KB head, marker, 2 KB tail),
except in sessions with an open distillation batch.

Without a dictionary (or with --new-dict) it first learns one from a random
sample of the database and stores it as the next version; dictionaries are
never deleted. Lines already at the current slim version are skipped, so a
second run finds nothing to do and an interrupted run may simply be started
again (or continued with --after-id).

It may run while the server is up; the server must already run the version
that brings this command, since it creates the dictionary table and the
slim_version column. It walks session_chunks in id order and writes in short
transactions of --batch lines, then pauses at least as long as the transaction
held the write lock (and never less than --pause), so the server's writer gets
at least half the time. --dry-run writes nothing, not even the dictionary, and
reports what would happen.

At the end both full-text indexes are merged with 'optimize' (deleted words
otherwise stay in their segments). That is one statement that holds the write
lock for its whole duration, on a large database minutes. --skip-optimize
leaves it out; "ctx db vacuum --fts-optimize" does it with the server stopped.

The file does not shrink by itself. Take a backup before, and afterwards stop
the server and run "ctx db vacuum --db <path>".`

func cmdDBSlimSessions(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db slim-sessions", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "report what would change without writing")
	batch := fs.Int("batch", 200, "lines per transaction")
	pause := fs.Duration("pause", 20*time.Millisecond, "least pause between transactions")
	afterID := fs.Int64("after-id", 0, "continue after this chunk id")
	newDict := fs.Bool("new-dict", false, "learn a new dictionary even if one exists")
	skipOptimize := fs.Bool("skip-optimize", false, "do not merge the full-text indexes at the end")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbSlimSessionsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *pause < 0 || *afterID < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbSlimSessionsUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun && !*asJSON {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	lastLine := time.Now()
	rep, err := store.SlimSessions(ctx, *dbPath, store.SlimSessionsOptions{
		DryRun:       *dryRun,
		Batch:        *batch,
		Pause:        *pause,
		AfterID:      *afterID,
		NewDict:      *newDict,
		SkipOptimize: *skipOptimize,
		Progress: func(r store.SlimSessionsReport) {
			if *asJSON || time.Since(lastLine) < 5*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to chunk id %d: %d lines, raw %s -> %s, %d deleted, %s\n", r.LastID, r.Scanned,
				humanSize(r.RawBefore), humanSize(r.RawAfter), r.Deleted, r.Duration.Round(time.Second))
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		if err != nil && !interrupted {
			fmt.Fprintf(stdout, "slim-sessions failed: %v\n", err)
		}
		reportSlimSessions(stdout, rep, interrupted)
	}
	if err != nil {
		return 1
	}
	return 0
}

func reportSlimSessions(stdout io.Writer, r store.SlimSessionsReport, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted after chunk id %d; run it again to continue\n", r.LastID)
	}
	would := ""
	if r.DryRun {
		would = "would be "
	}
	switch {
	case r.DictTrained && r.DryRun:
		fmt.Fprintf(stdout, "dictionary: would learn version %d (%s from %d sample lines)\n", r.Dict.Version, humanSize(int64(r.Dict.Bytes)), r.Dict.SampleLines)
	case r.DictTrained:
		fmt.Fprintf(stdout, "dictionary: learned version %d (%s from %d sample lines)\n", r.Dict.Version, humanSize(int64(r.Dict.Bytes)), r.Dict.SampleLines)
	case r.Dict.Version > 0:
		fmt.Fprintf(stdout, "dictionary: version %d\n", r.Dict.Version)
	default:
		fmt.Fprintln(stdout, "dictionary: none (too few lines to learn one), packed without")
	}
	fmt.Fprintf(stdout, "lines: %d in the table, %d already current, %d %sprocessed\n", r.Chunks, r.Current, r.Scanned, would)
	fmt.Fprintf(stdout, "  slimmed %d, packed %d, deleted %d, kept because cited %d\n", r.Slimmed, r.Packed, r.Deleted, r.KeptCited)
	fmt.Fprintf(stdout, "raw:  %s stored -> %s (%s)\n", humanSize(r.RawBefore), humanSize(r.RawAfter), percentOf(r.RawAfter, r.RawBefore))
	fmt.Fprintf(stdout, "      %s as text -> %s after the slim rule\n", humanSize(r.LineBefore), humanSize(r.LineAfter))
	fmt.Fprintf(stdout, "text: %s -> %s, %d cut", humanSize(r.TextBefore), humanSize(r.TextAfter), r.TextCut)
	if r.TextKeptOpenBatch > 0 {
		fmt.Fprintf(stdout, ", %d left whole (open distillation batch)", r.TextKeptOpenBatch)
	}
	fmt.Fprintln(stdout)
	if len(r.Tables) > 0 {
		keys := make([]string, 0, len(r.Tables))
		for k := range r.Tables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprint(stdout, "derived rows changed or deleted:")
		for _, k := range keys {
			fmt.Fprintf(stdout, " %s %d", k, r.Tables[k])
		}
		fmt.Fprintln(stdout)
	}
	if len(r.Optimized) > 0 {
		fmt.Fprintf(stdout, "full-text indexes merged: %v\n", r.Optimized)
	}
	fmt.Fprintf(stdout, "took %s, of which %s holding the write lock\n", r.Duration.Round(time.Millisecond), r.Write.Round(time.Millisecond))
	if !r.DryRun && r.Scanned > 0 {
		fmt.Fprintln(stdout, "the file shrinks only after VACUUM: stop the server, then run ctx db vacuum --db <db>")
	}
}

func percentOf(a, b int64) string {
	if b <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}
