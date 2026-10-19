package main

import (
	"context"
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

const stripImagesUsage = `usage: ctx strip-images --db <path> [--dry-run] [--batch 200] [--sleep 50ms]

Replace the base64 images, PDFs and audio in stored session transcripts with a
short placeholder. New chunks get the same treatment when they are stored; this
is for the ones already in the database:

  "data":"[ghosttree: Bild entfernt, image/png, 1234567 Bytes, sha256:0123456789abcdef]"

The rest of each line stays byte for byte. Search and the session view only use
text, so they do not change. The pixels are gone for good: take a backup first
if you might want them.

It may run while the server is up. It walks session_chunks in id order and
writes in short transactions of --batch rows (at most 64 MiB of raw data held
at a time), pausing --sleep between them, and waits up to 30 seconds for a
locked database. Ctrl-C stops between two transactions; run it again to
continue, a second run finds nothing left to change. --dry-run writes nothing
and only counts.

The file does not get smaller by itself: the freed pages are reused by new data
and given back to the filesystem by VACUUM, which needs the server stopped and
free disk space of about the size of the database.`

func cmdStripImages(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("strip-images", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count what would be replaced without writing")
	batch := fs.Int("batch", 200, "chunks per transaction")
	sleep := fs.Duration("sleep", 50*time.Millisecond, "pause between transactions")
	fs.Usage = func() { fmt.Fprintln(stdout, stripImagesUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *batch <= 0 || *sleep < 0 || fs.NArg() > 0 {
		fmt.Fprintln(stdout, stripImagesUsage)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dryRun {
		fmt.Fprintln(stdout, "dry run: nothing is written")
	}
	lastLine := time.Now()
	rep, err := store.StripSessionBlobs(ctx, *dbPath, store.StripBlobsOptions{
		DryRun: *dryRun,
		Batch:  *batch,
		Sleep:  *sleep,
		Progress: func(r store.StripBlobsReport) {
			if time.Since(lastLine) < 2*time.Second {
				return
			}
			lastLine = time.Now()
			fmt.Fprintf(stdout, "  up to chunk id %d: %d changed, %s saved\n", r.LastID, r.Changed, humanSize(r.SavedBytes))
		},
	})
	interrupted := errors.Is(err, context.Canceled)
	if err != nil && !interrupted {
		fmt.Fprintf(stdout, "strip-images failed: %v\n", err)
		reportStripImages(stdout, rep, *dryRun, false)
		return 1
	}
	reportStripImages(stdout, rep, *dryRun, interrupted)
	if interrupted {
		return 1
	}
	return 0
}

func reportStripImages(stdout io.Writer, r store.StripBlobsReport, dryRun, interrupted bool) {
	if interrupted {
		fmt.Fprintf(stdout, "interrupted after chunk id %d; run it again to continue\n", r.LastID)
	}
	verb := "replaced"
	if dryRun {
		verb = "would replace"
	}
	fmt.Fprintf(stdout, "checked %d chunks that mention base64 or a media type (of %d chunks in the table)\n", r.Candidates, r.Chunks)
	fmt.Fprintf(stdout, "chunks with embedded files %s: %d\n", verb, r.Changed)
	fmt.Fprintf(stdout, "bytes saved: %d (%s)\n", r.SavedBytes, humanSize(r.SavedBytes))
	if !dryRun && r.Changed > 0 {
		fmt.Fprintln(stdout, "the file shrinks only after VACUUM: stop the server, then run sqlite3 <db> VACUUM")
	}
}

// humanSize schreibt Bytes mit Binärpräfix.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
