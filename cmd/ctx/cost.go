package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/sessiondistill"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const costUsage = `usage: ctx cost --db <path> [--by project|model|version|day] [--since YYYY-MM-DD] [--days 7]

Report what the model jobs used, and what the session distiller has been billed.

The gardener (garden) and "Kurz erklärt" (explain) are shown per day and per
month (UTC) with the limits set for them: dollars, calls and tokens, where
unlimited means no cap. Tokens include what the provider served from or wrote
to its prompt cache, and the dollars price those at their own rates. Limits are
read from this environment: GHOSTTREE_GARDEN_DAILY_USD, GHOSTTREE_EXPLAIN_MONTHLY_CALLS
and the like, or the "limits" entry of llm.json (GHOSTTREE_LLM_CONFIG).

The distiller part covers the batch path only. The synchronous path and
'ctx migrate' call the model without ever seeing a token count, so they cannot
be metered and are not included there.`

func cmdCost(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	by := fs.String("by", "", "break down by project, model, version or day")
	since := fs.String("since", "", "only count batches created on or after this date")
	days := fs.Int("days", 7, "days of history per model job")
	fs.Usage = func() { fmt.Fprintln(stdout, costUsage) }
	if fs.Parse(args) != nil || *dbPath == "" || *days < 0 {
		fmt.Fprintln(stdout, costUsage)
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	if code := printJobUsage(st, stdout, time.Now(), *days); code != 0 {
		return code
	}

	total, err := st.DistillCost("", *since)
	if err != nil {
		fmt.Fprintf(stdout, "cost: %v\n", err)
		return 1
	}
	if len(total) == 0 || total[0].Batches == 0 {
		fmt.Fprintln(stdout, "distiller: nothing has been billed yet")
		return 0
	}
	fmt.Fprintln(stdout, "\ndistiller:")
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

// printJobUsage lists what each model job used today, this month and on the
// last days, next to the limits that apply to it.
func printJobUsage(st *store.Store, stdout io.Writer, at time.Time, days int) int {
	t := at.UTC()
	for _, job := range llmusage.Jobs {
		lim, err := jobLimits(job)
		if err != nil {
			fmt.Fprintf(stdout, "limits of %s: %v\n", job, err)
			return 1
		}
		day, err := st.LLMUsageOn(job, t.Format("2006-01-02"))
		if err != nil {
			fmt.Fprintf(stdout, "usage of %s: %v\n", job, err)
			return 1
		}
		month, err := st.LLMUsageMonth(job, t.Format("2006-01"))
		if err != nil {
			fmt.Fprintf(stdout, "usage of %s: %v\n", job, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", job)
		fmt.Fprintf(stdout, "  today      %s   limit: %s\n", usageLine(day), lim.Day.Text())
		fmt.Fprintf(stdout, "  this month %s   limit: %s\n", usageLine(month), lim.Month.Text())
		if hit := lim.Check(day, month); hit != nil {
			fmt.Fprintf(stdout, "  REACHED: %s\n", hit)
		}
		if days > 0 {
			recent, err := st.LLMUsageRecent(job, days)
			if err != nil {
				fmt.Fprintf(stdout, "usage of %s: %v\n", job, err)
				return 1
			}
			for _, u := range recent {
				fmt.Fprintf(stdout, "  %s %s\n", u.Day, usageLine(u))
			}
		}
	}
	return 0
}

func usageLine(u llmusage.Usage) string {
	line := fmt.Sprintf("$%.4f, %d calls, %d tokens", u.CostUSD(), u.Calls, u.Tokens())
	if u.CacheReadTokens+u.CacheWriteTokens > 0 {
		line += fmt.Sprintf(" (cache: %d read, %d written)", u.CacheReadTokens, u.CacheWriteTokens)
	}
	if u.Failures > 0 {
		line += fmt.Sprintf(", %d failed", u.Failures)
	}
	return line
}
