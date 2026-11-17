package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Reasons a run of a definition is skipped.
const (
	// SkipDisabled: the workflow was switched off after its run was queued.
	SkipDisabled = "disabled"
	// SkipInvalid: the newest version no longer passes the check (a block
	// left the catalogue, say).
	SkipInvalid = "invalid"
	// SkipAuthorRights: the account that saved the version it runs may no
	// longer act for the project (demoted below lead, removed, disabled). A run
	// acts for that account; without the right it does not start (REQ-695).
	SkipAuthorRights = "author_rights"
)

// skipError ends a run as skipped with a reason instead of failed.
type skipError struct{ reason string }

func (e skipError) Error() string { return "skipped: " + e.reason }

// Skip makes a resolver error that skips the run with the reason.
func Skip(reason string) error { return skipError{reason} }

// DefResolver resolves a run to what it executes (REQ-695): a run of a stored
// workflow takes the newest version of its definition at the moment it starts
// (DEC-45: a saved change applies to the next run), checks it again and hands
// the block of the catalogue the definition and its inputs. A run that names a
// block directly (a trigger of stage E0, ghosttree/echo) runs that block.
type DefResolver struct {
	Store   *store.Store
	Catalog Catalog
	// PRDiff reads the diff of a pull request where the server can (its GitHub
	// App); nil means the changed files stand in for it (stage E3).
	PRDiff func(ctx context.Context, project string, pr int64) (string, error)
	// Research searches the net for a block that takes it, when the
	// definition asks and the owner's policy allows; nil: no research.
	Research Researcher
}

// NewDefResolver is the resolver of a server with the default catalogue.
func NewDefResolver(st *store.Store) DefResolver {
	return DefResolver{Store: st, Catalog: DefaultCatalog()}
}

func (r DefResolver) Resolve(ctx context.Context, run store.WorkflowRun) (Block, string, error) {
	if strings.Contains(run.Workflow, "/") {
		return r.Catalog.Builtins().Resolve(ctx, run)
	}
	def, err := r.Store.WorkflowDefByName(run.Project, run.Workflow)
	if errors.Is(err, store.ErrWorkflowDefMissing) {
		return nil, "", ErrUnknownWorkflow
	}
	if err != nil {
		return nil, "", err
	}
	version := store.WorkflowVersionLabel(def.Version)
	if !def.Enabled {
		return nil, version, Skip(SkipDisabled)
	}
	d, rep := Check(def.Text, Env{Catalog: r.Catalog})
	if !rep.OK() {
		return nil, version, Skip(SkipInvalid)
	}
	// The owner's policy as it is now: a workflow saved before the owner
	// forbade what it does does not run (REQ-695).
	policy := r.Store.WorkflowPolicyOfProject(run.Project)
	if v := PolicyOf(policy).Violation(d); v != "" {
		return nil, version, Skip(v)
	}
	spec, _ := r.Catalog.Lookup(d.Uses)
	if spec.Run == nil {
		return nil, version, Skip(SkipExternal)
	}
	inner := spec.Run
	run.WorkflowVersion = version
	// The run acts for the author of this version, with the rights the author
	// holds now, not the ones the author had when saving it.
	if r.Store.WorkflowRunAccount(run) > 0 && r.Store.WorkflowRunActor(run) == 0 {
		return nil, version, Skip(SkipAuthorRights)
	}
	return BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
		// The inputs of the run: what the definition declares and what the
		// block always reads, assembled with the rights of the account the
		// run acts for.
		d.Inputs = mergeNames(d.Inputs, spec.Inputs)
		asm := newAssembler(r.Store, run, in.Event, r.Store.WorkflowRunActor(run), r.PRDiff)
		if d.Research && spec.Research && r.Research != nil {
			if q := researchQuery(asm); q != "" {
				if found, err := r.Research(ctx, q); err == nil && strings.TrimSpace(found) != "" {
					asm.data.Research = cut(found, maxDiffChars/2)
					d.Inputs = mergeNames(d.Inputs, []string{"research"})
				}
			}
		}
		in.Def = &d
		in.Values = InputValues(d.Inputs, in.Event)
		for _, n := range d.Inputs {
			if spec, ok := InputByName(n); (ok && spec.Assembled) || n == "research" {
				if v, ok := asm.value(ctx, n); ok {
					if in.Values == nil {
						in.Values = map[string]string{}
					}
					in.Values[n] = v
				}
			}
		}
		in.Data = &asm.data
		var results []Result
		in.Propose = func(r Result) { results = append(results, r) }
		out, err := inner.Run(ctx, in)
		if err != nil || len(results) == 0 || ctx.Err() != nil {
			return out, err
		}
		// Evidence counts only from the declared inputs, never from the prompt
		// or the instruction around them.
		g := Gate{Store: r.Store, Run: run, Def: &d, Policy: policy, Inputs: inputsText(&d, in.Values), Sources: asm.sources()}
		grep, gerr := g.Apply(results)
		if gerr != nil {
			return out, gerr
		}
		if out.Result != "" {
			out.Result += "; "
		}
		out.Result += grep.String()
		return out, nil
	}), version, nil
}

// SkipExternal: the block runs outside the engine (the gardener's unit) and
// is started by its own settings, not by a trigger.
const SkipExternal = "external"

// PolicyOf is the policy of the check from the stored one.
func PolicyOf(p store.WorkflowPolicy) Policy {
	return Policy{DenyInstruction: p.DenyInstruction, DenyPrompt: p.DenyPrompt, DenyResearch: p.DenyResearch, DenyKinds: p.DenyKinds}
}

// InputValues puts together the declared inputs of a run from its event.
func InputValues(names []string, ev store.WorkflowEvent) map[string]string {
	if len(names) == 0 {
		return nil
	}
	raw, _ := json.MarshalIndent(ev, "", "  ")
	out := map[string]string{}
	for _, n := range names {
		if v, ok := inputValue(n, ev, string(raw)); ok {
			out[n] = v
		}
	}
	return out
}

// mergeNames is a followed by what b adds, without repeats.
func mergeNames(a, b []string) []string {
	out := append([]string{}, a...)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// researchQuery is all a research step may send out: the title and the start
// of the summary of the idea, never its concept, its discussion or anything of
// the project beyond it (IDEA-45, security).
func researchQuery(a *assembler) string {
	ws := a.workshop()
	if ws == nil {
		return ""
	}
	q := ws.Idea.Title
	if ws.Summary != nil {
		q += " " + cut(oneLine(ws.Summary.Body), 200)
	}
	return cut(strings.TrimSpace(q), 300)
}

// renderInputs writes the inputs of a run as a block of text for a prompt, in
// the order the definition declares them.
func renderInputs(d *Def, values map[string]string) string {
	if d == nil || len(d.Inputs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, n := range d.Inputs {
		fmt.Fprintf(&b, "<input name=%q>\n%s\n</input>\n", n, values[n])
	}
	return b.String()
}

// inputsText is the text of the declared inputs, against which evidence is
// checked.
func inputsText(d *Def, values map[string]string) string {
	var b strings.Builder
	for _, n := range d.Inputs {
		b.WriteString(values[n])
		b.WriteString("\n")
	}
	return b.String()
}
