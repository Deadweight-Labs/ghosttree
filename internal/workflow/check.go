package workflow

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Policy is what the owner of a namespace forbids its workflows (REQ-695):
// an instruction (stage 2), an own prompt (stage 3), research on the net and
// single kinds of result. A workflow that breaks it does not run.
type Policy struct {
	DenyInstruction bool     `json:"deny_instruction,omitempty"`
	DenyPrompt      bool     `json:"deny_prompt,omitempty"`
	DenyResearch    bool     `json:"deny_research,omitempty"`
	DenyKinds       []string `json:"deny_kinds,omitempty"`
}

// Policy reasons; the run page words them.
const (
	PolicyInstruction = "policy.instruction"
	PolicyPrompt      = "policy.prompt"
	PolicyResearch    = "policy.research"
	PolicyKind        = "policy.kind"
)

// Violation is the first rule of the policy the definition breaks, as a
// reason (PolicyKind carries the kind after a colon); empty when it breaks none.
func (p Policy) Violation(d Def) string {
	switch {
	case d.Stage() == StagePrompt && p.DenyPrompt:
		return PolicyPrompt
	case d.Stage() == StageInstruction && p.DenyInstruction:
		return PolicyInstruction
	case d.Research && p.DenyResearch:
		return PolicyResearch
	}
	for _, r := range d.Results {
		if slices.Contains(p.DenyKinds, r.Kind) {
			return PolicyKind + ":" + r.Kind
		}
	}
	return ""
}

// Env is what a check knows beyond the file: the catalogue, the policy of the
// owner, and, on the server, which model aliases resolve and the owner's cap
// for one run. A check without Alias leaves aliases unchecked.
type Env struct {
	Catalog Catalog
	Policy  Policy
	// Alias says whether a model name resolves for the project; nil: unknown.
	Alias func(name string) bool
	// OwnerRunUSD is the owner's cap for one run, 0 when none is set.
	OwnerRunUSD float64
}

// CheckReport is the result of a check: the problems, and what the workflow would
// read, write and cost at most. It never calls a model.
type CheckReport struct {
	Stage    int           `json:"stage"`
	Event    string        `json:"event,omitempty"`
	Branches []string      `json:"branches,omitempty"`
	Every    int           `json:"every,omitempty"`
	Block    string        `json:"block,omitempty"`
	External bool          `json:"external,omitempty"`
	Reads    []string      `json:"reads,omitempty"`
	Writes   []ResultLimit `json:"writes,omitempty"`
	Model    string        `json:"model,omitempty"`
	// MaxRunUSD is the most one run may cost: the budget of the file, else the
	// owner's cap for a run; 0 for a block that calls no model. Unbounded is
	// true when a block calls a model and neither is set.
	MaxRunUSD   float64   `json:"max_run_usd"`
	MaxMonthUSD float64   `json:"max_month_usd,omitempty"`
	Unbounded   bool      `json:"unbounded,omitempty"`
	Research    bool      `json:"research,omitempty"`
	Aliases     bool      `json:"aliases_checked"`
	Errors      []Problem `json:"errors,omitempty"`
	Warnings    []Problem `json:"warnings,omitempty"`
}

// OK says whether the definition may be saved and run.
func (r CheckReport) OK() bool { return len(r.Errors) == 0 }

// lineOf finds the line of a key in the text, for problems found after parsing.
func lineOf(text, key string) int {
	for i, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, key+":") {
			return i + 1
		}
	}
	return 0
}

