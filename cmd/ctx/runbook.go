package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const runbookUsage = `usage: ctx runbook <command>

  list [--project P] [--machine M]                       the runbooks that apply here
  show NAME [--version N] [--json]                       one runbook: purpose, steps, status, last success
  history NAME                                           every version, who wrote it, which commit, approved or not
  write NAME --file spec.json [--project P | --global] [--machine M] [--note T] [--no-approve]
                                                         a new runbook or a new version (an owner or lead's version is approved by writing it)
  import FILE [--project P] [--machine M] [--no-approve] [--dry-run]
                                                         write the runbooks of a JSON file (see docs/runbooks/example.json)
  approve NAME [--version N]                             approve a version for execution (owner or lead); shows what changed first
  allow NAME [--version N]                               what allowing needs (a person, not a token) and what would change
  allow --db PATH --id N [--version V] --machine M       the operator allows a version on a machine (on the server, shows the diff, needs a terminal)
  revoke --db PATH --id N [--version V] --machine M      the operator takes an allowance back
  report NAME ok|failed|aborted [--version N] [--note T] [--param k=v]... [--deviation kind:step[:title[:command]]]...
                                                         report a run; a deviation becomes a proposal an owner or lead must approve
  proposals NAME [--state open|accepted|rejected]        proposals to change the runbook
  proposal accept|reject ID                              decide a proposal (owner or lead)

Run a runbook with: ctx run NAME [param=value ...] [--target T [--dry-run | --run]]`

const runUsage = `usage: ctx run NAME [param=value ...] [--version N] [--project P]
                 [--target T [--dry-run | --run]]

Without --target: prints the checklist with the parameters filled in.
With --target: a dry run that shows the exact argument lists. Only --run executes,
in the foreground of this process, without a shell, and only when the SERVER says
this exact version (hash) is approved by an owner or lead and allowed on this
machine by a person. A file on this machine allows nothing.`

// Überschreibbar, damit die Tests Ausführung, Eingabe und Terminal ersetzen.
var (
	runbookRunner = func(stdout io.Writer) runbook.Runner {
		return runbook.OSRunner{Stdin: os.Stdin, Stdout: stdout, Stderr: os.Stderr}
	}
	runbookStdin io.Reader = os.Stdin
	runbookIsTTY           = func() bool {
		st, err := os.Stdin.Stat()
		return err == nil && st.Mode()&os.ModeCharDevice != 0
	}
)

// runbookClient liefert den Client mit Anker und Session für schreibende Aufrufe.
func runbookClient(cfg config.Config) *client.Client {
	c := client.New(cfg)
	c.SetAnchorSource(func() gitanchor.Anchor {
		cwd, err := os.Getwd()
		if err != nil {
			return gitanchor.Anchor{}
		}
		return collector.ResolveAnchor(cwd)
	})
	c.SetOriginSession(currentSessionRef())
	return c
}

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

func runbookAxes(cfg config.Config, project, machine string) scope.Axes {
	git := currentGitContext(cfg.Machine).axes
	ax := scope.Axes{Project: git.Project, Machine: cfg.Machine}
	if project != "" {
		ax.Project = scope.NormalizeRemote(project)
	}
	if machine != "" && machine != "here" {
		ax.Machine = machine
	}
	return ax
}

