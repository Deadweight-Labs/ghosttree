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
                       [--embed-min-similarity 0.55] [--pause 50ms]

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
	embedFlagSet := addEmbedFlags(fs, false)
	fs.Usage = func() { fmt.Fprintln(stdout, evalUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || fs.NArg() > 0 || *sample <= 0 || *minChars <= 0 || *pause < 0 {
		fmt.Fprintln(stdout, evalUsage)
		return 2
	}
	cfg, err := embedFlagSet.resolve()
	if err != nil {
		fmt.Fprintf(stdout, "eval search: %v\n", err)
		return 2
	}
	opts := searchquality.Options{Sample: *sample, Seed: *seed, MinChars: *minChars, Pause: *pause, MinSimilarity: cfg.MinSimilarity, Progress: evalProgress}
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
	rep, err := searchquality.Run(ctx, st, opts)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			fmt.Fprintln(stdout, "The database has no embedding tables yet. Start the server once with this version; it creates them.")
			return 1
		}
		fmt.Fprintf(stdout, "eval search: %v\n", err)
		return 1
	}
	rep.Write(stdout)
	return 0
}
