package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const costUsage = `usage: ctx cost --db <path> [--by project|model|version|day] [--since YYYY-MM-DD]

Report what the session distiller has been billed, and what finishing the
backlog will cost.

Covers the batch path only. The synchronous path and 'ctx migrate' call the
model without ever seeing a token count, so they cannot be metered and are not
included here.`

func cmdCost(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	by := fs.String("by", "", "break down by project, model, version or day")
	since := fs.String("since", "", "only count batches created on or after this date")
	fs.Usage = func() { fmt.Fprintln(stdout, costUsage) }
	if fs.Parse(args) != nil || *dbPath == "" {
		fmt.Fprintln(stdout, costUsage)
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	total, err := st.DistillCost("", *since)
	if err != nil {
		fmt.Fprintf(stdout, "cost: %v\n", err)
		return 1
	}
	if len(total) == 0 {
		fmt.Fprintln(stdout, "nothing has been billed yet")
		return 0
	}
	spent := total[0]
	spentUSD := sessiondistill.BatchCostUSD(spent.PromptTokens, spent.CompletionTokens)
	fmt.Fprintf(stdout, "billed so far: $%.4f over %d batches and %d sessions\n", spentUSD, spent.Batches, spent.Sessions)
	fmt.Fprintf(stdout, "  %d input and %d output tokens, %.4f cents per session\n",
		spent.PromptTokens, spent.CompletionTokens, 100*spentUSD/float64(max(spent.Sessions, 1)))

	if *by != "" {
		rows, err := st.DistillCost(*by, *since)
		if err != nil {
			fmt.Fprintf(stdout, "cost: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "\nby %s:\n", *by)
		for _, r := range rows {
			label := r.Group
			if label == "" {
				label = "(none)"
			}
			fmt.Fprintf(stdout, "  %-40s $%.4f  %d sessions, %d in / %d out\n", label,
				sessiondistill.BatchCostUSD(r.PromptTokens, r.CompletionTokens),
				r.Sessions, r.PromptTokens, r.CompletionTokens)
		}
	}

	cutoff := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	pending, pendingChars, err := st.PendingDistillationSize(scope.Axes{}, cutoff)
	if err != nil {
		fmt.Fprintf(stdout, "forecast: %v\n", err)
		return 1
	}
	if pending == 0 {
		fmt.Fprintln(stdout, "\nnothing left to distil")
		return 0
	}
	fmt.Fprintf(stdout, "\nremaining: %d sessions, %d transcript characters\n", pending, pendingChars)

	billedChars, err := st.BilledTranscriptChars(*since)
	if err != nil {
		fmt.Fprintf(stdout, "forecast: %v\n", err)
		return 1
	}
	if billedChars == 0 || spent.PromptTokens == 0 {
		fmt.Fprintln(stdout, "  no forecast: nothing billed yet to derive a ratio from")
		return 0
	}
	// The ratio comes from what was actually billed, not from the pre-flight
	// estimator. That one guesses low on purpose so a prompt is never larger
	// than planned; using it here would overstate what is left to pay.
	charsPerToken := float64(billedChars) / float64(spent.PromptTokens)
	inputTokens := int(float64(pendingChars) / charsPerToken)
	// Output scales with session count, not with transcript size: a reply is a
	// handful of short items however long the input was.
	outputTokens := int(float64(spent.CompletionTokens) / float64(max(spent.Sessions, 1)) * float64(pending))
	fmt.Fprintf(stdout, "  forecast $%.4f at the measured %.2f characters per token\n",
		sessiondistill.BatchCostUSD(inputTokens, outputTokens), charsPerToken)
	return 0
}