func cmdRunbook(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, runbookUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	// Die Freigabe durch den Betreiber arbeitet direkt auf der Datenbank des
	// Servers und braucht keine Client-Konfiguration; dort gibt es oft keine.
	if sub == "revoke" {
		return runbookOperatorAllow(sub, rest, stdout)
	}
	if sub == "allow" {
		for _, a := range rest {
			if a == "--db" || strings.HasPrefix(a, "--db=") {
				return runbookOperatorAllow(sub, rest, stdout)
			}
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	c := runbookClient(cfg)
	usage := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(stdout, msg)
		}
		fmt.Fprintln(stdout, runbookUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stdout, err)
		return 1
	}
	newFlags := func(name string) *flag.FlagSet {
		fs := flag.NewFlagSet("runbook "+name, flag.ContinueOnError)
		fs.SetOutput(stdout)
		return fs
	}
	switch sub {
	case "list":
		fs := newFlags(sub)
		project := fs.String("project", "", "project (default: the current one)")
		machine := fs.String("machine", "", "machine (default: this one)")
		pos, err := parseInterspersed(fs, rest)
		if err != nil || len(pos) != 0 {
			return usage("")
		}
		list, err := c.ListRunbooks(runbookAxes(cfg, *project, *machine))
		if err != nil {
			return fail(err)
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "no runbooks apply here")
			return 0
		}
		for _, s := range list {
			status := fmt.Sprintf("v%d", s.Head)
			if s.ApprovedVersion != s.Head {
				status += fmt.Sprintf(" (approved: v%d)", s.ApprovedVersion)
				if s.ApprovedVersion == 0 {
					status = fmt.Sprintf("v%d (NOT APPROVED)", s.Head)
				}
			}
			line := fmt.Sprintf("%s  %s  [%s]  %s", s.Name, status, runbook.ScopeLabel(s.Project, s.Machine), s.Purpose)
			if s.Targets > 0 {
				line += fmt.Sprintf("  (%d targets)", s.Targets)
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	case "show", "history":
		fs := newFlags(sub)
		version := fs.Int("version", 0, "version (default: the newest)")
		project := fs.String("project", "", "project (default: the current one)")
		asJSON := fs.Bool("json", false, "JSON output")
		pos, err := parseInterspersed(fs, rest)
		if err != nil || len(pos) != 1 {
			return usage("name one runbook")
		}
		v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), *version)
		if err != nil {
			return fail(err)
		}
		if sub == "history" {
			hist, err := c.RunbookHistory(v.ID)
			if err != nil {
				return fail(err)
			}
			printRunbookHistory(stdout, hist)
			return 0
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(v)
			return 0
		}
		fmt.Fprint(stdout, runbook.Checklist(v, nil))
		return 0
	case "write", "import":
		return runbookWrite(sub, rest, stdout, c, cfg, usage, fail)
	case "approve":
		fs := newFlags(sub)
		version := fs.Int("version", 0, "version (default: the newest)")
		project := fs.String("project", "", "project (default: the current one)")
		pos, err := parseInterspersed(fs, rest)
		if err != nil || len(pos) != 1 {
			return usage("name one runbook")
		}
		v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), *version)
		if err != nil {
			return fail(err)
		}
		hist, err := c.RunbookHistory(v.ID)
		if err != nil {
			return fail(err)
		}
		var prev *runbook.Spec
		for i := range hist {
			if hist[i].Approved && hist[i].Version < v.Version {
				prev = &hist[i].Spec
			}
		}
		fmt.Fprintf(stdout, "Approving %s version %d (written by %s, hash %s) for execution. What changes against the last approved version:\n%s",
			v.Name, v.Version, v.Person, v.Hash[:12], runbook.Diff(prev, v.Spec))
		if err := c.ApproveRunbook(v.ID, v.Version, v.Hash); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "approved. On each machine, ctx runbook allow %s lets that machine run it.\n", v.Name)
		return 0
	case "allow":
		for _, a := range rest {
			if a == "--db" || strings.HasPrefix(a, "--db=") {
				return runbookOperatorAllow(sub, rest, stdout)
			}
		}
		return runbookAllowInfo(rest, stdout, c, cfg, usage, fail)
	case "revoke":
		return runbookOperatorAllow(sub, rest, stdout)
	case "report":
		return runbookReport(rest, stdout, c, cfg, usage, fail)
	case "proposals":
		fs := newFlags(sub)
		state := fs.String("state", "", "open, accepted or rejected (default: all)")
		project := fs.String("project", "", "project (default: the current one)")
		pos, err := parseInterspersed(fs, rest)
		if err != nil || len(pos) != 1 {
			return usage("name one runbook")
		}
		v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), 0)
		if err != nil {
			return fail(err)
		}
		ps, err := c.RunbookProposals(v.ID, *state)
		if err != nil {
			return fail(err)
		}
		if len(ps) == 0 {
			fmt.Fprintln(stdout, "no proposals")
		}
		for _, p := range ps {
			fmt.Fprintf(stdout, "proposal %d [%s] on version %d by %s", p.ID, p.State, p.BaseVersion, p.Person)
			if p.DecidedBy != "" {
				fmt.Fprintf(stdout, ", decided by %s", p.DecidedBy)
			}
			fmt.Fprintln(stdout)
			for _, d := range p.Deviations {
				fmt.Fprintf(stdout, "  %s step %d %s %s\n", d.Kind, d.Step, runbook.Escape(d.Title), runbook.Escape(d.Command))
			}
			if p.Note != "" {
				fmt.Fprintf(stdout, "  note: %s\n", runbook.Escape(p.Note))
			}
		}
		return 0
	case "proposal":
		if len(rest) != 2 || (rest[0] != "accept" && rest[0] != "reject") {
			return usage("proposal accept|reject ID")
		}
		id, err := strconv.ParseInt(rest[1], 10, 64)
		if err != nil || id <= 0 {
			return usage("the proposal needs a number")
		}
		p, err := c.DecideRunbookProposal(id, rest[0] == "accept")
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "proposal %d %s", p.ID, p.State)
		if p.ResultVer > 0 {
			fmt.Fprintf(stdout, ": version %d written and approved", p.ResultVer)
		}
		fmt.Fprintln(stdout)
		return 0
	}
	return usage("")
}

