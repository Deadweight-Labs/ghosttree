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

const explainUsage = `usage: ctx explain backfill --db <path> (--batch | --collect) [--limit N]
       ctx explain backfill --db <path> --dry-run
       ctx explain status --db <path>

"In short" (Kurz erklärt): a plain-language note on every knowledge entry,
request, pain, task and room question for a person. Agents write it when they
create the object. The server writes the missing note of a NEW object with the
model when its page is opened. Everything else, the whole backlog and every
changed object, is written as a batch through the Message Batches API at half
the price, in two steps that may run hours apart:

  --batch        collect finished batches, then submit one batch (several if the
                 API's size limits need it) for every object without a current
                 note, the review queue first. An object that waits in an open
                 batch is not sent again; one whose entry failed is left alone
                 for a day.
  --collect      only collect finished batches and store their notes

The batch has its own provider: the "explain" entry of llm.json (GHOSTTREE_LLM_CONFIG,
or ~/.config/ghosttree/llm.json) with api_key_file or credential, or
GHOSTTREE_EXPLAIN_KEY. Neither the top-level provider nor the gardener's entry is
ever used. Without a key nothing is sent and the command says so.

  --db           the ghosttree database (the server may keep running)
  --limit        submit at most this many objects per run (default 5000)
  --dry-run      count what is missing, call nothing
  --language     en or de (default: GHOSTTREE_LANGUAGE, else en)
  --daily-calls, --daily-tokens, --daily-usd, --monthly-calls, --monthly-tokens,
  --monthly-usd  caps over server and batches together; 0 or empty = unlimited.
                 Default: 200 calls and 400000 tokens a day, no monthly cap. They
                 can also be set in the "limits" entry of llm.json or with
                 GHOSTTREE_EXPLAIN_DAILY_CALLS and the like (see 'ctx cost').
                 Objects waiting in open batches count as calls.
  --model        override the model of the explain entry`

// ExplainConfig is how the server and the backfill command set up the model.
type ExplainConfig struct {
	// Mode is "auto" (use the model when the provider configuration loads) or
	// "off"; empty counts as off so that tests never pick up a user's file.
	Mode     string
	Model    string
	Limits   llmusage.Limits
	Language string
}

// newExplainer builds the service. Without a key for the "explain" entry it is
// read-only (agent-written explanations show, nothing is generated); that is
// the normal state of a server that has no key, not an error.
func newExplainer(st explain.Store, cfg ExplainConfig, logger *slog.Logger) *explain.Service {
	svc, _ := buildExplainer(st, cfg, logger)
	return svc
}

// buildExplainer also returns why there is no model, for commands that say so.
func buildExplainer(st explain.Store, cfg ExplainConfig, logger *slog.Logger) (*explain.Service, string) {
	ec := explain.Config{Language: cfg.Language, Limits: cfg.Limits}
	if cfg.Mode != "auto" {
		return explain.New(st, nil, ec), "explanations are off"
	}
	lc, err := llm.LoadExplainConfig()
	if err != nil {
		if errors.Is(err, llm.ErrExplainDisabled) {
			logger.Info("explain_without_model", "reason", err.Error())
		} else {
			logger.Warn("explain_without_model", "reason", err.Error())
		}
		return explain.New(st, nil, ec), err.Error()
	}
	if cfg.Model != "" {
		lc.Model = cfg.Model
	}
	client, err := llm.New(lc.Config)
	if err != nil {
		logger.Warn("explain_without_model", "reason", err.Error())
		return explain.New(st, nil, ec), err.Error()
	}
	ec.Model, ec.Metered = lc.Model, true
	ec.Batch, _ = client.(llm.BatchClient)
	ec.Price, ec.PriceKnown = lc.Price()
	if !ec.PriceKnown && (cfg.Limits.Day.USD > 0 || cfg.Limits.Month.USD > 0) {
		logger.Warn("explain_usd_limit_without_price", "model", lc.Model, "hint", "set input_usd_per_mtok and output_usd_per_mtok in the explain entry of llm.json; a dollar cap cannot stop anything without them")
	}
	logger.Info("explain_model", "model", lc.Model, "base_url", lc.BaseURL, "language", ec.Language,
		"daily", cfg.Limits.Day.Text(), "monthly", cfg.Limits.Month.Text())
	return explain.New(st, client, ec), ""
}

