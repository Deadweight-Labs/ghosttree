package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	envExplain      = "GHOSTTREE_EXPLAIN"
	envExplainModel = "GHOSTTREE_EXPLAIN_MODEL"
)

const explainUsage = `usage: ctx explain backfill --db <path> [--limit N] [--dry-run]
       ctx explain status --db <path>

"In short" (Kurz erklärt): a plain-language note on every knowledge entry,
request, pain, task for a person and room question. Agents write it when they
create the object; backfill writes the missing or outdated ones with the model
from the provider configuration (GHOSTTREE_LLM_CONFIG, or
~/.config/ghosttree/llm.json), the review queue first.

  --db           the ghosttree database (the server may keep running)
  --limit        explain at most this many objects (default 50)
  --dry-run      count what is missing, call nothing
  --language     en or de (default: GHOSTTREE_LANGUAGE, else en)
  --daily-calls, --daily-tokens, --daily-usd, --monthly-calls, --monthly-tokens,
  --monthly-usd  caps over server and backfill together; 0 or empty = unlimited.
                 Default: 200 calls and 400000 tokens a day, no monthly cap. They
                 can also be set in the "limits" entry of llm.json or with
                 GHOSTTREE_EXPLAIN_DAILY_CALLS and the like (see 'ctx cost').
  --model        override the model of the provider configuration

Without a provider configuration nothing is generated and the command says so.`

// ExplainConfig is how the server and the backfill command set up the model.
type ExplainConfig struct {
	// Mode is "auto" (use the model when the provider configuration loads) or
	// "off"; empty counts as off so that tests never pick up a user's file.
	Mode     string
	Model    string
	Limits   llmusage.Limits
	Language string
}

// newExplainer builds the service. Without a loadable provider configuration it
// is read-only (agent-written explanations show, nothing is generated); that is
// the normal state of a server that has no key, not an error.
func newExplainer(st explain.Store, cfg ExplainConfig, logger *slog.Logger) *explain.Service {
	ec := explain.Config{Language: cfg.Language, Limits: cfg.Limits}
	if cfg.Mode != "auto" {
		return explain.New(st, nil, ec)
	}
	lc, err := llm.LoadConfig()
	if err != nil {
		logger.Info("explain_without_model", "reason", err.Error())
		return explain.New(st, nil, ec)
	}
	if cfg.Model != "" {
		lc.Model = cfg.Model
	}
	client, err := llm.New(lc)
	if err != nil {
		logger.Warn("explain_without_model", "reason", err.Error())
		return explain.New(st, nil, ec)
	}
	ec.Model, ec.Metered = lc.Model, lc.Format == "anthropic"
	ec.Price, ec.PriceKnown = lc.Price()
	if !ec.PriceKnown && (cfg.Limits.Day.USD > 0 || cfg.Limits.Month.USD > 0) {
		logger.Warn("explain_usd_limit_without_price", "model", lc.Model, "hint", "set input_usd_per_mtok and output_usd_per_mtok in llm.json; a dollar cap cannot stop anything without them")
	}
	logger.Info("explain_model", "model", lc.Model, "format", lc.Format, "language", ec.Language,
		"daily", cfg.Limits.Day.Text(), "monthly", cfg.Limits.Month.Text())
	return explain.New(st, client, ec)
}

func cmdExplain(args []string, stdout io.Writer) int {
	if len(args) == 0 || (args[0] != "backfill" && args[0] != "status") {
		fmt.Fprintln(stdout, explainUsage)
		return 2
	}
	fs := flag.NewFlagSet("explain "+args[0], flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	limit := fs.Int("limit", 50, "explain at most this many objects")
	dry := fs.Bool("dry-run", false, "count what is missing, call nothing")
	language := fs.String("language", os.Getenv(envLanguage), "en or de")
	limits := addLimitFlags(fs, llmusage.JobExplain, "")
	model := fs.String("model", os.Getenv(envExplainModel), "model, overriding the provider configuration")
	fs.Usage = func() { fmt.Fprintln(stdout, explainUsage) }
	if fs.Parse(args[1:]) != nil || *dbPath == "" || *limit < 0 {
		fmt.Fprintln(stdout, explainUsage)
		return 2
	}
	lim, err := limits()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	lang := *language
	if lang != "de" {
		lang = "en"
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := newExplainer(st, ExplainConfig{Mode: "auto", Model: *model, Limits: lim, Language: lang}, logger)
	return runExplain(args[0], svc, st, *limit, *dry, stdout)
}

func runExplain(sub string, svc *explain.Service, st *store.Store, limit int, dry bool, stdout io.Writer) int {
	today, err := svc.Today()
	if err != nil {
		fmt.Fprintf(stdout, "usage: %v\n", err)
		return 1
	}
	lim := svc.Limits()
	agent, generated, _ := st.ExplanationCounts()
	if sub == "status" || dry {
		if svc.Enabled() {
			fmt.Fprintf(stdout, "model: %s, language %s\n", svc.Model(), svc.Language())
		} else {
			fmt.Fprintln(stdout, "no model configured: nothing is generated (agent-written explanations still show)")
		}
		month, _ := svc.Month()
		fmt.Fprintf(stdout, "today: %d calls, %d tokens, $%.4f, %d failed (cap %s)\n", today.Calls, today.Tokens(), today.CostUSD(), today.Failures, lim.Day.Text())
		fmt.Fprintf(stdout, "this month: %d calls, %d tokens, $%.4f (cap %s)\n", month.Calls, month.Tokens(), month.CostUSD(), lim.Month.Text())
		fmt.Fprintf(stdout, "stored: %d by agents, %d generated\n", agent, generated)
		missing, err := countMissing(svc, st)
		if err == nil {
			fmt.Fprintf(stdout, "missing or outdated: %d\n", missing)
		}
		return 0
	}
	if !svc.Enabled() {
		fmt.Fprintln(stdout, "no model configured: set GHOSTTREE_LLM_CONFIG (see 'ctx explain --help'); nothing generated")
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := svc.Backfill(ctx, limit)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stdout, "backfill: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "generated %d, already current %d, failed %d", report.Generated, report.Fresh, report.Failed)
	switch report.Stopped {
	case "limit":
		fmt.Fprintf(stdout, ", stopped: %s", report.LimitHit)
	case "none":
		fmt.Fprint(stdout, ", nothing left")
	}
	fmt.Fprintln(stdout)
	for _, e := range report.Errors {
		fmt.Fprintf(stdout, "  %s\n", e)
	}
	return 0
}

// countMissing counts the candidates without a current explanation.
func countMissing(svc *explain.Service, st *store.Store) (int, error) {
	cands, err := st.ExplainCandidates(100000)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cands {
		src, err := st.ExplainSource(c.Kind, c.ID)
		if err != nil || src.Project == "" {
			continue
		}
		e, ok, err := st.Explanation(c.Kind, c.ID)
		if err != nil {
			return 0, err
		}
		if !ok || e.ContentHash != src.Hash || (e.Source == explain.SourceGenerated && e.Language != svc.Language()) {
			n++
		}
	}
	return n, nil
}
