package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const redactSessionsUsage = `usage: ctx redact-sessions --db <path> [--dry-run] [--batch 200] [--sleep 20ms] [--after-id N] [--skip-optimize]

Replace passwords and other secrets in the stored session transcripts with
[REDACTED:labeled], [REDACTED:url] and the like. New chunks get the same
treatment when they are stored; this is for the ones already in the database.
What is replaced is the value behind a label, the label stays:

  Passwort: Hx7!kQ2pz        ->  Passwort: [REDACTED:labeled]
  api_key=k7k7k7k7m2m2m2m2   ->  api_key=[REDACTED:labeled]
  https://u:pw@host/x        ->  https://u:[REDACTED:url]@host/x

plus the tokens with a fixed prefix (ghp_, sk-ant-, AKIA, eyJ..., private keys).

Every copy of the text is cleaned, not only session_chunks.text and .raw: the
full-text index chunks_fts, the search index of the session view (chunk_index and
sess_fts), the session title, and the quotes of knowledge evidence and request
sightings. Knowledge entries, requests, documents and messages are not touched
(they hold what an agent wrote, not the transcript); the report lists the
knowledge entries and requests whose evidence comes from a changed chunk, so you
can look at them.

It may run while the server is up. It walks session_chunks in id order and
writes in short transactions of --batch rows (at most 64 MiB held at a time),
pausing --sleep between them, and waits up to 30 seconds for a locked database.
Ctrl-C stops between two transactions; run it again to continue (or start at
--after-id with the id the report names). A second run finds nothing left to
change. --dry-run writes nothing and only counts. The report holds numbers and
names, never a value.

At the end both full-text indexes are merged into one segment (FTS5 optimize),
because removing an entry only marks it as deleted and the words stay in the
index segments until they are merged. That is a single statement that rewrites
each index: it holds the write lock for as long as it takes, which on a large
database can be minutes, and needs free disk space of about the size of the
index. --skip-optimize leaves it out; a later run without it does it. The write-
ahead log is truncated afterwards.

The run overwrites what it frees (secure_delete). Pages that were free before
the run, from older updates, still hold old content, and so do backups: the
database file is clean only after VACUUM (ctx db vacuum, or with the server
stopped: sqlite3 <db> VACUUM), which needs free disk space of about the size of
the database.`

func cmdRedactSessions(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("redact-sessions", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count what would be replaced without writing")
	batch := fs.Int("batch", 200, "chunks per transaction")
	sleep := fs.Duration("sleep", 20*time.Millisecond, "pause between transactions")
	after := fs.Int64("after-id", 0, "continue behind this chunk id")
	skipOptimize := fs.Bool("skip-optimize", false, "do not merge the full-text indexes at the end")
	fs.Usage = func() { fmt.Fprintln(stdout, redactSessionsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *sleep < 0 || *after < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, redactSessionsUsage)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	lastLine := time.Now()
	rep, err := store.RedactSessions(ctx, *dbPath, store.RedactSessionsOptions{
		DryRun:       *dryRun,
		Batch:        *batch,
		Sleep:        *sleep,
		AfterID:      *after,
		SkipOptimize: *skipOptimize,
		Progress: func(r store.RedactSessionsReport) {
			if time.Since(lastLine) < 2*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to chunk id %d: %d of %d checked, %d changed\n", r.LastID, r.Scanned, r.Chunks, r.Changed)
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if err != nil && !interrupted {
		fmt.Fprintf(stdout, "redact-sessions failed: %v\n", err)
		reportRedactSessions(stdout, rep, *dbPath, *dryRun, false)
		return 1
	}
	reportRedactSessions(stdout, rep, *dbPath, *dryRun, interrupted)
	if interrupted {
		return 1
	}
	return 0
}

func reportRedactSessions(stdout io.Writer, r store.RedactSessionsReport, db string, dryRun, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted after chunk id %d; run it again to continue (--after-id %d)\n", r.LastID, r.LastID)
	}
	verb := "replaced"
	if dryRun {
		verb = "would replace"
	}
	fmt.Fprintf(stdout, "checked %d chunks (of %d in the table)\n", r.Scanned, r.Chunks)
	fmt.Fprintf(stdout, "chunks with secrets %s: %d\n", verb, r.Changed)
	if len(r.Tables) > 0 {
		fmt.Fprintln(stdout, "cleaned per table (rows or entries):")
		for _, k := range sortedKeys(r.Tables) {
			fmt.Fprintf(stdout, "  %-26s %d\n", k, r.Tables[k])
		}
	}
	if len(r.Kinds) > 0 {
		fmt.Fprintln(stdout, "replacements per field and kind:")
		for _, k := range sortedKeys(r.Kinds) {
			fmt.Fprintf(stdout, "  %-26s %d\n", k, r.Kinds[k])
		}
	}
	for _, c := range []struct {
		what string
		ids  []int64
	}{{"knowledge entries", r.CitedKnowledge}, {"requests", r.CitedRequests}} {
		if len(c.ids) == 0 {
			continue
		}
		ids := make([]string, 0, len(c.ids))
		for i, id := range c.ids {
			if i == 20 {
				ids = append(ids, fmt.Sprintf("... (%d in all)", len(c.ids)))
				break
			}
			ids = append(ids, fmt.Sprint(id))
		}
		fmt.Fprintf(stdout, "%s with evidence from a changed chunk (their text is not touched, look at them): %s\n", c.what, strings.Join(ids, " "))
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(stdout, "not in this database, skipped: %s\n", strings.Join(r.Skipped, ", "))
	}
	if dryRun {
		return
	}
	if len(r.Optimized) > 0 {
		fmt.Fprintf(stdout, "full-text indexes merged: %s\n", strings.Join(r.Optimized, ", "))
	} else if !interrupted {
		fmt.Fprintln(stdout, "full-text indexes NOT merged: the removed words are still in their segments; run again without --skip-optimize")
	}
	if r.Checkpoint != "" {
		fmt.Fprintf(stdout, "write-ahead log: %s\n", r.Checkpoint)
	}
	if r.Changed > 0 {
		fmt.Fprintf(stdout, "the file still holds old pages from before this run: run ctx db vacuum (or stop the server, then sqlite3 %s VACUUM). Backups made before this run still hold the secrets\n", db)
	}
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