func printRunbookHistory(w io.Writer, hist []runbook.View) {
	for _, h := range hist {
		status := "not approved"
		if h.Approved {
			status = "approved by " + h.ApprovedBy
		}
		fmt.Fprintf(w, "v%d  %s  by %s  hash %s  %s", h.Version, h.CreatedAt, h.Person, h.Hash[:12], status)
		if h.GitAnchor != nil {
			fmt.Fprintf(w, "  commit %s", shortCommit(h.GitAnchor.Commit))
		}
		fmt.Fprintln(w)
		if h.Note != "" {
			fmt.Fprintf(w, "    %s\n", runbook.Escape(h.Note))
		}
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

type runbookImportFile struct {
	Runbooks []struct {
		Name    string          `json:"name"`
		Project string          `json:"project"`
		Machine string          `json:"machine"`
		Note    string          `json:"note"`
		Spec    json.RawMessage `json:"spec"`
	} `json:"runbooks"`
}

func runbookWrite(sub string, rest []string, stdout io.Writer, c *client.Client, cfg config.Config, usage func(string) int, fail func(error) int) int {
	fs := flag.NewFlagSet("runbook "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	file := fs.String("file", "", "the runbook as JSON (write)")
	project := fs.String("project", "", "project (default: the current one)")
	machine := fs.String("machine", "", "machine the runbook is for (\"here\" is this one; default: none)")
	global := fs.Bool("global", false, "no project: the runbook is yours alone")
	note := fs.String("note", "", "what changed")
	noApprove := fs.Bool("no-approve", false, "do not ask for approval")
	dry := fs.Bool("dry-run", false, "import: check the file, write nothing")
	pos, err := parseInterspersed(fs, rest)
	if err != nil || len(pos) != 1 {
		return usage("name the runbook (write) or the file (import)")
	}
	here := cfg.Machine
	resolveMachine := func(m string) string {
		if m == "here" {
			return here
		}
		return m
	}
	if sub == "write" {
		if *file == "" {
			return usage("write needs --file")
		}
		raw, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		spec, err := runbook.ParseSpec(raw)
		if err != nil {
			return fail(err)
		}
		proj := scope.NormalizeRemote(*project)
		if proj == "" && !*global {
			proj = currentGitContext(cfg.Machine).axes.Project
		}
		if proj != "" {
			if proj, err = c.WriteTarget(*project, proj); err != nil {
				return fail(err)
			}
		}
		res, err := c.WriteRunbook(proj, resolveMachine(*machine), pos[0], spec, *note, !*noApprove)
		if err != nil {
			return fail(err)
		}
		printRunbookWritten(stdout, pos[0], res)
		return 0
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return fail(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var in runbookImportFile
	if err := dec.Decode(&in); err != nil {
		return fail(fmt.Errorf("%s: %w", pos[0], err))
	}
	if len(in.Runbooks) == 0 {
		return fail(fmt.Errorf("%s holds no runbooks", pos[0]))
	}
	type item struct {
		name, project, machine, note string
		spec                         runbook.Spec
	}
	var items []item
	for i, rb := range in.Runbooks {
		if err := runbook.ValidName(rb.Name); err != nil {
			return fail(fmt.Errorf("entry %d: %w", i+1, err))
		}
		spec, err := runbook.ParseSpec(rb.Spec)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", rb.Name, err))
		}
		proj := scope.NormalizeRemote(rb.Project)
		switch rb.Project {
		case "current":
			proj = scope.NormalizeRemote(*project)
			if proj == "" {
				proj = currentGitContext(cfg.Machine).axes.Project
			}
			if proj == "" {
				return fail(fmt.Errorf("%s: project \"current\" needs a git checkout or --project", rb.Name))
			}
		}
		m := resolveMachine(rb.Machine)
		if *machine != "" {
			m = resolveMachine(*machine)
		}
		items = append(items, item{rb.Name, proj, m, rb.Note, spec})
	}
	for _, it := range items {
		if *dry {
			fmt.Fprintf(stdout, "%s: valid (%s, %d steps, %d targets)\n", it.name, runbook.ScopeLabel(it.project, it.machine), len(it.spec.Steps), len(it.spec.Targets))
			continue
		}
		res, err := c.WriteRunbook(it.project, it.machine, it.name, it.spec, it.note, !*noApprove)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", it.name, err))
		}
		printRunbookWritten(stdout, it.name, res)
	}
	return 0
}

func printRunbookWritten(w io.Writer, name string, res client.RunbookWritten) {
	what := "unchanged"
	switch {
	case res.NewRunbook:
		what = "created"
	case res.NewVersion:
		what = "new version"
	}
	status := "not approved"
	if res.Approved {
		status = "approved"
	}
	fmt.Fprintf(w, "%s: %s, version %d, %s (hash %s)\n", name, what, res.Version, status, res.Hash[:12])
	if res.ApprovalRequested && !res.Approved {
		fmt.Fprintln(w, "  approval was requested but your role does not allow it: an owner or lead approves with ctx runbook approve")
	}
}

// runbookAllowInfo zeigt, was sich ändert und wie die Freigabe geht. Eine
// Freigabe für eine Maschine entscheidet ein Mensch, nicht dieser Befehl: ein
// Token liegt auf jeder Maschine, auf der ein Agent läuft.
func runbookAllowInfo(rest []string, stdout io.Writer, c *client.Client, cfg config.Config, usage func(string) int, fail func(error) int) int {
	fs := flag.NewFlagSet("runbook allow", flag.ContinueOnError)
	fs.SetOutput(stdout)
	version := fs.Int("version", 0, "version (default: the newest)")
	project := fs.String("project", "", "project (default: the current one)")
	pos, err := parseInterspersed(fs, rest)
	if err != nil || len(pos) != 1 {
		return usage("name one runbook")
	}
	v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), *version)
	if err != nil {
		return fail(err)
	}
	state, err := c.RunbookAllowed(v.ID, v.Version, cfg.Machine)
	if err != nil {
		return fail(err)
	}
	if state.Allowed {
		fmt.Fprintf(stdout, "version %d of %s is allowed on %s (by %s)\n", v.Version, v.Name, cfg.Machine, state.AllowedBy)
		return 0
	}
	fmt.Fprintf(stdout, "%s version %d (hash %s) is not allowed on %s: %s\n", v.Name, v.Version, v.Hash[:12], cfg.Machine, state.Reason)
	fmt.Fprintf(stdout, "Written by %s; approved: %t. Allowing needs a person, not this command:\n", v.Person, v.Approved)
	fmt.Fprintf(stdout, "  an operator runs on the server: ctx runbook allow --db <database> --id %d --version %d --machine %s\n", v.ID, v.Version, cfg.Machine)
	fmt.Fprintln(stdout, "  (the web interface will offer the same as an interactive page)")
	return 3
}

// runbookOperatorAllow ist der Weg des Betreibers mit Datenbankzugriff, wie bei
// den Rollen: er sieht den Unterschied zu der Fassung, die auf der Maschine
// freigegeben war, und bestätigt am Terminal mit dem Anfang des Hashes.
func runbookOperatorAllow(sub string, rest []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("runbook "+sub, flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "", "path to the server's database")
	id := fs.Int64("id", 0, "the runbook's number")
	version := fs.Int("version", 0, "version (default: the newest)")
	machine := fs.String("machine", "", "the machine the version may run on")
	pos, err := parseInterspersed(fs, rest)
	if err != nil || len(pos) != 0 || *db == "" || *id <= 0 || *machine == "" {
		fmt.Fprintln(stdout, runbookUsage)
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	defer st.Close()
	v, err := st.RunbookView(*id, *version)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if sub == "revoke" {
		if err := st.RevokeRunbookAllowance(v.ID, v.Version, *machine); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintf(stdout, "allowance of %s version %d on %s revoked\n", v.Name, v.Version, *machine)
		return 0
	}
	if !v.Approved {
		fmt.Fprintf(stdout, "version %d of %s is not approved by an owner or lead, so it cannot be allowed (ctx runbook approve)\n", v.Version, v.Name)
		return 3
	}
	var old *runbook.Spec
	if versions, _ := st.RunbookAllowedVersions(v.ID, *machine); len(versions) > 0 {
		if hist, err := st.RunbookHistory(v.ID); err == nil {
			for i := range hist {
				if hist[i].Version == versions[0] {
					old = &hist[i].Spec
				}
			}
		}
		fmt.Fprintf(stdout, "Changes against version %d, which is allowed on %s:\n", versions[0], *machine)
	} else {
		fmt.Fprintf(stdout, "Nothing of %s is allowed on %s yet. Everything it contains:\n", v.Name, *machine)
	}
	fmt.Fprint(stdout, runbook.Diff(old, v.Spec))
	fmt.Fprintf(stdout, "\n%s version %d, written by %s, approved by %s, hash %s\n", v.Name, v.Version, v.Person, v.ApprovedBy, v.Hash)
	if !runbookIsTTY() {
		fmt.Fprintln(stdout, "allowing needs a person at a terminal: run this command yourself")
		return 3
	}
	confirm := v.Hash[:8]
	fmt.Fprintf(stdout, "To allow it on %s, type %s: ", *machine, confirm)
	line, _ := bufio.NewReader(runbookStdin).ReadString('\n')
	if strings.TrimSpace(line) != confirm {
		fmt.Fprintln(stdout, "\nnot allowed")
		return 1
	}
	by := "operator"
	if u, err := user.Current(); err == nil {
		by = "operator:" + u.Username
	}
	store.SetRunbookExecution(true)
	if err := st.AllowRunbook(store.RunbookAllowRequest{RunbookID: v.ID, Version: v.Version, Hash: v.Hash, Machine: *machine, Person: by, Via: "cli-db"}); err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	fmt.Fprintf(stdout, "\nallowed: %s version %d on %s. The server runs it only with GHOSTTREE_RUNBOOK_EXEC=1.\n", v.Name, v.Version, *machine)
	return 0
}

func runbookReport(rest []string, stdout io.Writer, c *client.Client, cfg config.Config, usage func(string) int, fail func(error) int) int {
	fs := flag.NewFlagSet("runbook report", flag.ContinueOnError)
	fs.SetOutput(stdout)
	version := fs.Int("version", 0, "version that was followed (default: the newest)")
	project := fs.String("project", "", "project (default: the current one)")
	note := fs.String("note", "", "what happened")
	target := fs.String("target", "", "the executable target, if one was run")
	var params, devs stringsFlag
	fs.Var(&params, "param", "k=v parameter of the run (repeatable)")
	fs.Var(&devs, "deviation", "kind:step[:title[:command]] where the run differed (repeatable)")
	pos, err := parseInterspersed(fs, rest)
	if err != nil || len(pos) != 2 {
		return usage("report NAME ok|failed|aborted")
	}
	result := map[string]string{"ok": runbook.ResultOK, "failed": runbook.ResultFailed, "aborted": runbook.ResultAborted}[pos[1]]
	if result == "" {
		return usage("the result is ok, failed or aborted")
	}
	v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), *version)
	if err != nil {
		return fail(err)
	}
	report := client.RunReport{Version: v.Version, Target: *target, Mode: runbook.ModeChecklist, Result: result, Note: *note, Machine: cfg.Machine, Params: map[string]string{}}
	for _, p := range params {
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			return usage("--param needs k=v")
		}
		report.Params[k] = val
	}
	for _, d := range devs {
		dev, err := runbook.ParseDeviation(d)
		if err != nil {
			return fail(err)
		}
		report.Deviations = append(report.Deviations, dev)
	}
	res, err := c.ReportRun(v.ID, report)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "run %d reported (%s, version %d)\n", res.RunID, result, v.Version)
	if res.ProposalID > 0 {
		fmt.Fprintf(stdout, "the deviation became proposal %d: an owner or lead decides it with ctx runbook proposal accept|reject %d\n", res.ProposalID, res.ProposalID)
	}
	return 0
}

