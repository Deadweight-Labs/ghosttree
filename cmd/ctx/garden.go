package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const gardenUsage = `usage: ctx garden check-knowledge [--project <remote>] [--repo <dir>] [--type pitfall,decision]
                                  [--limit <n>] [--max-usd 2] [--dry-run] [--propose] [--out <file>]
                                  [--db <path>] [--min-age 14]
                                  [--daily-usd N] [--daily-calls N] [--daily-tokens N]
                                  [--monthly-usd N] [--monthly-calls N] [--monthly-tokens N]

The gardener checks pitfalls and decisions against the code and says whether
each one still holds. It proposes, a person decides.

It is an option and stays off without an API key: set GHOSTTREE_GARDEN_KEY, or a
"garden" entry with api_key_file or credential in llm.json. Without a key the
command stops and does nothing (a dry run is the one exception: it reads, counts
and calls nothing).

  --dry-run    count tokens and cost, call nothing
  --max-usd    stop cleanly before a call that could exceed this budget (default 2)
  --limit      send at most this many entries to the model
  --propose    file stale and correction verdicts as staged notes marked as
               coming from the gardener. Never verifies, deprecates or deletes
               anything, and the entry checked is left untouched.
  --out        write the Markdown report to a file instead of stdout
  --db         the ghosttree database. With it the gardener remembers, per entry,
               when and against which commit it looked (the next run takes entries
               it has never seen first, then the ones longest ago, and leaves out
               what is unchanged), and it records what each call used, per day,
               so daily and monthly limits hold across runs and restarts. Without
               it every run starts from the top and only --max-usd limits it.
  --min-age    days; an entry looked at more recently is left out unless it or the
               code it names changed (default 14, 0 = no such rule)
  --daily-*, --monthly-*
               limits in dollars, calls and tokens; 0 or empty = unlimited
               (the default). Also GHOSTTREE_GARDEN_DAILY_USD and so on, or the
               "limits" entry of llm.json. A limit that is reached ends the run
               cleanly, names itself, and exits with status 3.`

// gardenStore is what the command needs from the server: read the entries,
// and, with --propose, add new ones. There is no patch or delete in it.
type gardenStore interface {
	ProjectKnowledge(project string, includeArchived bool) ([]store.Knowledge, error)
	Remember(k store.Knowledge, autoCtx scope.Axes) (store.Knowledge, error)
}

// gardenState is what the gardener remembers between runs. It is the database
// itself; nil means a run that remembers nothing.
type gardenState interface {
	GardenChecks(project string) (map[int64]store.GardenCheck, error)
	SaveGardenCheck(project string, c store.GardenCheck) error
	LLMUsageOn(job, day string) (llmusage.Usage, error)
	LLMUsageMonth(job, month string) (llmusage.Usage, error)
	AddLLMUsage(job, day string, u llmusage.Usage) error
}

