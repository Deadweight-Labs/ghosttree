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
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbUsage = `usage: ctx db vacuum --db <path> [--tmpdir <dir>] [--fts-optimize] [--yes]
       ctx db check --db <path> [--full]
       ctx db backfill-actors --db <path> [--dry-run] [--batch N] [--kind k,...] [--no-models] [--json]
       ctx db chunk-stats --db <path> [--sample N] [--json]
       ctx db slim-sessions --db <path> [--dry-run] [--batch N] [--pause D] [--json]
       ctx db slim-search --db <path> [--dry-run] [--batch N] [--pause D] [--json]
       ctx db slim-text --db <path> [--dry-run] [--restore] [--batch N] [--pause D] [--json]
       ctx db backfill-session-git --db <path> [--dry-run] [--resume] [--batch N] [--pause D] [--json]
       ctx db migrate-owners --db <path> --dry-run|--apply --backup <file> [--plan <file>]

Maintenance of the database file with the SQL functions of the schema. The
sqlite3 CLI cannot do this: the CHECK constraints of the snapshot tables call
ghosttree_valid_utf8, which only ctx registers, so "sqlite3 <db> VACUUM" and
"sqlite3 <db> 'PRAGMA quick_check'" stop with "no such function".

  vacuum  give the free pages back to the filesystem (server stopped)
  check   quick_check or integrity_check, writes nothing (may run beside the server)
  backfill-actors  who wrote existing entries (agent or person), only where proven
  chunk-stats  bytes of stored transcript lines per line type, from a sample
  slim-sessions  slim and pack the stored transcript lines (may run beside the server)
  slim-search    one search index over sessions, no text copies (may run beside the server)
  slim-text      empty session_chunks.text, readers derive it from the stored line (may run beside the server)
  backfill-session-git  tie stored sessions to their commits and pull requests, with a coverage report (may run beside the server)
  migrate-owners   projects to their owners (organization or person), REQ-623`

const dbChunkStatsUsage = `usage: ctx db chunk-stats --db <path> [--sample N] [--json]

Draw N random transcript lines from session_chunks (default 2000) and count the
bytes of their raw JSON per line type (claude:assistant,
codex:event_msg:item_completed:CommandExecution, ...): how many lines, how
large as text, what they take as stored now (packed or not), what the current
slim rule would leave of them, and what of that would be left packed with the
newest dictionary (the state after "ctx db slim-sessions"). Percentages are of
the text. Use it to spot new ballast that a harness started to write.

Each line is one lookup by its number; nothing scans the table, and it pauses
between batches. Nothing is written (query_only), so the server may keep
running. Shares are of the sample, not exact.`

const dbVacuumUsage = `usage: ctx db vacuum --db <path> [--tmpdir <dir>] [--fts-optimize] [--yes]

Give the free pages of the database back to the filesystem: checkpoint the WAL,
VACUUM, checkpoint again, then PRAGMA quick_check. It reports the size of the
file and the WAL before and after, the time it took, and the quick_check result;
the exit status is not 0 unless quick_check says "ok".

Stop the server first (systemctl stop ghosttree). The command refuses, and
changes nothing, when
  - another process has the database, its -wal or its -shm open (looked up in
    /proc on Linux, only for processes it may inspect),
  - another connection holds the write lock (it waits 3 seconds) or a reader
    keeps the first checkpoint from finishing,
  - there is not enough free disk space.

Disk space: VACUUM writes a temporary copy of the live data and, in WAL mode,
the same amount again into the WAL next to the database, so it needs about the
size of the live data (the file minus its free pages, plus a tenth and 64 MiB) in
each place; where both are on one filesystem, twice that. The temporary copy
goes to --tmpdir, by default the directory of the database; SQLite alone would
use SQLITE_TMPDIR, TMPDIR or /var/tmp, which can be too small.

It asks before it starts; --yes skips the question. A ctx without a terminal
needs --yes. Ctrl-C during VACUUM leaves the database unchanged.

--fts-optimize first runs 'optimize' on every FTS5 table (chunks_fts, sess_fts,
knowledge_fts, ...). After rows are deleted or redacted, their tokens stay in the
segments of the full-text index until it is optimized, and VACUUM does not touch
them. It takes extra time and writes through the WAL, which is truncated again
before VACUUM starts.

No schema change: the database is opened without the migrations that
"ctx serve" runs.`

const dbCheckUsage = `usage: ctx db check --db <path> [--full]

Check the database with PRAGMA quick_check, including the CHECK constraints that
call ghosttree_valid_utf8. --full runs integrity_check instead: slower, and it
also verifies that every index matches its table. Prints "ok", or the first
findings, one per line, and exits with 1 if there are any.

Nothing is written to the database (the connection is query_only), so the server
may keep running. Run it as the user that owns the database (see "Running
maintenance commands" in deploy/README.md): SQLite skips the CHECK constraints of
the rows on a connection that is read-only at the file level, and "ok" would not
mean much; the command says so when that happens. A WAL database also needs the
-shm file, which a first connection creates.`