// cmdRun ist ctx run: die Checkliste mit eingesetzten Parametern, der Probelauf
// eines Ziels, oder mit --run die Ausführung im Vordergrund.
func cmdRun(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stdout)
	version := fs.Int("version", 0, "version (default: the newest)")
	project := fs.String("project", "", "project (default: the current one)")
	target := fs.String("target", "", "executable target")
	dry := fs.Bool("dry-run", false, "show what would run (the default for a target)")
	doRun := fs.Bool("run", false, "execute the target")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) == 0 {
		fmt.Fprintln(stdout, runUsage)
		return 2
	}
	if *dry && *doRun {
		fmt.Fprintln(stdout, "--dry-run and --run exclude each other")
		return 2
	}
	if *doRun && *target == "" {
		fmt.Fprintln(stdout, "--run needs --target")
		return 2
	}
	given := map[string]string{}
	for _, p := range pos[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			fmt.Fprintf(stdout, "%q is not param=value\n%s\n", p, runUsage)
			return 2
		}
		given[k] = v
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	c := runbookClient(cfg)
	v, err := c.FindRunbook(pos[0], runbookAxes(cfg, *project, ""), *version)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if *target == "" {
		values, err := v.Spec.BindPartial(given)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 2
		}
		fmt.Fprint(stdout, runbook.Checklist(v, values))
		return 0
	}
	values, err := v.Spec.Bind(given)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	plan, err := v.Spec.Plan(*target, values)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	// Ausführbar ist nur, was Owner oder Lead freigegeben haben UND was dieser
	// Mensch auf dieser Maschine zugelassen hat, und nur in genau der Fassung,
	// die er gesehen hat (der Hash wird hier neu berechnet, nicht geglaubt).
	var blocked []string
	if !v.Approved {
		blocked = append(blocked, fmt.Sprintf("version %d is not approved by an owner or lead (ctx runbook approve %s)", v.Version, v.Name))
	}
	if runbook.Hash(v.Project, v.Machine, v.Name, v.Spec) != v.Hash {
		blocked = append(blocked, "the content does not match its hash")
	}
	state, err := c.RunbookAllowed(v.ID, v.Version, cfg.Machine)
	switch {
	case err != nil:
		blocked = append(blocked, "the server could not say whether this machine may run it: "+err.Error())
	case !state.Allowed:
		blocked = append(blocked, state.Reason+"; allowing needs a person (ctx runbook allow "+v.Name+")")
	}
	if !*doRun {
		fmt.Fprintf(stdout, "Dry run of %s:%s (version %d). Nothing is executed. Each line is one program with its arguments, started without a shell:\n", v.Name, *target, v.Version)
		for i, st := range plan {
			fmt.Fprintf(stdout, "%d. %s\n", i+1, runbook.QuoteArgv(st.Argv))
		}
		if len(blocked) > 0 {
			fmt.Fprintln(stdout, "\n--run would be refused:")
			for _, b := range blocked {
				fmt.Fprintf(stdout, "- %s\n", b)
			}
		} else {
			fmt.Fprintf(stdout, "\nTo execute: ctx run %s --target %s --run\n", v.Name, *target)
		}
		return 0
	}
	if len(blocked) > 0 {
		fmt.Fprintln(stdout, "refusing to run:")
		for _, b := range blocked {
			fmt.Fprintf(stdout, "- %s\n", b)
		}
		return 3
	}
	_, runErr := runbook.Execute(context.Background(), runbookRunner(stdout), plan, stdout)
	result := runbook.ResultOK
	if runErr != nil {
		result = runbook.ResultFailed
	}
	rep := client.RunReport{Version: v.Version, Target: *target, Mode: runbook.ModeExec, Result: result, Params: values, Machine: cfg.Machine}
	if runErr != nil {
		rep.Note = runbook.Escape(runErr.Error())
	}
	res, err := c.ReportRun(v.ID, rep)
	if err != nil {
		fmt.Fprintf(stdout, "warning: the run could not be reported: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "run %d reported (%s, version %d, hash %s)\n", res.RunID, result, v.Version, v.Hash[:12])
	}
	if runErr != nil {
		fmt.Fprintln(stdout, runErr)
		return 1
	}
	return 0
}
