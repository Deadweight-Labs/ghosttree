package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/eval/searchquality"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const evalUsage = `usage: ctx eval search --db <path> [--sample 1500] [--seed 20261020] [--min-chars 30]
                       [--embed-url <url>] [--embed-query-url <url>] [--embed-model <id>]
                       [--embed-min-similarity 0.55] [--embed-session-weight 2] [--pause 50ms]
                       [--set <file.jsonl>] [--requests-builtin]
                       [--sessions-builtin] [--sessions-sample 300]

With --set, --requests-builtin or --sessions-builtin the run measures the search of
the other kinds instead, on queries whose right answer is known:

  --set <file>        a query set, one JSON object per line:
                      {"query": "...", "kind": "request|ghost|pain|document|knowledge|session",
                       "target": "..."}
                      target is the id of the right object; for a ghost
                      description "project:path", for a session its number or
                      the address of the web view. Each query runs through the
                      production search of its kind, scoped to the project of
                      its target (as an agent asks; sessions are searched in
                      the whole instance, a session of any project may be the
                      one), as the instance owner sees it. Answers per kind: n,
                      R@1, R@3, R@10 and MRR@10 for the full-text, the embedding
                      and the fused ranking. No query and no text of an object
                      is printed.
  --requests-builtin  the queries the request ledger holds the answer to: the
                      quotes of request sightings, and the title of a request
                      marked as a duplicate of, or as superseding, another with
                      the other as the answer (--sample of each, --min-chars
                      for the quotes). Both can be given; they are measured
                      together, per block.
  --sessions-builtin  the queries the knowledge holds the answer to for the
                      session search: the title of an active entry with evidence
                      (at least 15 characters), and any session of its evidence
                      as the answer (--sessions-sample of them, drawn with
                      --seed; with the defaults the draw of the experiment the
                      search of sessions was decided on). Titles are summaries
                      written by a model, not questions; questions go in a
                      --set file. The fused ranking uses --embed-session-weight
                      (env GHOSTTREE_EMBED_SESSION_WEIGHT) for the vectors.
                      Measured together with the other sets, per block.

Without them, the run measures the knowledge search on evidence quotes:

Measure how well the knowledge search finds an entry from a quote of the
transcript it was distilled from. The queries are the evidence quotes
(knowledge_evidence.quote) of at least --min-chars characters that back active
entries, drawn at random with --seed; the target of a query is the entry the
quote backs. Each query runs through the search function of the server, once
with the full-text ranking alone, once with the embedding ranking alone and once
with both fused (the last two only with --embed-url). Answers: n, R@1, R@3, R@10
and MRR@10 per ranking, for all targets and for the targets that are not
quarantined. Nothing else is printed: no quote and no text of an entry, because
they can contain secrets.

The full-text row alone is the "before", the hybrid row the "after".
--embed-min-similarity sets the cosine an entry needs to enter the embedding
ranking, as the server would be configured; run the measurement with several
values to see what a threshold costs and what it saves.

