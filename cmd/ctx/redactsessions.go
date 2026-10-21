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

	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const redactSessionsUsage = `usage: ctx redact-sessions --db <path> [--dry-run] [--batch 200] [--sleep 20ms] [--after-id N] [--skip-optimize] [--literals-file <path>]

Replace passwords and other secrets in the stored session transcripts with
[REDACTED:labeled], [REDACTED:url] and the like. New chunks get the same
treatment when they are stored; this is for the ones already in the database.
What is replaced is the value behind a label, the label stays:

  Passwort: Hx7!kQ2pz        ->  Passwort: [REDACTED:labeled]
  api_key=k7k7k7k7m2m2m2m2   ->  api_key=[REDACTED:labeled]
  https://u:pw@host/x        ->  https://u:[REDACTED:url]@host/x

plus the tokens with a fixed prefix (ghp_, sk-ant-, AKIA, eyJ..., private keys).

A value that was leaked and is known, but stands without a label (in a command
line, in the output of grep, as a fragment), is not found that way. Name it with
--literals-file: one value per line, blank lines and lines that start with # are
ignored (a value that starts with # is written \#), spaces around a value are
dropped. Every occurrence of every value is replaced by [REDACTED:literal], with
or without a label, in the same places as above. In raw it is found as it is
written there, also as a JSON escape (\u0026 for &, \" for a quote), and raw
stays valid JSON. A value matches in the spelling given and in lower case (the
full-text columns do not tell the cases apart, and fragments of a value turn up
in lower case in command output); other spellings do not match. Values shorter
than 4 characters are refused, so that nobody empties the database by mistake
(exit 2). The file is a secret itself: it must not be accessible to group or
others (chmod 600), otherwise the command stops (exit 2). A value never appears
in the output or in an error message, and is never given to SQL; only the number
of values and of replacements is shown. Without the switch none of this happens.
Texts that people and agents wrote (knowledge, its versions, requests) are not
changed: they have history, search copies and snapshots that this command cannot
clean as a whole. They are counted, with the numbers of the entries, so that you
can look at them.

Every copy of the text is cleaned, not only session_chunks.text and .raw: the
full-text index chunks_fts, the search index of the session view (chunk_index and
sess_fts), the session title, and the quotes of knowledge evidence and request
sightings. The vector of the session search (REQ-538) of a chunk that is changed
was made from the text before and is dropped in the same transaction; the server
makes it again from the text as it is now (counted as session_embeddings). Knowledge entries, requests, documents and messages are not touched
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
database file is clean only after "ctx db vacuum --db <path>" with the server
stopped, which needs free disk space of about the size of the database. The
sqlite3 CLI cannot do it: the schema needs an SQL function only ctx registers.`

func cmdRedactSessions(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("redact-sessions", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count what would be replaced without writing")
	batch := fs.Int("batch", 200, "chunks per transaction")
	sleep := fs.Duration("sleep", 20*time.Millisecond, "pause between transactions")
	after := fs.Int64("after-id", 0, "continue behind this chunk id")
	skipOptimize := fs.Bool("skip-optimize", false, "do not merge the full-text indexes at the end")
	literalsFile := fs.String("literals-file", "", "file with one known value per line to replace everywhere")
	fs.Usage = func() { fmt.Fprintln(stdout, redactSessionsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *sleep < 0 || *after < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, redactSessionsUsage)
		return 2
	}
	// Before the database is touched: a list that is refused must not cost a run.
	var literals *redact.Literals
	if *literalsFile != "" {
		l, err := readLiteralsFile(*literalsFile)
		if err != nil {
			fmt.Fprintf(stdout, "redact-sessions: --literals-file: %v\n", err)
			return 2
		}
		literals = l
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	if literals != nil {
		fmt.Fprintf(stdout, "known values: %d (the values are never printed)\n", literals.Len())
	}
	lastLine := time.Now()
	rep, err := store.RedactSessions(ctx, *dbPath, store.RedactSessionsOptions{
		DryRun:       *dryRun,
		Batch:        *batch,
		Sleep:        *sleep,
		AfterID:      *after,
		SkipOptimize: *skipOptimize,
		Literals:     literals,
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
		fmt.Fprintf(stdout, "%s with evidence from a changed chunk (their text is not touched, look at them): %s\n", c.what, joinIDs(c.ids))
	}
	// Holders is nil without --literals-file and an empty map when nothing was found.
	if r.Holders != nil {
		if len(r.Holders) == 0 {
			fmt.Fprintln(stdout, "texts written by people and agents that hold a known value: none")
		} else {
			fmt.Fprintln(stdout, "texts written by people and agents that hold a known value (counted, NOT changed: history, search copies and snapshots hold them too; look at them):")
			for _, k := range sortedHolderKeys(r.Holders) {
				h := r.Holders[k]
				fmt.Fprintf(stdout, "  %-26s %d rows, entries: %s\n", k, h.Rows, joinIDs(h.Owners))
			}
		}
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
		fmt.Fprintf(stdout, "the file still holds old pages from before this run: stop the server, then run ctx db vacuum --db %s. Backups made before this run still hold the secrets\n", db)
	}
}

// joinIDs lists the first 20 numbers and says how many there are in all.
func joinIDs(ids []int64) string {
	out := make([]string, 0, len(ids))
	for i, id := range ids {
		if i == 20 {
			out = append(out, fmt.Sprintf("... (%d in all)", len(ids)))
			break
		}
		out = append(out, fmt.Sprint(id))
	}
	return strings.Join(out, " ")
}

func sortedHolderKeys(m map[string]store.RedactHolders) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readLiteralsFile reads the known values. The file is a secret itself, so it
// is refused when group or others can reach it (the mode is read from the open
// file, not from the path). No error holds a value, only the path, the mode or
// a line number.
func readLiteralsFile(path string) (*redact.Literals, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s has mode %04o, but it holds secrets and must not be accessible to group or others; run: chmod 600 %s", path, perm, path)
	}
	return redact.ParseLiterals(f)
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
