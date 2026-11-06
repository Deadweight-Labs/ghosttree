package garden

import (
	"context"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// SessionSpec is one session's task.
type SessionSpec struct {
	Name   string
	Prompt string
	// System is the standing instruction of the session. Empty means the one of
	// the knowledge sessions (AgentSystemPrompt).
	System    string
	MaxTurns  int
	BudgetUSD float64
}

// AgentOptions drive an agent run.
type AgentOptions struct {
	Runner     SessionRunner
	Project    string
	Pricing    Pricing
	MaxUSD     float64
	DryRun     bool
	Sessions   int
	PerSession int
	Turns      int
	// Limit caps the entries of the whole run (0 = sessions times per-session).
	Limit int
	// Known is every active entry of the project, which relations may name.
	Known map[int64]Entry
	Gate  func() *llmusage.Reached
	// OnSession sees every session that ran, for metering.
	OnSession func(s SessionStat, failed bool)
	OnJudged  func(v Verdict)

	// Ledger and Ghosts are the tasks of job 4 (open requests, whose criteria the
	// checkout may show as fulfilled) and job 1 (files whose description is out of
	// date or missing), already chosen and rotated. They run in sessions of their
	// own, with their own standing instruction, before the knowledge sessions; a
	// failure of theirs never stops those. Empty means the job is off.
	Ledger       []LedgerRequest
	Ghosts       []GhostPath
	GhostSamples []GhostSample
	// OnLedgerChecked and OnGhostLooked see every request and path a session
	// went through, so the caller can remember it (verdict is "proposed" or "kept").
	OnLedgerChecked func(r LedgerRequest)
	OnGhostLooked   func(g GhostPath, verdict string)
}

// The kinds of session of an agent run.
const (
	taskKnowledge = "knowledge"
	taskLedger    = "ledger"
	taskGhosts    = "ghosts"
)

// sessionTask is one session to run, whatever it is about.
type sessionTask struct {
	kind    string
	name    string
	prompt  string
	system  string
	turns   int
	entries int
	pkg     Package
	reqs    []LedgerRequest
	ghosts  []GhostPath
}

// chunkEven splits xs into the fewest pieces of at most per elements, as even as
// it can: 9 elements make 5 and 4, not 8 and 1.
func chunkEven[T any](xs []T, per int) [][]T {
	if len(xs) == 0 || per < 1 {
		return nil
	}
	n := (len(xs) + per - 1) / per
	out := make([][]T, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, xs[i*len(xs)/n:(i+1)*len(xs)/n])
	}
	return out
}

// extraTasks are the sessions of jobs 4 and 1.
func (opt AgentOptions) extraTasks() []sessionTask {
	var out []sessionTask
	turns := opt.Turns
	if turns > ledgerMaxTurns {
		turns = ledgerMaxTurns
	}
	chunks := chunkEven(opt.Ledger, ledgerPerSession)
	for i, c := range chunks {
		out = append(out, sessionTask{kind: taskLedger, name: fmt.Sprintf("Ledger %d/%d (%d Aufträge)", i+1, len(chunks), len(c)),
			prompt: LedgerPrompt(opt.Project, c, turns), system: LedgerSystemPrompt(), turns: turns, entries: len(c), reqs: c})
	}
	gchunks := ChunkGhosts(opt.Ghosts, ghostsPerSession)
	for i, c := range gchunks {
		name := fmt.Sprintf("Beschreibungen %d/%d (%d Pfade, %s)", i+1, len(gchunks), len(c), ghostArea(c))
		out = append(out, sessionTask{kind: taskGhosts, name: name,
			prompt: GhostPrompt(opt.Project, name, c, opt.GhostSamples, turns), system: GhostSystemPrompt(), turns: turns, entries: len(c), ghosts: c})
	}
	return out
}

