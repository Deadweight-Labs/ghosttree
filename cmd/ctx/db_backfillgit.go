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

	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbBackfillGitUsage = `usage: ctx db backfill-session-git --db <path> [--dry-run] [--resume] [--after-id N]
                                     [--batch N] [--match-batch N] [--pause D] [--json]

Tie the sessions already in the database to the commits and pull requests they
produced (IDEA-31). The collector recognises git commit, git push, gh pr create
and gh pr merge in new transcript lines as they are written; this command does
the same for the lines already stored, and then runs the match that the server
runs after a pull request sync:

  1. It reads session_chunks session by session, in id order, with the same
     recognition as the collector (the command at the start of a simple shell
     command, paired with its result; a mention in a prompt is not a call). It
     reads the stored line (raw), not the text column, which is empty or cut.
  2. It writes what POST /api/session-git-events writes: the git events with
     their time window, the hashes a commit printed (source reported) and the
     pull requests a session created or merged. No reflog: the server has no
     repository; observed hashes only come from a live collector.
  3. It checks the hashes against the commits of the pull requests, and lays the
     time window of every commit call without a hash over them (source matched),
     under the author rules of the server (a commit by another known person is
     not assigned; an author who cannot be checked gives a weak assignment).
  4. It prints the coverage: how many commit calls, pull requests and commits of
     pull requests have a session now, split by reported, observed and matched.

Only the three small tables session_git_events, session_commits and session_prs
are written, in short transactions (--batch events, --match-batch commits, and a
pause at least as long as the write lock was held). It may run while the server is
up; the server must already run the version that brings the tables. Reading takes
no lock. A second run writes nothing new; an interrupted run may be started again,
with --resume (continues behind the last session it finished) or --after-id.

--dry-run writes nothing to the file: it runs the same steps against temporary
copies of the three tables and prints the coverage the real run would leave. Run it
first; on a large database it takes as long as the real run, minus the pauses.
Only numbers are printed.`

func cmdDBBackfillSessionGit(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db backfill-session-git", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "run against temporary copies of the tables; write nothing")
	resume := fs.Bool("resume", false, "continue behind the last session the previous run finished")
	afterID := fs.Int64("after-id", 0, "continue behind this session id")
	batch := fs.Int("batch", 100, "git events per write transaction")
	matchBatch := fs.Int("match-batch", 20, "commits or reported hashes per transaction of the match")
	pause := fs.Duration("pause", 20*time.Millisecond, "least pause between transactions")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbBackfillGitUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *matchBatch <= 0 || *pause < 0 || *afterID < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, dbBackfillGitUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun && !*asJSON {
		fmt.Fprintln(stdout, "dry run: nothing is written to the file")
	}
	lastLine := time.Now()
	rep, err := store.BackfillSessionGit(ctx, *dbPath, store.BackfillGitOptions{
		DryRun:     *dryRun,
		Resume:     *resume,
		AfterID:    *afterID,
		Batch:      *batch,
		MatchBatch: *matchBatch,
		Pause:      *pause,
		Scan: func(harness, externalID string) store.GitLineScanner {
			return collector.NewHistoryScan(harness, externalID)
		},
		Progress: func(r store.BackfillGitReport) {
			if *asJSON || time.Since(lastLine) < 5*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to session id %d: %d sessions, %d lines, %d events found, %s\n",
				r.LastID, r.Sessions, r.Chunks, foundTotal(r), r.Duration.Round(time.Second))
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		if err != nil && !interrupted {
			fmt.Fprintf(stdout, "backfill-session-git failed: %v\n", err)
		}
		reportBackfillGit(stdout, rep, interrupted)
	}
	if err != nil {
		return 1
	}
	return 0
}

func foundTotal(r store.BackfillGitReport) int64 {
	var n int64
	for _, c := range r.Found {
		n += c
	}
	return n
}

func reportBackfillGit(stdout io.Writer, r store.BackfillGitReport, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted behind session id %d; run it again with --resume (or --after-id %d) to continue\n", r.LastID, r.LastID)
		fmt.Fprintln(stdout, "the coverage below is not printed for an interrupted run")
	}
	fmt.Fprintf(stdout, "read %d sessions (%d lines), %d could not be read\n", r.Sessions, r.Chunks, r.SessionsFailed)
	if r.FirstFailure != "" {
		fmt.Fprintf(stdout, "  first: %s\n", r.FirstFailure)
	}
	fmt.Fprintf(stdout, "git events found: commit %d, push %d, pr_create %d, pr_merge %d\n",
		r.Found["commit"], r.Found["push"], r.Found["pr_create"], r.Found["pr_merge"])
	verb := "written"
	if r.DryRun {
		verb = "that would be written"
	}
	fmt.Fprintf(stdout, "  new %s: %d events, %d hashes printed by commits (reported); already known: %d\n", verb, r.Recorded, r.ReportedAdded, r.Known)
	if r.Rejected > 0 || r.Capped > 0 {
		fmt.Fprintf(stdout, "  not taken: %d invalid, %d over the per-session limit\n", r.Rejected, r.Capped)
	}
	fmt.Fprintf(stdout, "match: %d hashes checked, %d commit calls without a hash laid over the pull requests, %d commits assigned (matched)\n",
		r.ClaimsChecked, r.EventsMatched, r.CommitsMatched)
	if interrupted {
		reportBackfillLocks(stdout, r)
		return
	}
	c := r.Coverage
	fmt.Fprintf(stdout, "\ncoverage of the whole database (%d sessions, %d with git calls)\n", c.Sessions, c.SessionsWithCalls)
	coverageLine(stdout, "commit calls", c.CommitCalls)
	coverageLine(stdout, "pull requests", c.PRsBy)
	fmt.Fprintf(stdout, "    of which merged: %d of %d with sessions\n", c.MergedBy.With(), c.MergedPRs)
	coverageLine(stdout, "commits in PRs", c.CommitsBy)
	if c.ShortReported > 0 {
		fmt.Fprintf(stdout, "  %d reported hashes are still short: no pull request carries the commit yet\n", c.ShortReported)
	}
	reportBackfillLocks(stdout, r)
}

func coverageLine(stdout io.Writer, what string, b store.BySource) {
	total := b.With() + b.None
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(b.With()) / float64(total)
	}
	fmt.Fprintf(stdout, "  %-15s %d of %d have a session (%.1f%%): reported %d, observed %d, matched %d (%d weak), none %d\n",
		what, b.With(), total, pct, b.Reported, b.Observed, b.Matched, b.MatchedWeak, b.None)
}

func reportBackfillLocks(stdout io.Writer, r store.BackfillGitReport) {
	if r.Batches > 0 {
		fmt.Fprintf(stdout, "write lock per transaction: p50 %s, p95 %s, max %s (%d transactions)\n",
			r.LockP50.Round(10*time.Microsecond), r.LockP95.Round(10*time.Microsecond), r.LockMax.Round(10*time.Microsecond), r.Batches)
	}
	fmt.Fprintf(stdout, "took %s (read %s, match %s, count %s), of which %s holding the write lock\n", r.Duration.Round(time.Millisecond),
		r.ScanTook.Round(time.Millisecond), r.MatchTook.Round(time.Millisecond), r.CoverageTook.Round(time.Millisecond), r.Write.Round(time.Millisecond))
}
