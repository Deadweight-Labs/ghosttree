package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/garden"
	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const gardenUsage = `usage: ctx garden check-knowledge [--project <remote>] [--repo <dir>] [--type pitfall,decision]
                                  [--limit <n>] [--max-usd 2] [--dry-run] [--propose] [--out <file>]

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
  --out        write the Markdown report to a file instead of stdout`

// gardenStore is what the command needs from the server: read the entries,
// and, with --propose, add new ones. There is no patch or delete in it.
type gardenStore interface {
	ProjectKnowledge(project string, includeArchived bool) ([]store.Knowledge, error)
	Remember(k store.Knowledge, autoCtx scope.Axes) (store.Knowledge, error)
}

type gardenOpts struct {
	Types   map[string]bool
	Limit   int
	MaxUSD  float64
	DryRun  bool
	Propose bool
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
	fs.Usage = func() { fmt.Fprintln(stdout, gardenUsage) }
	if fs.Parse(args[1:]) != nil || *maxUSD <= 0 || *limit < 0 {
		fmt.Fprintln(stdout, gardenUsage)
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
	pricing := garden.Pricing{InUSDPerMTok: cfg.InputUSDPerMTok, OutUSDPerMTok: cfg.OutputUSDPerMTok}

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
	opts := gardenOpts{Types: map[string]bool{}, Limit: *limit, MaxUSD: *maxUSD, DryRun: *dryRun, Propose: *propose}
	for _, t := range strings.Split(*types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			opts.Types[t] = true
		}
	}
	return runGardenCheck(client.New(sc), repo, project, opts, model, pricing, *out, stdout)
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
		entries = append(entries, garden.Entry{ID: k.ID, Type: k.Type, Title: k.Title, Body: k.Body, ObservedAt: k.ObservedAt, CreatedAt: k.CreatedAt})
	}
	// Oldest observation first: those are the ones most likely to have been
	// overtaken, and a limited run should spend itself there.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Since() < entries[j].Since() })
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "no %s entries in %s\n", joinTypes(opts.Types), project)
		return 0
	}

	res := garden.Check(context.Background(), repo, entries, garden.Options{
		Model: model, Pricing: pricing, MaxUSD: opts.MaxUSD, DryRun: opts.DryRun, Limit: opts.Limit,
	})
	var report bytes.Buffer
	garden.WriteReport(&report, project, res, time.Now())

	if opts.Propose && !opts.DryRun {
		existing, err := st.ProjectKnowledge(project, true)
		if err != nil {
			fmt.Fprintf(stdout, "cannot read existing proposals: %v\n", err)
			return 1
		}
		n, err := garden.Propose(st, project, existing, res.Verdicts)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(&report, "\n%d Vorschläge als staged-Notiz mit Herkunft gardener abgelegt (ctx review).\n", n)
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, report.Bytes(), 0o644); err != nil {
			fmt.Fprintf(stdout, "write report: %v\n", err)
			return 1
		}
		verb := "spent"
		if opts.DryRun {
			verb = "estimated"
		}
		fmt.Fprintf(stdout, "report: %s (%d entries, %s $%.4f)\n", outPath, len(res.Verdicts), verb, res.Spent)
	} else {
		stdout.Write(report.Bytes())
	}
	switch res.StoppedBy {
	case "budget":
		fmt.Fprintf(stdout, "stopped: budget of $%.2f reached\n", opts.MaxUSD)
		return 3
	case "errors":
		return 1
	}
	return 0
}

func joinTypes(m map[string]bool) string {
	var ts []string
	for t := range m {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return strings.Join(ts, "/")
}
