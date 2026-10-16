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
	"strconv"
	"syscall"

	"github.com/Deadweight-Labs/ghosttree/internal/explain"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	envExplain            = "GHOSTTREE_EXPLAIN"
	envExplainModel       = "GHOSTTREE_EXPLAIN_MODEL"
	envExplainDailyCalls  = "GHOSTTREE_EXPLAIN_DAILY_CALLS"
	envExplainDailyTokens = "GHOSTTREE_EXPLAIN_DAILY_TOKENS"

	defaultExplainDailyCalls  = 200
	defaultExplainDailyTokens = 400000
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
  --daily-calls  cap per day over server and backfill together (default 200, 0 = none)
  --daily-tokens cap per day (default 400000, 0 = none)
  --model        override the model of the provider configuration

Without a provider configuration nothing is generated and the command says so.`

// ExplainConfig is how the server and the backfill command set up the model.
type ExplainConfig struct {
	// Mode is "auto" (use the model when the provider configuration loads) or
	// "off"; empty counts as off so that tests never pick up a user's file.
	Mode        string
	Model       string
	DailyCalls  int
	DailyTokens int
	Language    string
}

// newExplainer builds the service. Without a loadable provider configuration it
// is read-only (agent-written explanations show, nothing is generated); that is
// the normal state of a server that has no key, not an error.
func newExplainer(st explain.Store, cfg ExplainConfig, logger *slog.Logger) *explain.Service {
	ec := explain.Config{Language: cfg.Language, Limits: explain.Limits{DailyCalls: cfg.DailyCalls, DailyTokens: cfg.DailyTokens}}
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
	logger.Info("explain_model", "model", lc.Model, "format", lc.Format, "language", ec.Language, "daily_calls", cfg.DailyCalls, "daily_tokens", cfg.DailyTokens)
	return explain.New(st, client, ec)
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v >= 0 {
		return v
	}
	return def
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
	calls := fs.Int("daily-calls", envInt(envExplainDailyCalls, defaultExplainDailyCalls), "cap of model calls per day (0 = none)")
	tokens := fs.Int("daily-tokens", envInt(envExplainDailyTokens, defaultExplainDailyTokens), "cap of tokens per day (0 = none)")
	model := fs.String("model", os.Getenv(envExplainModel), "model, overriding the provider configuration")
	fs.Usage = func() { fmt.Fprintln(stdout, explainUsage) }
	if fs.Parse(args[1:]) != nil || *dbPath == "" || *limit < 0 || *calls < 0 || *tokens < 0 {
		fmt.Fprintln(stdout, explainUsage)
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
	svc := newExplainer(st, ExplainConfig{Mode: "auto", Model: *model, DailyCalls: *calls, DailyTokens: *tokens, Language: lang}, logger)
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
		fmt.Fprintf(stdout, "today: %d calls (cap %s), %d tokens (cap %s), %d failed\n", today.Calls, capText(lim.DailyCalls), today.Tokens(), capText(lim.DailyTokens), today.Failures)
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
		fmt.Fprint(stdout, ", stopped at the daily cap")
	case "none":
		fmt.Fprint(stdout, ", nothing left")
	}
	fmt.Fprintln(stdout)
	for _, e := range report.Errors {
		fmt.Fprintf(stdout, "  %s\n", e)
	}
	return 0
}

func capText(n int) string {
	if n <= 0 {
		return "none"
	}
	return strconv.Itoa(n)
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