type gardenOpts struct {
	Types   map[string]bool
	Limit   int
	MaxUSD  float64
	DryRun  bool
	Propose bool
	// State is the memory between runs; nil keeps nothing.
	State gardenState
	// Limits are the daily and monthly caps (zero = unlimited).
	Limits llmusage.Limits
	// MinAge is the window in which an unchanged entry is not looked at again.
	MinAge time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func cmdGarden(args []string, stdout io.Writer) int {
	if len(args) == 0 || args[0] != "check-knowledge" {
		fmt.Fprintln(stdout, gardenUsage)
		return 2
	}
	fs := flag.NewFlagSet("garden check-knowledge", flag.ContinueOnError)
	fs.SetOutput(stdout)
	projectFlag := fs.String("project", "", "project (canonical remote); default: the current repository")
	repoFlag := fs.String("repo", "", "checkout to search; default: the current repository")
	types := fs.String("type", "pitfall,decision", "entry types to check")
	limit := fs.Int("limit", 0, "send at most this many entries to the model (0 = all)")
	maxUSD := fs.Float64("max-usd", 2, "budget in USD; a call that could exceed it is not made")
	dryRun := fs.Bool("dry-run", false, "count tokens and cost without calling the model")
	propose := fs.Bool("propose", false, "file stale/correction verdicts as staged gardener notes")
	out := fs.String("out", "", "write the report to this file")
	dbPath := fs.String("db", "", "ghosttree database: remember checks and usage between runs")
	minAge := fs.Int("min-age", 14, "days; an entry looked at more recently is left out unless it or its code changed (0 = off)")
	limits := addLimitFlags(fs, llmusage.JobGarden, "")
	fs.Usage = func() { fmt.Fprintln(stdout, gardenUsage) }
	// NaN compares false with everything and +Inf is no cap at all, so
	// neither may reach the budget check.
	if fs.Parse(args[1:]) != nil || !(*maxUSD > 0) || math.IsInf(*maxUSD, 0) || *limit < 0 || *minAge < 0 {
		fmt.Fprintln(stdout, gardenUsage)
		return 2
	}
	lim, err := limits()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}

	// Opt-in first: nothing below runs without a key, except counting.
	cfg, err := llm.LoadGardenConfig()
	if errors.Is(err, llm.ErrGardenDisabled) {
		if !*dryRun {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintln(stdout, "note: no gardener key configured; this dry run calls nothing, a real run would refuse")
	} else if err != nil {
		fmt.Fprintf(stdout, "gardener config: %v\n", err)
		return 1
	}
	if cfg.InputUSDPerMTok <= 0 || cfg.OutputUSDPerMTok <= 0 {
		fmt.Fprintf(stdout, "no price known for model %q: set input_usd_per_mtok and output_usd_per_mtok in the \"garden\" entry, the budget cannot be enforced without them\n", cfg.Model)
		return 1
	}
	pricing := garden.Pricing{InUSDPerMTok: cfg.InputUSDPerMTok, OutUSDPerMTok: cfg.OutputUSDPerMTok,
		CacheReadUSDPerMTok: cfg.CacheReadUSDPerMTok, CacheWriteUSDPerMTok: cfg.CacheWriteUSDPerMTok}

	cwd, _ := os.Getwd()
	git := collector.ResolveGitContext(cwd)
	root, project := *repoFlag, scope.NormalizeRemote(*projectFlag)
	if root == "" {
		root = git.Root
	}
	if project == "" {
		project = git.Project
	}
	if root == "" || project == "" {
		fmt.Fprintln(stdout, "need a git checkout with an origin remote, or --repo and --project")
		return 2
	}
	if err := verifyCheckout(root, project); err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	repo, err := garden.OpenRepo(root)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	sc, err := config.Load()
	if err != nil {
		fmt.Fprintf(stdout, "no config (%s) - run 'ctx setup'\n", config.Path())
		return 1
	}
	var model llm.UsageClient
	if !*dryRun {
		c, err := llm.New(cfg.Config)
		if err != nil {
			fmt.Fprintf(stdout, "gardener config: %v\n", err)
			return 1
		}
		model = c.(llm.UsageClient)
	}
	opts := gardenOpts{Types: map[string]bool{}, Limit: *limit, MaxUSD: *maxUSD, DryRun: *dryRun, Propose: *propose,
		Limits: lim, MinAge: time.Duration(*minAge) * 24 * time.Hour}
	if *dbPath != "" {
		st, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintf(stdout, "open db: %v\n", err)
			return 1
		}
		defer st.Close()
		opts.State = st
	} else if !*dryRun {
		fmt.Fprintln(stdout, "note: no --db, so this run neither remembers which entries it checked nor counts towards daily and monthly limits")
	}
	for _, t := range strings.Split(*types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			opts.Types[t] = true
		}
	}
	return runGardenCheck(client.New(sc), repo, project, opts, model, pricing, *out, stdout)
}

// verifyCheckout refuses a checkout whose origin is not the project being
// checked. Against the wrong repository nearly every symbol is missing, and
// every entry would come back stale.
func verifyCheckout(root, project string) error {
	origin, _ := collector.GitInfo(root)
	if origin == "" {
		return fmt.Errorf("%s has no origin remote, so it cannot be matched to %s", root, project)
	}
	if origin != scope.NormalizeRemote(project) {
		return fmt.Errorf("%s is a checkout of %s, not of %s; refusing to compare them", root, origin, project)
	}
	return nil
}

