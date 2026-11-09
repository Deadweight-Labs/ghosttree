package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbBackfillActorsUsage = `usage: ctx db backfill-actors --db <path> [--dry-run] [--batch N] [--kind k,...] [--no-models] [--json]

Stamp who wrote the entries that were written before the server recorded it
(REQ-620): a person in the web interface or an agent, and for an agent its
harness, session and machine. Only what can be proven is filled:

  - a web origin is a person (origin "human" alone is not proof)
  - a session that belongs only to the writer gives harness, session and machine
  - room messages and votes are what the server recorded by route
  - a pain or idea gets the writer's session that called pain_record/idea_record
    (or ctx pain/idea add) within two minutes, by the times of its lines
  - requests and knowledge with origin agent are agents; knowledge keeps its harness
  - distilled entries and everything else stay unknown
  - entries younger than five minutes are left to the live stamp (recent)

Afterwards the model history of every session an actor points to is read from
the stored transcript lines (--no-models skips this).

Runs in batches (--batch, default 500 entries per transaction) and may run
beside the server. Idempotent: a second run only reads. --dry-run counts and
writes nothing. --kind limits the run (request, knowledge, pain, idea,
criterion, request_done, task, comment, feature, message, vote).`

func cmdDBBackfillActors(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db backfill-actors", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	dryRun := fs.Bool("dry-run", false, "count what would be filled, write nothing")
	batch := fs.Int("batch", 500, "entries per transaction")
	kinds := fs.String("kind", "", "comma-separated kinds to fill (default all)")
	noModels := fs.Bool("no-models", false, "do not derive the model history of sessions")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { fmt.Fprintln(stdout, dbBackfillActorsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 || *batch <= 0 {
		fmt.Fprintln(stdout, dbBackfillActorsUsage)
		return 2
	}
	opts := store.ActorBackfillOptions{DryRun: *dryRun, Batch: *batch, Models: !*noModels}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			opts.Kinds = append(opts.Kinds, k)
		}
	}
	s, err := store.OpenWithOptions(*dbPath, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer s.Close()
	rep, err := s.BackfillActors(opts)
	if err != nil {
		fmt.Fprintf(stdout, "backfill failed: %v\n", err)
		return 1
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(raw))
		return 0
	}
	if rep.DryRun {
		fmt.Fprintln(stdout, "dry run: nothing was written")
	}
	fmt.Fprintf(stdout, "%-13s %10s %8s %8s %13s %8s %7s\n", "kind", "candidates", "person", "agent", "with session", "unknown", "recent")
	for _, k := range rep.Kinds {
		fmt.Fprintf(stdout, "%-13s %10d %8d %8d %13d %8d %7d\n", k.Kind, k.Candidates, k.Human, k.Agent, k.WithSession, k.Unknown, k.Recent)
	}
	if opts.Models {
		fmt.Fprintf(stdout, "model history: %d sessions, %d stretches\n", rep.Sessions, rep.Segments)
	}
	return 0
}
