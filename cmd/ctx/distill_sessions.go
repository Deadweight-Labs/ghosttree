package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func cmdDistillSessions(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("distill-sessions", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to sqlite database")
	idle := fs.Duration("idle", time.Hour, "minimum session idle time")
	limit := fs.Int("limit", 20, "maximum sessions per run")
	project := fs.String("project", "", "restrict to one project (canonical remote)")
	submit := fs.Bool("submit", false, "send pending sessions as one batch job")
	collect := fs.Bool("collect", false, "ingest results of finished batch jobs")
	dryRun := fs.Bool("dry-run", false, "with --submit: report size and cost without sending")
	if fs.Parse(args) != nil || *idle <= 0 || *limit <= 0 {
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	cfg, err := llm.LoadConfig()
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	model, err := llm.New(cfg)
	if err != nil {
		fmt.Fprintf(stdout, "LLM config failed: %v\n", err)
		return 1
	}
	filter := scope.CanonicalAxes(scope.Axes{Project: *project})
	cutoff := time.Now().Add(-*idle).UTC().Format(time.RFC3339Nano)

	if !*submit && !*collect {
		return distillSynchronously(st, model, filter, cutoff, *limit, stdout)
	}
	batch, ok := model.(llm.BatchClient)
	if !ok {
		fmt.Fprintf(stdout, "the configured provider has no batch endpoint\n")
		return 1
	}
	// Collect first: a finished batch releases its sessions, and the ones it
	// failed on belong in the batch this same run is about to submit.
	if *collect {
		if code := collectBatches(st, batch, stdout); code != 0 {
			return code
		}
	}
	if *submit {
		return submitBatch(st, batch, filter, cutoff, *limit, *dryRun, stdout)
	}
	return 0
}

func submitBatch(st *store.Store, client llm.BatchClient, filter scope.Axes, cutoff string, limit int, dryRun bool, stdout io.Writer) int {
	report, err := sessiondistill.SubmitBatch(context.Background(), st, client, sessiondistill.SubmitOptions{
		Filter: filter, IdleBefore: cutoff, Limit: limit,
		Budget: sessiondistill.DefaultBudget, DryRun: dryRun,
	})
	if err != nil {
		fmt.Fprintf(stdout, "submit: %v\n", err)
		return 1
	}
	if report.TrimmedSessions > 0 {
		// Say it out loud: a trimmed transcript and an uneventful one produce
		// the same small result otherwise.
		fmt.Fprintf(stdout, "%d sessions over budget, %d chunks omitted\n", report.TrimmedSessions, report.DroppedChunks)
	}
	if report.Sessions == 0 {
		fmt.Fprintf(stdout, "nothing to submit\n")
		return 0
	}
	// Output is priced at the cap because nothing better is knowable before the
	// answer exists; the input figure is the local character estimate. Both are
	// replaced by usage.prompt_tokens at collect time.
	cost := sessiondistill.BatchCostUSD(report.EstimatedTokens, report.Sessions*sessiondistill.MaxOutputTokens)
	if dryRun {
		fmt.Fprintf(stdout, "would submit %d sessions, ~%d input tokens, at most $%.2f\n",
			report.Sessions, report.EstimatedTokens, cost)
		return 0
	}
	fmt.Fprintf(stdout, "submitted batch %s: %d sessions, ~%d input tokens, at most $%.2f\n",
		report.ProviderID, report.Sessions, report.EstimatedTokens, cost)
	return 0
}

func collectBatches(st *store.Store, client llm.BatchClient, stdout io.Writer) int {
	report, err := sessiondistill.CollectBatches(context.Background(), st, client)
	if err != nil {
		fmt.Fprintf(stdout, "collect: %v\n", err)
		return 1
	}
	if report.Pending > 0 {
		fmt.Fprintf(stdout, "%d batches still running\n", report.Pending)
	}
	if report.Batches == 0 {
		return 0
	}
	fmt.Fprintf(stdout, "collected %d batches: %d sessions distilled into %d quarantined items, %d failed (%d truncated)\n",
		report.Batches, report.Sessions, report.Items, report.Failed, report.Truncated)
	// A failure count without a cause cannot be acted on, and these run under a
	// timer where the journal is the only place anyone will look.
	for _, failure := range report.Failures {
		fmt.Fprintf(stdout, "  %s\n", failure)
	}
	fmt.Fprintf(stdout, "billed %d input and %d output tokens, $%.4f\n",
		report.PromptTokens, report.CompletionTokens,
		sessiondistill.BatchCostUSD(report.PromptTokens, report.CompletionTokens))
	return 0
}

func distillSynchronously(st *store.Store, model llm.Client, filter scope.Axes, cutoff string, limit int, stdout io.Writer) int {
	sessions, err := st.SessionsPendingDistillation(filter, cutoff, limit)
	if err != nil {
		fmt.Fprintf(stdout, "list sessions: %v\n", err)
		return 1
	}
	processed, inserted := 0, 0
	for _, session := range sessions {
		chunks, err := st.ReadSession(session.ID, 0, 5000)
		if err != nil || len(chunks) == 0 {
			continue
		}
		digest := sessiondistill.Digest(chunks)
		exists, err := st.SessionDistillationExists(session.ID, digest)
		if err != nil || exists {
			continue
		}
		existing, err := st.KnowledgeForProject(session.Scope.Project)
		if err != nil {
			fmt.Fprintf(stdout, "session %d knowledge: %v\n", session.ID, err)
			return 1
		}
		titles := make([]string, 0, len(existing))
		for _, k := range existing {
			titles = append(titles, k.Title)
		}
		items, dropped, err := sessiondistill.DistillWithBudget(context.Background(), model, chunks, titles, sessiondistill.DefaultBudget)
		if err != nil {
			fmt.Fprintf(stdout, "session %d distill: %v\n", session.ID, err)
			continue
		}
		if dropped > 0 {
			// Say it out loud: a trimmed transcript and an uneventful one
			// produce the same small result otherwise.
			fmt.Fprintf(stdout, "session %d: transcript over budget, %d of %d chunks omitted\n", session.ID, dropped, len(chunks))
		}
		n, err := st.ApplySessionDistillation(session.ID, digest, session.Scope, items)
		if err != nil {
			fmt.Fprintf(stdout, "session %d persist: %v\n", session.ID, err)
			continue
		}
		processed++
		inserted += n
		if processed >= limit {
			break
		}
	}
	fmt.Fprintf(stdout, "distilled %d sessions into %d quarantined knowledge items\n", processed, inserted)
	return 0
}