// ghostArea names the directories of a session's files.
func ghostArea(gs []GhostPath) string {
	seen := map[string]bool{}
	var dirs []string
	for _, g := range gs {
		d := path.Dir(g.Path)
		if d == "." {
			d = areaRoot
		}
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > 3 {
		return strings.Join(dirs[:3], ", ") + ", …"
	}
	return strings.Join(dirs, ", ")
}

// CheckAgent works through the entries (in rotation order) in a few sessions
// and returns what survived validation. The budget is split over the sessions
// that are left, so one session cannot spend what the others need. The sessions
// of jobs 4 and 1 come first (they are small), the knowledge sessions after them
// share what remains.
func CheckAgent(ctx context.Context, repo *Repo, entries []Entry, opt AgentOptions) Result {
	res := Result{DryRun: opt.DryRun, MaxUSD: opt.MaxUSD, Agent: true}
	total := opt.Sessions * opt.PerSession
	if opt.Limit > 0 && opt.Limit < total {
		total = opt.Limit
	}
	if total > len(entries) {
		total = len(entries)
	}
	res.Deferred = len(entries) - total
	pkgs := Partition(repo, entries[:total], opt.Sessions, opt.PerSession)
	for i, p := range pkgs {
		p.Name = fmt.Sprintf("%d/%d %s", i+1, len(pkgs), p.Name)
		pkgs[i] = p
	}
	res.Packages = pkgs
	tasks := opt.extraTasks()
	for _, p := range pkgs {
		tasks = append(tasks, sessionTask{kind: taskKnowledge, name: p.Name, prompt: PackagePrompt(p, opt.Project, opt.Turns), turns: opt.Turns, entries: len(p.Entries), pkg: p})
	}
	if opt.DryRun {
		for _, t := range tasks {
			sys := t.system
			if sys == "" {
				sys = AgentSystemPrompt(true)
			}
			res.Sessions = append(res.Sessions, SessionStat{Name: t.name, Entries: t.entries, Usage: llm.Usage{InputTokens: EstimateTokens(len(t.prompt) + len(sys))}})
			for _, e := range t.pkg.Entries {
				res.Verdicts = append(res.Verdicts, Verdict{Entry: e, Kind: NotRun})
			}
		}
		res.UpperBoundUSD = opt.MaxUSD
		return res
	}
	rest := func(from int) {
		for _, t := range tasks[from:] {
			for _, e := range t.pkg.Entries {
				res.Verdicts = append(res.Verdicts, Verdict{Entry: e, Kind: NotRun, Reason: "Lauf vorher beendet, nicht geprüft"})
			}
		}
	}
	fails, extraFails := 0, 0
	statusBefore, statusErr := repo.Status()
	printBefore, printErr := repo.Fingerprint()
	if printErr != nil || statusErr != nil {
		// Without a baseline the check after a session would be skipped
		// silently; better to run nothing than sessions nobody can verify.
		res.StoppedBy = "baseline"
		res.LimitHit = "Ausgangszustand des Checkouts nicht lesbar"
		if printErr != nil {
			res.LimitHit += ": " + printErr.Error()
		} else {
			res.LimitHit += ": " + statusErr.Error()
		}
		rest(0)
		return res
	}
	for i, t := range tasks {
		if t.kind != taskKnowledge && extraFails >= maxFails {
			continue
		}
		if opt.Gate != nil {
			if hit := opt.Gate(); hit != nil {
				res.StoppedBy, res.LimitHit = "limit", hit.Error()
				rest(i)
				return res
			}
		}
		left := opt.MaxUSD - res.Spent
		share := left / float64(len(tasks)-i)
		if share <= 0.01 {
			res.StoppedBy = "budget"
			rest(i)
			return res
		}
		// The cap of a session is twice its share, never more than what is left:
		// a session cut off by it loses everything it paid for, so it gets room
		// to finish. Spending is still counted and the later sessions get what
		// remains. A ledger or description session, whose size is small by
		// construction, never takes more than a quarter of the run.
		budget := math.Min(2*share, left)
		if t.kind != taskKnowledge {
			budget = math.Min(budget, opt.MaxUSD/4)
		}
		spec := SessionSpec{Name: t.name, Prompt: t.prompt, System: t.system, MaxTurns: t.turns, BudgetUSD: budget}
		out, err := opt.Runner.RunSession(ctx, spec)
		st := SessionStat{Name: t.name, Entries: t.entries, Turns: out.Turns, Usage: out.Usage, ReportedUSD: out.ReportedUSD, Denials: out.Denials, DeniedCalls: out.DeniedCalls, Tools: out.Tools}
		st.CostUSD = opt.Pricing.CostUsage(out.Usage)
		res.Spent += st.CostUSD
		// The fingerprint of .git/config and .git/hooks is taken first and
		// without git: a planted hook or config must never get to run.
		printAfter, perr := repo.Fingerprint()
		changed := perr != nil || printAfter != printBefore
		if !changed {
			after, serr := repo.Status()
			changed = serr != nil || after != statusBefore
		}
		if changed {
			// Whatever it was, the session changed the checkout it was only
			// meant to read. Nothing it said is taken, and the run ends.
			st.Err = "der Checkout wurde während der Session verändert, Ergebnis verworfen, Lauf beendet"
			res.Sessions = append(res.Sessions, st)
			if opt.OnSession != nil {
				opt.OnSession(st, true)
			}
			for _, e := range t.pkg.Entries {
				res.Verdicts = append(res.Verdicts, Verdict{Entry: e, Kind: Failed, Reason: "Session verworfen: " + st.Err})
			}
			res.StoppedBy = "tamper"
			rest(i + 1)
			return res
		}
		var apply func() (int, []string)
		partial := ""
		if err != nil && out.Text != "" && out.Looked != nil && strings.HasPrefix(err.Error(), "die Session endete mit error_max") {
			// Cut off by a limit: take what it had written, if that parses.
			if a, verr := res.validate(repo, opt, t, out); verr == nil {
				apply, partial, err = a, err.Error(), nil
			}
		}
		if err == nil && out.Looked != nil && len(out.Tools) == 0 {
			err = fmt.Errorf("die Session hat kein einziges Werkzeug benutzt, die Urteile sind ungeprüft")
		}
		if err == nil && partial == "" {
			apply, err = res.validate(repo, opt, t, out)
		}
		if err != nil {
			st.Err = err.Error()
			res.Sessions = append(res.Sessions, st)
			if opt.OnSession != nil {
				opt.OnSession(st, true)
			}
			for _, e := range t.pkg.Entries {
				res.Verdicts = append(res.Verdicts, Verdict{Entry: e, Kind: Failed, Reason: "Session fehlgeschlagen: " + err.Error()})
			}
			if t.kind != taskKnowledge {
				// The extra jobs are not the core: their failures are reported and
				// stop only themselves.
				extraFails++
				continue
			}
			if fails++; fails >= maxFails {
				res.StoppedBy = "errors"
				rest(i + 1)
				return res
			}
			continue
		}
		if t.kind == taskKnowledge {
			fails = 0
		} else {
			extraFails = 0
		}
		st.Judged, st.Rejected = apply()
		if partial != "" {
			st.Err = "Teilergebnis: " + partial
		}
		res.Sessions = append(res.Sessions, st)
		if opt.OnSession != nil {
			opt.OnSession(st, false)
		}
	}
	return res
}

// validate holds the answer of a session to the rules of its kind and returns
// what to do with it: apply adds the result to the run, tells the caller what was
// seen, and returns how many items were judged and what was dropped. Nothing is
// added before apply is called.
func (res *Result) validate(repo *Repo, opt AgentOptions, t sessionTask, out SessionResult) (func() (int, []string), error) {
	switch t.kind {
	case taskLedger:
		v, err := validateLedger(repo, out.Text, t.reqs, out.Looked)
		if err != nil {
			return nil, err
		}
		return func() (int, []string) {
			byID := map[int64]LedgerRequest{}
			for _, r := range t.reqs {
				byID[r.ID] = r
			}
			for _, id := range v.Checked {
				if opt.OnLedgerChecked != nil {
					opt.OnLedgerChecked(byID[id])
				}
			}
			for j := range v.Proofs {
				v.Proofs[j].Session = t.name
			}
			res.LedgerChecked += len(v.Checked)
			res.Proofs = append(res.Proofs, v.Proofs...)
			return len(v.Checked), v.Rejected
		}, nil
	case taskGhosts:
		v, err := validateGhosts(repo, out.Text, t.ghosts, out.Looked)
		if err != nil {
			return nil, err
		}
		return func() (int, []string) {
			for j := range v.Proposals {
				v.Proposals[j].Session = t.name
				if opt.OnGhostLooked != nil {
					opt.OnGhostLooked(v.Proposals[j].GhostPath, "proposed")
				}
			}
			for _, g := range v.Kept {
				if opt.OnGhostLooked != nil {
					opt.OnGhostLooked(g, "kept")
				}
			}
			res.GhostNotes = append(res.GhostNotes, v.Proposals...)
			res.GhostKept = append(res.GhostKept, v.Kept...)
			return len(v.Proposals) + len(v.Kept), v.Rejected
		}, nil
	}
	val, err := validateAnswer(repo, out.Text, t.pkg, opt.Known, out.Looked)
	if err != nil {
		return nil, err
	}
	return func() (int, []string) {
		judged := 0
		for j := range val.Relations {
			val.Relations[j].Session = t.name
		}
		for _, v := range val.Verdicts {
			if v.Kind != NotRun && v.Kind != Failed {
				judged++
				if opt.OnJudged != nil {
					opt.OnJudged(v)
				}
			}
		}
		res.Verdicts = append(res.Verdicts, val.Verdicts...)
		res.Relations = append(res.Relations, val.Relations...)
		res.Covers = append(res.Covers, val.Covers...)
		return judged, val.Rejected
	}, nil
}