--embed-query-url names the second server that embeds the queries, as in ctx
serve (--embed-url is needed too: the stored vectors are the job server's). The
run first checks that both serve the same model and stops if they do not, since
the numbers would mean nothing; if a server does not answer that check, it says
so and goes on.

The database is opened read-only, without migrations, and the server may keep
running. The stored vectors are read; only the queries are sent to the embedding
server, one at a time, --pause apart, with a progress line every ten seconds on
stderr. Run "ctx embed status" first: while entries still wait for their vector
the vector and hybrid rows understate the result.`

// evalProgress is where progress lines go; a test replaces it.
var evalProgress io.Writer = os.Stderr

func cmdEval(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, evalUsage)
		return 2
	}
	switch args[0] {
	case "search":
		return cmdEvalSearch(args[1:], stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, evalUsage)
		return 0
	}
	fmt.Fprintf(stdout, "unknown eval command %q\n%s\n", args[0], evalUsage)
	return 2
}

func cmdEvalSearch(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("eval search", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	sample := fs.Int("sample", 1500, "number of queries drawn from the evidence quotes")
	seed := fs.Int64("seed", 20261020, "seed of the draw")
	minChars := fs.Int("min-chars", 30, "shortest quote that counts as a query")
	pause := fs.Duration("pause", 50*time.Millisecond, "wait between two query embeddings")
	setFile := fs.String("set", "", "a query set (JSON lines: query, kind, target) to measure the search of the other kinds on")
	requestsBuiltin := fs.Bool("requests-builtin", false, "measure the request search on the queries the ledger holds the answer to")
	sessionsBuiltin := fs.Bool("sessions-builtin", false, "measure the session search on the titles of knowledge entries, the sessions of their evidence being the answers")
	sessionsSample := fs.Int("sessions-sample", 300, "number of titles drawn for --sessions-builtin")
	embedFlagSet := addEmbedFlags(fs, false)
	fs.Usage = func() { fmt.Fprintln(stdout, evalUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 || *sample <= 0 || *minChars <= 0 || *pause < 0 || *sessionsSample <= 0 {
		fmt.Fprintln(stdout, evalUsage)
		return 2
	}
	var set []searchquality.SetQuery
	if *setFile != "" {
		f, err := os.Open(*setFile)
		if err != nil {
			fmt.Fprintf(stdout, "eval search: %v\n", err)
			return 1
		}
		set, err = searchquality.ParseSet(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(stdout, "eval search: %s: %v\n", *setFile, err)
			return 1
		}
	}
	cfg, err := embedFlagSet.resolve()
	if err != nil {
		fmt.Fprintf(stdout, "eval search: %v\n", err)
		return 2
	}
	opts := searchquality.Options{Sample: *sample, Seed: *seed, MinChars: *minChars, Pause: *pause, MinSimilarity: cfg.MinSimilarity, SessionWeight: cfg.SessionWeight, Progress: evalProgress}
	if cfg.enabled() {
		c, err := cfg.client()
		if err != nil {
			fmt.Fprintf(stdout, "eval search: %v\n", err)
			return 2
		}
		opts.Embedder = c
		if cfg.separateQuery() {
			qc, err := cfg.queryClient()
			if err != nil {
				fmt.Fprintf(stdout, "eval search: %v\n", err)
				return 2
			}
			opts.Embedder = qc
			var diff *embed.DisagreementError
			switch _, err := verifyServers(cfg); {
			case errors.As(err, &diff):
				fmt.Fprintf(stdout, "eval search: the query server and the job server do not serve the same model, so the numbers would mean nothing: %v\n", diff)
				return 1
			case err != nil:
				fmt.Fprintf(evalProgress, "could not check that the query server and the job server serve the same model: %v\n", err)
			}
		}
	}
	st, err := store.OpenReadOnly(*dbPath, 2)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *setFile != "" || *requestsBuiltin || *sessionsBuiltin {
		if *requestsBuiltin {
			builtin, err := searchquality.RequestSet(st, *minChars, *sample, *seed)
			if err != nil {
				return evalFailed(stdout, err)
			}
			set = append(set, builtin...)
		}
		if *sessionsBuiltin {
			builtin, err := searchquality.SessionSet(st, *sessionsSample, *seed)
			if err != nil {
				return evalFailed(stdout, err)
			}
			set = append(set, builtin...)
		}
		if len(set) == 0 {
			fmt.Fprintln(stdout, "eval search: there is no query to measure: the set is empty, and the ledger holds no sighting quote, no duplicate or successor relation and no knowledge entry with evidence and a title")
			return 1
		}
		rep, err := searchquality.RunSet(ctx, st, opts, set)
		if err != nil {
			return evalFailed(stdout, err)
		}
		rep.Write(stdout)
		return 0
	}
	rep, err := searchquality.Run(ctx, st, opts)
	if err != nil {
		return evalFailed(stdout, err)
	}
	rep.Write(stdout)
	return 0
}

// evalFailed reports a failed run. A database from before the embedding tables
// has to be started once with this version, which creates them.
func evalFailed(stdout io.Writer, err error) int {
	if strings.Contains(err.Error(), "no such table") {
		fmt.Fprintln(stdout, "The database has no embedding tables yet. Start the server once with this version; it creates them.")
		return 1
	}
	fmt.Fprintf(stdout, "eval search: %v\n", err)
	return 1
}