// Check parses a definition and checks it against the catalogue, the policy
// and the aliases of env.
func Check(text string, env Env) (Def, CheckReport) {
	d, probs := Parse(text)
	rep := CheckReport{Errors: probs, Stage: d.Stage(), Event: d.On, Branches: d.Branches, Every: d.Every, Model: d.Model,
		MaxMonthUSD: d.MonthUSD, Research: d.Research, Aliases: env.Alias != nil}
	if env.Catalog == nil {
		env.Catalog = DefaultCatalog()
	}
	errf := func(key, format string, args ...any) {
		rep.Errors = append(rep.Errors, Problem{Line: lineOf(text, key), Key: key, Msg: fmt.Sprintf(format, args...)})
	}
	// itemf is an error about one item of a key (a pattern, an input, a kind
	// of result), so that the block view puts it at that item.
	itemf := func(key, item, format string, args ...any) {
		rep.Errors = append(rep.Errors, Problem{Line: lineOf(text, key), Key: key, Item: item, Msg: fmt.Sprintf(format, args...)})
	}
	warnf := func(key, format string, args ...any) {
		rep.Warnings = append(rep.Warnings, Problem{Line: lineOf(text, key), Key: key, Msg: fmt.Sprintf(format, args...)})
	}
	if len(probs) > 0 && d.On == "" && d.Uses == "" {
		return d, rep
	}
	// Trigger and filter.
	switch {
	case d.On == "":
		errf(KeyOn, "on is missing: one of %s", strings.Join(store.WorkflowTriggerKinds, ", "))
	case !slices.Contains(store.WorkflowTriggerKinds, d.On):
		errf(KeyOn, "unknown event %s (known: %s)", d.On, strings.Join(store.WorkflowTriggerKinds, ", "))
	}
	if len(d.Branches) > 0 && d.On != store.EventPRMerged {
		errf(KeyBranches, "branches only narrows %s", store.EventPRMerged)
	}
	for _, b := range d.Branches {
		if !validBranchPattern(b) {
			itemf(KeyBranches, b, "branch pattern %q is not valid", b)
		}
	}
	switch {
	case d.On == store.EventSchedule && (d.Every < 5 || d.Every > 7*24*60):
		errf(KeyEvery, "a schedule runs every 5 minutes to 7 days (every: 5 to 10080)")
	case d.On != store.EventSchedule && d.Every != 0:
		errf(KeyEvery, "every only belongs to on: %s", store.EventSchedule)
	}
	// Task: stage 1 and 2 use a block; an own prompt (stage 3) comes with E5.
	if d.Prompt != "" || d.Uses == "" {
		errf(KeyUses, "an own prompt (stage 3) arrives with stage E5; use a block: uses: %s<name> (known: %s)", BlockPrefix, strings.Join(env.Catalog.Names(), ", "))
	}
	if env.Policy.DenyPrompt && d.Stage() == StagePrompt {
		errf(KeyPrompt, "the owner's policy forbids an own prompt (stage 3)")
	}
	var block BlockSpec
	known := false
	if d.Uses != "" {
		block, known = env.Catalog.Lookup(d.Uses)
		if !known {
			errf(KeyUses, "unknown block %s (known: %s)", d.Uses, strings.Join(env.Catalog.Names(), ", "))
		}
	}
	if known && block.External {
		errf(KeyUses, "%s is started by its own settings (the gardener's tab, the distiller's job), not by a workflow file", block.Name)
	}
	if known {
		rep.Block, rep.External = block.Name, block.External
		if len(block.Events) > 0 && d.On != "" && !slices.Contains(block.Events, d.On) {
			errf(KeyOn, "%s does not run on %s (only on %s)", block.Name, d.On, strings.Join(block.Events, ", "))
		}
		if d.Stage() == StageInstruction && !block.Instruction {
			errf(KeyUses, "%s takes no instruction; remove the text below the second ---", block.Name)
		}
		rep.Reads = append(rep.Reads, block.Reads...)
		rep.Reads = append(rep.Reads, block.Inputs...)
	}
	if d.Stage() == StageInstruction && env.Policy.DenyInstruction {
		errf(KeyUses, "the owner's policy forbids an instruction (stage 2)")
	}
	// Inputs.
	for _, in := range d.Inputs {
		spec, ok := InputByName(in)
		switch {
		case !ok:
			itemf(KeyInputs, in, "unknown input %s", in)
		case len(spec.Events) > 0 && d.On != "" && !slices.Contains(spec.Events, d.On):
			itemf(KeyInputs, in, "input %s is not there on %s", in, d.On)
		case slices.Contains(rep.Reads, in):
		default:
			rep.Reads = append(rep.Reads, in)
		}
	}
	// Results.
	for _, r := range d.Results {
		switch {
		case !slices.Contains(ResultKinds, r.Kind):
			itemf(KeyResults, r.Kind, "unknown kind of result %s (known: %s)", r.Kind, strings.Join(ResultKinds, ", "))
			continue
		case known && !slices.Contains(block.Results, r.Kind):
			itemf(KeyResults, r.Kind, "%s cannot create %s", block.Name, r.Kind)
			continue
		case slices.Contains(env.Policy.DenyKinds, r.Kind):
			itemf(KeyResults, r.Kind, "the owner's policy forbids the kind of result %s", r.Kind)
			continue
		}
		rep.Writes = append(rep.Writes, r)
	}
	// Research comes with stage 3.
	if d.Research {
		if env.Policy.DenyResearch {
			errf(KeyResearch, "the owner's policy forbids research on the net")
		} else {
			warnf(KeyResearch, "research on the net arrives with stage E5; this run will not search")
		}
	}
	// Model and budget.
	switch {
	case known && block.Model && d.Model == "":
		errf(KeyModel, "%s calls a model: name a model alias, for example model: klein", block.Name)
	case known && !block.Model && d.Model != "":
		warnf(KeyModel, "%s calls no model; model is ignored", block.Name)
	case d.Model != "" && env.Alias != nil && !env.Alias(d.Model):
		errf(KeyModel, "unknown model alias %s", d.Model)
	}
	if d.RunUSD > 0 && d.MonthUSD > 0 && d.RunUSD > d.MonthUSD {
		errf(KeyBudget, "budget.run_usd is above budget.month_usd")
	}
	if !known || block.Model {
		rep.MaxRunUSD = d.RunUSD
		if env.OwnerRunUSD > 0 && (rep.MaxRunUSD == 0 || env.OwnerRunUSD < rep.MaxRunUSD) {
			rep.MaxRunUSD = env.OwnerRunUSD
		}
		rep.Unbounded = known && rep.MaxRunUSD == 0
	}
	return d, rep
}