func runGardenCheck(st gardenStore, repo *garden.Repo, project string, opts gardenOpts, model llm.UsageClient, pricing garden.Pricing, outPath string, stdout io.Writer) int {
	all, err := st.ProjectKnowledge(project, false)
	if err != nil {
		fmt.Fprintf(stdout, "cannot read knowledge: %v\n", err)
		return 1
	}
	var entries []garden.Entry
	for _, k := range all {
		if !opts.Types[k.Type] || k.Status != "active" || k.Scope.Project != project || k.Harness == garden.Harness {
			continue
		}
		entries = append(entries, garden.Entry{ID: k.ID, Type: k.Type, Title: k.Title, Body: k.Body, ObservedAt: k.ObservedAt, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt})
	}
	// Oldest observation first: those are the ones most likely to have been
	// overtaken, and a limited run should spend itself there. Rotation then
	// puts entries never looked at before the ones looked at long ago.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Since() < entries[j].Since() })
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "no %s entries in %s\n", joinTypes(opts.Types), project)
		return 0
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	head, _ := repo.Head()
	var rot garden.Rotation
	if opts.State != nil {
		checks, err := opts.State.GardenChecks(project)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read earlier checks: %v\n", err)
			return 1
		}
		seen := make(map[int64]garden.Seen, len(checks))
		for id, c := range checks {
			seen[id] = garden.Seen{CheckedAt: c.CheckedAt, Commit: c.Commit, EntryHash: c.EntryHash, Verdict: c.Verdict}
		}
		entries, rot = garden.Rotate(repo, entries, seen, opts.MinAge, now())
	}
	meter := &gardenMeter{st: opts.State, limits: opts.Limits, now: now}

	gopts := garden.Options{
		Model: model, Pricing: pricing, MaxUSD: opts.MaxUSD, DryRun: opts.DryRun, Limit: opts.Limit,
		Gate: meter.gate, OnCall: meter.record,
	}
	if opts.State != nil {
		gopts.OnJudged = func(v garden.Verdict) {
			err := opts.State.SaveGardenCheck(project, store.GardenCheck{KnowledgeID: v.Entry.ID, CheckedAt: now().UTC().Format(time.RFC3339Nano),
				Commit: head, EntryHash: garden.HashOf(v.Entry), Verdict: v.Kind})
			if err != nil {
				fmt.Fprintf(stdout, "could not remember the check of #%d: %v\n", v.Entry.ID, err)
			}
		}
	}
	res := garden.Check(context.Background(), repo, entries, gopts)
	res.Rotation = rot
	if meter.err != nil {
		fmt.Fprintf(stdout, "could not record usage: %v\n", meter.err)
	}
	var report bytes.Buffer
	garden.WriteReport(&report, project, res, time.Now())

	// The report comes first: the model calls are paid for, and nothing that
	// goes wrong afterwards may cost the person the result. If the file cannot
	// be written the report goes to stdout instead.
	code := 0
	written := false
	if outPath != "" {
		if err := writePrivate(outPath, report.Bytes()); err != nil {
			fmt.Fprintf(stdout, "write report: %v; printing it instead\n", err)
			code = 1
		} else {
			written = true
			verb := "spent"
			if opts.DryRun {
				verb = "estimated"
			}
			fmt.Fprintf(stdout, "report: %s (%d entries, %s $%.4f)\n", outPath, len(res.Verdicts), verb, res.Spent)
		}
	}
	if !written {
		stdout.Write(report.Bytes())
	}

	if opts.Propose && !opts.DryRun {
		existing, err := st.ProjectKnowledge(project, true)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read existing proposals: %v\n", err)
			return 1
		}
		n, err := garden.Propose(st, project, existing, res.Verdicts)
		if err != nil {
			fmt.Fprintf(stdout, "%v (%d proposals filed before the failure; the report above is complete)\n", err, n)
			return 1
		}
		fmt.Fprintf(stdout, "%s %d proposals filed as staged notes from the gardener (see 'ctx review')\n", garden.Headline(res.Verdicts), n)
	}
	if code != 0 {
		return code
	}
	switch res.StoppedBy {
	case "budget":
		fmt.Fprintf(stdout, "stopped: budget of $%.2f reached\n", opts.MaxUSD)
		return 3
	case "limit":
		fmt.Fprintf(stdout, "stopped: %s\n", res.LimitHit)
		return 3
	case "errors":
		return 1
	}
	return 0
}

// writePrivate writes the report readable by its owner only: it quotes the
// project's knowledge and code, and an existing file keeps its old mode unless
// it is set again.
func writePrivate(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func joinTypes(m map[string]bool) string {
	var ts []string
	for t := range m {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return strings.Join(ts, "/")
}

// gardenMeter counts what the run uses against the daily and monthly limits.
// With a database the figures are the persisted ones, so earlier runs and the
// other job on the same day count; without one only this run counts.
type gardenMeter struct {
	st     gardenState
	limits llmusage.Limits
	now    func() time.Time
	run    llmusage.Usage
	err    error
}

func (m *gardenMeter) gate() *llmusage.Reached {
	if !m.limits.Day.Any() && !m.limits.Month.Any() {
		return nil
	}
	day, month := m.run, m.run
	if m.st != nil {
		t := m.now().UTC()
		var err error
		if day, err = m.st.LLMUsageOn(llmusage.JobGarden, t.Format("2006-01-02")); err != nil {
			return &llmusage.Reached{Cause: err}
		}
		if month, err = m.st.LLMUsageMonth(llmusage.JobGarden, t.Format("2006-01")); err != nil {
			return &llmusage.Reached{Cause: err}
		}
	}
	return m.limits.Check(day, month)
}

func (m *gardenMeter) record(v garden.Verdict, u llm.Usage, failed bool) {
	rec := llmusage.Usage{Calls: 1, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, CostMicroUSD: llmusage.MicroUSD(v.CostUSD)}
	if failed {
		rec.Failures = 1
	}
	m.run = m.run.Add(rec)
	if m.st != nil {
		if err := m.st.AddLLMUsage(llmusage.JobGarden, m.now().UTC().Format("2006-01-02"), rec); err != nil && m.err == nil {
			m.err = err
		}
	}
}