// Austauschbar, damit Tests weder das Terminal noch den freien Platz des
// Rechners brauchen.
var (
	dbTTY         = openDevTTY
	dbFreeBytes   func(dir string) (uint64, error)
	dbLockTimeout time.Duration
)

func cmdDB(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, dbUsage)
		return 2
	}
	switch args[0] {
	case "vacuum":
		return cmdDBVacuum(args[1:], stdout)
	case "check":
		return cmdDBCheck(args[1:], stdout)
	case "backfill-actors":
		return cmdDBBackfillActors(args[1:], stdout)
	case "chunk-stats":
		return cmdDBChunkStats(args[1:], stdout)
	case "slim-sessions":
		return cmdDBSlimSessions(args[1:], stdout)
	case "slim-search":
		return cmdDBSlimSearch(args[1:], stdout)
	case "slim-text":
		return cmdDBSlimText(args[1:], stdout)
	case "backfill-session-git":
		return cmdDBBackfillSessionGit(args[1:], stdout)
	case "migrate-owners":
		return cmdDBMigrateOwners(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, dbUsage)
		return 0
	}
	fmt.Fprintf(stdout, "unknown db command %q\n%s\n", args[0], dbUsage)
	return 2
}

func cmdDBVacuum(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db vacuum", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	tmpDir := fs.String("tmpdir", "", "directory for the temporary copy (default: the directory of the database)")
	fts := fs.Bool("fts-optimize", false, "optimize the FTS5 tables before VACUUM")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Usage = func() { fmt.Fprintln(stdout, dbVacuumUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbVacuumUsage)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	rep, err := store.VacuumDatabase(ctx, *dbPath, store.VacuumOptions{
		TempDir:     *tmpDir,
		FTSOptimize: *fts,
		LockTimeout: dbLockTimeout,
		FreeBytes:   dbFreeBytes,
		Progress: func(step string) {
			fmt.Fprintf(stdout, "[%s] %s\n", time.Since(start).Round(time.Second), step)
		},
		Confirm: func(plan store.VacuumPlan) error {
			printVacuumPlan(stdout, plan, *fts)
			if !*yes {
				tty, err := dbTTY()
				if err != nil {
					fmt.Fprintln(stdout, "This terminal cannot ask questions. Run it again with --yes once the server is stopped.")
					return store.ErrVacuumDeclined
				}
				defer tty.Close()
				if !confirm(ctx, tty, "The server must be stopped. Run VACUUM now? [y/N] ", false) {
					return store.ErrVacuumDeclined
				}
			}
			// Die Zeit im Bericht zählt ab dem Ja, nicht ab dem Start der Frage.
			start = time.Now()
			return nil
		},
	})
	switch {
	case errors.Is(err, store.ErrVacuumDeclined):
		fmt.Fprintln(stdout, "nothing was changed")
		return 1
	case errors.Is(err, store.ErrDatabaseInUse), errors.Is(err, store.ErrNotEnoughSpace):
		fmt.Fprintf(stdout, "refused: %v\n", err)
		return 1
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(stdout, "interrupted: %v\n", err)
		return 1
	case err != nil:
		fmt.Fprintf(stdout, "vacuum failed: %v\n", err)
		if len(rep.QuickCheck) > 0 {
			printCheckFindings(stdout, "quick_check", rep.QuickCheck)
		}
		return 1
	}
	return reportVacuum(stdout, rep)
}

func printVacuumPlan(stdout io.Writer, p store.VacuumPlan, fts bool) {
	fmt.Fprintf(stdout, "database   %s\n", p.Path)
	fmt.Fprintf(stdout, "size       %s, WAL %s (%s of it free pages)\n", humanSize(p.FileBytes), humanSize(p.WALBytes), humanSize(p.FreePages*p.PageSize))
	fmt.Fprintf(stdout, "result     about %s afterwards\n", humanSize(p.LiveBytes))
	fmt.Fprintf(stdout, "temp dir   %s\n", p.TempDir)
	if p.SameFS {
		fmt.Fprintf(stdout, "needs      %s for the temporary copy plus %s for the new WAL, one filesystem, %s free\n",
			humanSize(p.NeedTemp), humanSize(p.NeedDir), humanSize(int64(p.FreeDir)))
	} else {
		fmt.Fprintf(stdout, "needs      %s in %s (%s free) and %s in %s (%s free)\n",
			humanSize(p.NeedTemp), p.TempDir, humanSize(int64(p.FreeTemp)), humanSize(p.NeedDir), p.DBDir, humanSize(int64(p.FreeDir)))
	}
	switch {
	case !p.Scan.Supported:
		fmt.Fprintln(stdout, "in use     write lock is free; open files of other processes are not checked on this platform")
	case p.Scan.Unreadable > 0:
		fmt.Fprintf(stdout, "in use     write lock is free, no inspectable process has the files open (%d processes of other users could not be inspected)\n", p.Scan.Unreadable)
	default:
		fmt.Fprintln(stdout, "in use     write lock is free, no other process has the files open")
	}
	if fts {
		fmt.Fprintf(stdout, "fts        optimize %s\n", strings.Join(p.FTSTables, ", "))
	}
}

func reportVacuum(stdout io.Writer, rep store.VacuumReport) int {
	p := rep.Plan
	code := 0
	fmt.Fprintf(stdout, "VACUUM finished in %s\n", rep.Duration.Round(time.Second))
	fmt.Fprintf(stdout, "database   %s -> %s (saved %s)\n", humanSize(p.FileBytes), humanSize(rep.SizeAfter), humanSize(p.FileBytes-rep.SizeAfter))
	fmt.Fprintf(stdout, "WAL        %s -> %s\n", humanSize(p.WALBytes), humanSize(rep.WALAfter))
	if len(rep.FTSOptimized) > 0 {
		fmt.Fprintf(stdout, "optimized  %s\n", strings.Join(rep.FTSOptimized, ", "))
	}
	if !rep.WALTruncated {
		fmt.Fprintln(stdout, "warning: the checkpoint after VACUUM could not finish because another connection opened the database; the file shrinks at the next checkpoint")
		code = 1
	}
	if rep.QuickCheckOK() {
		fmt.Fprintln(stdout, "quick_check: ok")
	} else {
		printCheckFindings(stdout, "quick_check", rep.QuickCheck)
		code = 1
	}
	return code
}

func cmdDBCheck(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db check", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	full := fs.Bool("full", false, "run integrity_check instead of quick_check")
	fs.Usage = func() { fmt.Fprintln(stdout, dbCheckUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbCheckUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := store.CheckDatabase(ctx, *dbPath, store.CheckOptions{Full: *full})
	if err != nil {
		// Eine zerstörte Seite zeigt sich als Fehler der Abfrage selbst.
		if len(rep.Messages) > 0 {
			printCheckFindings(stdout, rep.Pragma, rep.Messages)
		}
		fmt.Fprintf(stdout, "%s failed: %v\n", rep.Pragma, err)
		return 1
	}
	if rep.OK() {
		fmt.Fprintln(stdout, "ok")
		if rep.ConstraintsSkipped {
			fmt.Fprintln(stdout, "note: the file is not writable for this user, so SQLite skipped the CHECK constraints of the rows; run it as the user that owns the database")
		}
		return 0
	}
	printCheckFindings(stdout, rep.Pragma, rep.Messages)
	return 1
}

func printCheckFindings(stdout io.Writer, pragma string, msgs []string) {
	fmt.Fprintf(stdout, "%s found problems (first findings):\n", pragma)
	for _, m := range msgs {
		fmt.Fprintf(stdout, "  %s\n", m)
	}
}

func cmdDBChunkStats(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db chunk-stats", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	sample := fs.Int("sample", 2000, "number of lines to sample")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbChunkStatsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 || *sample <= 0 || *sample > 1_000_000 {
		fmt.Fprintln(stdout, dbChunkStatsUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := store.ChunkStats(ctx, *dbPath, store.ChunkStatsOptions{Sample: *sample})
	if err != nil {
		fmt.Fprintf(stdout, "chunk-stats failed: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return 1
		}
		return 0
	}
	printChunkStats(stdout, rep)
	return 0
}

func printChunkStats(stdout io.Writer, rep store.ChunkStatsReport) {
	dict := "no dictionary"
	if rep.Dict > 0 {
		dict = fmt.Sprintf("dictionary %d", rep.Dict)
	}
	fmt.Fprintf(stdout, "chunks: at most %d (highest id), sampled %d lines, %s as text, %s stored (%d packed), slim rule version %d, %s, %s\n",
		rep.HighestID, rep.Sampled, humanBytes(int(rep.Bytes)), humanBytes(int(rep.StoredBytes)), rep.Packed, rep.SlimVersion, dict,
		rep.Duration.Round(time.Millisecond))
	if rep.Sampled == 0 {
		return
	}
	fmt.Fprintf(stdout, "%-56s %7s %7s %10s %8s %8s %8s\n", "line type", "text", "lines", "avg bytes", "stored", "slim", "packed")
	pct := func(a, b int64) string {
		if b <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
	}
	for _, k := range rep.Kinds {
		fmt.Fprintf(stdout, "%-56s %6.1f%% %7d %10d %8s %8s %8s\n", k.Kind,
			100*float64(k.Bytes)/float64(rep.Bytes), k.Chunks, k.Bytes/int64(k.Chunks),
			pct(k.StoredBytes, k.Bytes), pct(k.SlimBytes, k.Bytes), pct(k.PackedBytes, k.Bytes))
	}
	fmt.Fprintf(stdout, "stored now: %s of the text; after slim: %s; after slim and packing (what slim-sessions leaves): %s\n",
		pct(rep.StoredBytes, rep.Bytes), pct(rep.SlimBytes, rep.Bytes), pct(rep.PackedBytes, rep.Bytes))
}