func cmdExplain(args []string, stdout io.Writer) int {
	if len(args) == 0 || (args[0] != "backfill" && args[0] != "status") {
		fmt.Fprintln(stdout, explainUsage)
		return 2
	}
	fs := flag.NewFlagSet("explain "+args[0], flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	limit := fs.Int("limit", 5000, "submit at most this many objects per run")
	batch := fs.Bool("batch", false, "collect finished batches, then submit the missing explanations as a batch")
	collect := fs.Bool("collect", false, "only collect finished batches")
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
	svc, why := buildExplainer(st, ExplainConfig{Mode: "auto", Model: *model, Limits: lim, Language: lang}, logger)
	return runExplain(args[0], svc, why, st, explainRun{limit: *limit, dry: *dry, batch: *batch, collect: *collect}, stdout)
}

type explainRun struct {
	limit               int
	dry, batch, collect bool
}

func runExplain(sub string, svc *explain.Service, why string, st *store.Store, run explainRun, stdout io.Writer) int {
	today, err := svc.Today()
	if err != nil {
		fmt.Fprintf(stdout, "usage: %v\n", err)
		return 1
	}
	lim := svc.Limits()
	agent, generated, _ := st.ExplanationCounts()
	if sub == "status" || run.dry {
		if svc.Enabled() {
			fmt.Fprintf(stdout, "model: %s, language %s\n", svc.Model(), svc.Language())
		} else {
			fmt.Fprintf(stdout, "no model configured: %s; nothing is generated (agent-written explanations still show)\n", why)
		}
		month, _ := svc.Month()
		fmt.Fprintf(stdout, "today: %d calls, %d tokens, $%.4f, %d failed (cap %s)\n", today.Calls, today.Tokens(), today.CostUSD(), today.Failures, lim.Day.Text())
		fmt.Fprintf(stdout, "this month: %d calls, %d tokens, $%.4f (cap %s)\n", month.Calls, month.Tokens(), month.CostUSD(), lim.Month.Text())
		fmt.Fprintf(stdout, "stored: %d by agents, %d generated\n", agent, generated)
		if open, err := st.OpenExplainBatches(); err == nil {
			waiting := 0
			for _, b := range open {
				waiting += b.Pending
			}
			fmt.Fprintf(stdout, "batches open: %d (%d objects waiting)\n", len(open), waiting)
		}
		missing, err := countMissing(svc, st)
		if err == nil {
			fmt.Fprintf(stdout, "missing or outdated: %d\n", missing)
		}
		return 0
	}
	if !run.batch && !run.collect {
		fmt.Fprintln(stdout, "bulk explanations are written as a batch only: use --batch (collect and submit) or --collect")
		return 2
	}
	if !svc.Enabled() {
		fmt.Fprintf(stdout, "no model configured: %s; nothing sent\n", why)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Collecting comes first: it frees what the open batches hold back and
	// shows which objects are still missing.
	col, err := svc.CollectBatches(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stdout, "collect: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "collected %d batches: saved %d, failed %d, outdated %d, already current %d; still open %d\n",
		col.Collected, col.Saved, col.Failed, col.Stale, col.Superseded, col.Open)
	for _, e := range col.Errors {
		fmt.Fprintf(stdout, "  %s\n", e)
	}
	if !run.batch {
		return 0
	}
	rep, err := svc.SubmitBatches(ctx, run.limit)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stdout, "submit: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "submitted %d objects in %d batches; already current %d, already waiting %d, not sendable %d",
		rep.Submitted, rep.Batches, rep.Fresh, rep.Pending, rep.Skipped)
	switch rep.Stopped {
	case "limit":
		fmt.Fprintf(stdout, ", stopped: %s", rep.LimitHit)
	case "none":
		fmt.Fprint(stdout, ", nothing left")
	}
	fmt.Fprintln(stdout)
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