// validBranchPattern accepts a branch name or a pattern of path.Match with
// the characters of branch names.
func validBranchPattern(p string) bool {
	if p == "" || len(p) > 250 || strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:\\", r) {
			return false
		}
	}
	_, err := path.Match(p, "x")
	return err == nil
}

// BranchMatches says whether a branch is one of the patterns.
func BranchMatches(patterns []string, branch string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}

// FormatReport words a report for a terminal or an agent: the errors and
// warnings, then what the workflow would read, write and cost at most.
func FormatReport(r CheckReport) string {
	var b strings.Builder
	if r.OK() {
		b.WriteString("ok\n")
	}
	for _, p := range r.Errors {
		b.WriteString("error: " + p.String() + "\n")
	}
	for _, p := range r.Warnings {
		b.WriteString("warning: " + p.String() + "\n")
	}
	fmt.Fprintf(&b, "stage: %d\n", r.Stage)
	if r.Event != "" {
		on := r.Event
		if len(r.Branches) > 0 {
			on += " on " + strings.Join(r.Branches, ", ")
		}
		if r.Every > 0 {
			on += fmt.Sprintf(" every %d minutes", r.Every)
		}
		b.WriteString("on: " + on + "\n")
	}
	if r.Block != "" {
		b.WriteString("block: " + r.Block + "\n")
	}
	reads := "nothing beyond the event"
	if len(r.Reads) > 0 {
		reads = strings.Join(r.Reads, ", ")
	}
	b.WriteString("reads: " + reads + "\n")
	writes := "nothing"
	if len(r.Writes) > 0 {
		var parts []string
		for _, w := range r.Writes {
			parts = append(parts, fmt.Sprintf("%s (at most %d, as proposals)", w.Kind, w.Max))
		}
		writes = strings.Join(parts, ", ")
	}
	b.WriteString("writes: " + writes + "\n")
	switch {
	case r.Unbounded:
		b.WriteString("costs at most: no cap in the file or of the owner; the caps of the connection and the server apply\n")
	case r.MaxRunUSD > 0:
		fmt.Fprintf(&b, "costs at most: %.2f USD a run", r.MaxRunUSD)
		if r.MaxMonthUSD > 0 {
			fmt.Fprintf(&b, ", %.2f USD a month", r.MaxMonthUSD)
		}
		b.WriteString("\n")
	default:
		b.WriteString("costs at most: 0 USD (no model call)\n")
	}
	if r.Model != "" {
		b.WriteString("model: " + r.Model + "\n")
	}
	if !r.Aliases {
		b.WriteString("aliases: not checked (no server)\n")
	}
	return b.String()
}
