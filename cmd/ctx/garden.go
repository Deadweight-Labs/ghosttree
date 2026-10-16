package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
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

const gardenUsage = `usage: ctx garden check-knowledge [--mode agent|api] [--project <remote>] [--repo <dir>] [--type pitfall,decision]
                                  [--sessions 4] [--turns 60] [--per-session 30] [--session-timeout 40m]
                                  [--claude <path>] [--claude-dir <dir>] [--no-mcp]
                                  [--limit <n>] [--max-usd 2] [--dry-run] [--propose] [--out <file>]
                                  [--db <path>] [--min-age 14]
                                  [--daily-usd N] [--daily-calls N] [--daily-tokens N]
                                  [--monthly-usd N] [--monthly-calls N] [--monthly-tokens N]

The gardener checks pitfalls and decisions against the code and says whether
each one still holds. It proposes, a person decides.

Two ways to run it:

  --mode agent (default)  A few Claude Code sessions (claude -p, stream-json) go
               through the entries together: the entries are split into topics
               (code area of the files they name), one session per package
               checks them against the code, finds duplicates and contradictions
               between entries, and answers with JSON. ctx checks that JSON
               (every verdict needs a citation that exists in the checkout, every
               relation a verbatim quote of each entry) before anything is filed.
               The session can read the checkout (Read, Grep, Glob, only below it; git_log and
               git_show as fixed tools, no Bash) and search ghosttree read-only; it cannot write, has no
               network but the model proxy and ghosttree, never bypasses
               permissions. Every session gets its own empty config directory under
               --claude-dir (required) and an environment of a few variables; no
               key in it. Cost is metered from the stream's token usage, cache
               included, as job garden (one call per turn).
  --mode api   One Messages API call per entry, as before. Needs an API key.

  --sessions   most sessions per run (default 4); --per-session most entries a
               session gets (default 30); --turns most turns of a session (default
               60); --limit caps the entries of the whole run
  --claude, --claude-dir, --no-mcp
               the claude binary; the directory under which each session gets an empty one (also
               GHOSTTREE_GARDEN_CLAUDE_DIR); no ghosttree tools

In api mode it is an option and stays off without an API key: set GHOSTTREE_GARDEN_KEY, or a
"garden" entry with api_key_file or credential in llm.json. Without a key the
command stops and does nothing (a dry run is the one exception: it reads, counts
and calls nothing).

  --state-file the same memory as --db, kept in a JSON file of its own. The
               sandboxed unit uses it, because the session runs in that unit
               and must not reach the database. Not together with --db.

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
	// Agent mode: how many sessions, entries per session and turns per session,
	// and what starts a session.
	Sessions, PerSession, Turns int
	Runner                      garden.SessionRunner
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func cmdGarden(args []string, stdout io.Writer) int {
	if len(args) > 0 && args[0] == "git-mcp" {
		return cmdGardenGitMCP(args[1:], stdout)
	}
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
	stateFile := fs.String("state-file", "", "the same memory as --db, in a JSON file of its own (for a sandbox that cannot reach the database)")
	mode := fs.String("mode", "agent", "agent (Claude Code sessions) or api (one call per entry)")
	sessions := fs.Int("sessions", 4, "agent mode: most sessions per run")
	perSession := fs.Int("per-session", 30, "agent mode: most entries per session")
	turns := fs.Int("turns", 60, "agent mode: most turns per session")
	sessionTimeout := fs.Duration("session-timeout", 40*time.Minute, "agent mode: a session running longer is killed")
	claudeBin := fs.String("claude", os.Getenv("GHOSTTREE_CLAUDE"), "agent mode: path of the claude binary")
	claudeDir := fs.String("claude-dir", os.Getenv("GHOSTTREE_GARDEN_CLAUDE_DIR"), "agent mode: directory under which every session gets its own empty CLAUDE_CONFIG_DIR (required)")
	noMCP := fs.Bool("no-mcp", false, "agent mode: give the sessions no ghosttree tools")
	minAge := fs.Int("min-age", 14, "days; an entry looked at more recently is left out unless it or its code changed (0 = off)")
	limits := addLimitFlags(fs, llmusage.JobGarden, "")
	fs.Usage = func() { fmt.Fprintln(stdout, gardenUsage) }
	// NaN compares false with everything and +Inf is no cap at all, so
	// neither may reach the budget check.
	if fs.Parse(args[1:]) != nil || !(*maxUSD > 0) || math.IsInf(*maxUSD, 0) || *limit < 0 || *minAge < 0 ||
		(*mode != "agent" && *mode != "api") || *sessions < 1 || *perSession < 1 || *turns < 12 || *sessionTimeout <= 0 {
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
	switch {
	case errors.Is(err, llm.ErrGardenDisabled) && *mode == "agent":
		// The sessions are paid for through the settings.json of the claude
		// directory; no key of ours is involved.
	case errors.Is(err, llm.ErrGardenDisabled):
		if !*dryRun {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintln(stdout, "note: no gardener key configured; this dry run calls nothing, a real run would refuse")
	case err != nil:
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
	if !*dryRun && *mode == "api" {
		c, err := llm.New(cfg.Config)
		if err != nil {
			fmt.Fprintf(stdout, "gardener config: %v\n", err)
			return 1
		}
		model = c.(llm.UsageClient)
	}
	opts := gardenOpts{Types: map[string]bool{}, Limit: *limit, MaxUSD: *maxUSD, DryRun: *dryRun, Propose: *propose,
		Limits: lim, MinAge: time.Duration(*minAge) * 24 * time.Hour}
	if *stateFile != "" && *dbPath != "" {
		fmt.Fprintln(stdout, "--db and --state-file exclude each other")
		return 2
	}
	if *stateFile != "" {
		fst, err := openFileState(*stateFile)
		if err != nil {
			fmt.Fprintf(stdout, "open state file: %v\n", err)
			return 1
		}
		opts.State = fst
	} else if *dbPath != "" {
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
	if *mode == "agent" {
		opts.Sessions, opts.PerSession, opts.Turns = *sessions, *perSession, *turns
		if !*dryRun {
			bin, err := garden.FindClaude(*claudeBin)
			if err != nil {
				fmt.Fprintln(stdout, err)
				return 1
			}
			cc := garden.ClaudeConfig{Bin: bin, Model: cfg.Model, RepoRoot: repo.Root, ConfigRoot: *claudeDir,
				Timeout: *sessionTimeout, ParentEnv: os.Environ()}
			if !*noMCP {
				if cc.CtxBin, err = os.Executable(); err != nil {
					fmt.Fprintf(stdout, "cannot find the ctx binary for the ghosttree tools: %v\n", err)
					return 1
				}
			}
			if cfg.APIKey != "" {
				if u, perr := url.Parse(cfg.BaseURL); perr == nil && u.Host == "api.anthropic.com" {
					cc.APIKey = cfg.APIKey
				} else {
					cc.BaseURL, cc.Token = cfg.BaseURL, cfg.APIKey
				}
			}
			if *claudeDir == "" {
				fmt.Fprintln(stdout, garden.ErrNoConfigRoot)
				return 1
			}
			abs, aerr := filepath.Abs(*claudeDir)
			if aerr != nil {
				fmt.Fprintf(stdout, "claude dir: %v\n", aerr)
				return 1
			}
			*claudeDir, cc.ConfigRoot = abs, abs
			if err := os.MkdirAll(*claudeDir, 0o700); err != nil {
				fmt.Fprintf(stdout, "claude dir: %v\n", err)
				return 1
			}
			cc.DenyRead = agentDenyRead(repo.Root, *claudeDir, *dbPath, cfg.APIKeyFile)
			if *stateFile != "" {
				cc.DenyRead = append(cc.DenyRead, *stateFile)
			}
			opts.Runner = garden.ClaudeRunner{Config: cc}
		}
		return runGardenAgent(client.New(sc), repo, project, opts, pricing, *out, stdout)
	}
	return runGardenCheck(client.New(sc), repo, project, opts, model, pricing, *out, stdout)
}

// cmdGardenGitMCP serves the fixed git tools of an agent session on stdio. It
// is started by Claude Code from the session's MCP config, never by hand.
func cmdGardenGitMCP(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("garden git-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("repo", "", "checkout the git tools look at")
	if fs.Parse(args) != nil || *root == "" || !filepath.IsAbs(*root) {
		fmt.Fprintln(os.Stderr, "usage: ctx garden git-mcp --repo <absolute path>")
		return 2
	}
	repo, err := garden.OpenRepo(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "git-mcp: %v\n", err)
		return 1
	}
	if err := garden.RunGitServer(context.Background(), repo, version); err != nil {
		fmt.Fprintf(os.Stderr, "git-mcp: %v\n", err)
		return 1
	}
	return 0
}

// agentDenyRead lists what a session may not read even though the tools could
// reach it: the usual credential places under HOME, the session's own state,
// the database, the llm.json directory and the key file. Anything that contains
// the checkout is left out, or the session could read nothing.
func agentDenyRead(root, claudeDir, db, keyFile string) []string {
	var cand []string
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{".config", ".claude", ".ssh", ".gnupg", ".aws", ".local/share"} {
			cand = append(cand, filepath.Join(home, d))
		}
	}
	cand = append(cand, claudeDir, db, keyFile)
	if p := os.Getenv("GHOSTTREE_LLM_CONFIG"); p != "" {
		cand = append(cand, filepath.Dir(p))
	} else if d, err := os.UserConfigDir(); err == nil {
		cand = append(cand, filepath.Join(d, "ghosttree"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cand {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		if abs == root || strings.HasPrefix(root, abs+string(filepath.Separator)) {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
		if db == c {
			// SQLite keeps its journal next to the file.
			out = append(out, abs+"-wal", abs+"-shm")
		}
	}
	return out
}

// verifyCheckout refuses a checkout whose origin is not the project being
// checked. Against the wrong repository nearly every symbol is missing, and
// every entry would come back stale.
func verifyCheckout(root, project string) error {
	var origin string
	if out, err := garden.SafeGit(root, "config", "--get", "remote.origin.url").Output(); err == nil {
		origin = scope.NormalizeRemote(strings.TrimSpace(string(out)))
	}
	if origin == "" {
		return fmt.Errorf("%s has no origin remote, so it cannot be matched to %s", root, project)
	}
	if origin != scope.NormalizeRemote(project) {
		return fmt.Errorf("%s is a checkout of %s, not of %s; refusing to compare them", root, origin, project)
	}
	return nil
}

// gardenDue reads the active entries of the types asked for, oldest
// observation first. known is every one of them by id. done is true when the
// run is over already (nothing to check, or the store could not be read) and
// code is its exit status.
func gardenDue(st gardenStore, project string, opts gardenOpts, stdout io.Writer) (entries []garden.Entry, known map[int64]garden.Entry, code int, done bool) {
	all, err := st.ProjectKnowledge(project, false)
	if err != nil {
		fmt.Fprintf(stdout, "cannot read knowledge: %v\n", err)
		return nil, nil, 1, true
	}
	known = map[int64]garden.Entry{}
	for _, k := range all {
		if !opts.Types[k.Type] || k.Status != "active" || k.Scope.Project != project || k.Harness == garden.Harness {
			continue
		}
		e := garden.Entry{ID: k.ID, Type: k.Type, Title: k.Title, Body: k.Body, ObservedAt: k.ObservedAt, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt}
		entries = append(entries, e)
		known[e.ID] = e
	}
	// Oldest observation first: those are the ones most likely to have been
	// overtaken, and a limited run should spend itself there. Rotation then
	// puts entries never looked at before the ones looked at long ago.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Since() < entries[j].Since() })
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "no %s entries in %s\n", joinTypes(opts.Types), project)
		return nil, known, 0, true
	}
	return entries, known, 0, false
}

// runGardenAgent is the agent mode: the entries due are split into packages and
// a few Claude Code sessions check them together.
func runGardenAgent(st gardenStore, repo *garden.Repo, project string, opts gardenOpts, pricing garden.Pricing, outPath string, stdout io.Writer) int {
	entries, known, code, done := gardenDue(st, project, opts, stdout)
	if done {
		return code
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
	aopts := garden.AgentOptions{
		Runner: opts.Runner, Project: project, Pricing: pricing, MaxUSD: opts.MaxUSD, DryRun: opts.DryRun,
		Sessions: opts.Sessions, PerSession: opts.PerSession, Turns: opts.Turns, Limit: opts.Limit, Known: known,
		Gate: meter.gate, OnSession: meter.recordSession,
	}
	if opts.State != nil {
		aopts.OnJudged = func(v garden.Verdict) {
			err := opts.State.SaveGardenCheck(project, store.GardenCheck{KnowledgeID: v.Entry.ID, CheckedAt: now().UTC().Format(time.RFC3339Nano),
				Commit: head, EntryHash: garden.HashOf(v.Entry), Verdict: v.Kind})
			if err != nil {
				fmt.Fprintf(stdout, "could not remember the check of #%d: %v\n", v.Entry.ID, err)
			}
		}
	}
	if !opts.DryRun && opts.Runner == nil {
		fmt.Fprintln(stdout, "agent mode without a session runner")
		return 1
	}
	res := garden.CheckAgent(context.Background(), repo, entries, aopts)
	res.Rotation = rot
	if meter.err != nil {
		fmt.Fprintf(stdout, "could not record usage: %v\n", meter.err)
	}
	titles := map[int64]string{}
	for id, e := range known {
		titles[id] = e.Title
	}
	return finishGarden(st, project, opts, res, outPath, stdout, titles)
}

func runGardenCheck(st gardenStore, repo *garden.Repo, project string, opts gardenOpts, model llm.UsageClient, pricing garden.Pricing, outPath string, stdout io.Writer) int {
	entries, _, code, done := gardenDue(st, project, opts, stdout)
	if done {
		return code
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
	return finishGarden(st, project, opts, res, outPath, stdout, nil)
}

// finishGarden writes the report, files the proposals and turns the way the
// run ended into an exit status. It is the same for both modes.
func finishGarden(st gardenStore, project string, opts gardenOpts, res garden.Result, outPath string, stdout io.Writer, titles map[int64]string) int {
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
		rn, err := garden.ProposeRelations(st, project, existing, res.Relations, titles)
		if err != nil {
			fmt.Fprintf(stdout, "%v (%d proposals filed before the failure; the report above is complete)\n", err, n+rn)
			return 1
		}
		fmt.Fprintf(stdout, "%s %d proposals filed as staged notes from the gardener (see 'ctx review')", garden.Headline(res.Verdicts), n+rn)
		if rn > 0 {
			fmt.Fprintf(stdout, ", %d of them about duplicates or contradictions between entries", rn)
		}
		fmt.Fprintln(stdout)
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
	case "errors", "tamper", "baseline":
		if res.StoppedBy == "baseline" {
			fmt.Fprintf(stdout, "stopped: %s\n", res.LimitHit)
		}
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

// recordSession counts one agent session. Its calls are its turns, each of which
// is one request to the proxy.
func (m *gardenMeter) recordSession(st garden.SessionStat, failed bool) {
	calls := st.Turns
	if calls < 1 {
		calls = 1
	}
	rec := llmusage.Usage{Calls: calls, InputTokens: st.Usage.InputTokens, OutputTokens: st.Usage.OutputTokens,
		CacheReadTokens: st.Usage.CacheReadTokens, CacheWriteTokens: st.Usage.CacheWriteTokens, CostMicroUSD: llmusage.MicroUSD(st.CostUSD)}
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
