package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	spec, _ := r.Catalog.Lookup(d.Uses)
	if spec.Run == nil {
		return nil, version, Skip(SkipInvalid)
	}
	inner := spec.Run
	return BlockFunc(func(ctx context.Context, in Input) (store.WorkflowRunOutcome, error) {
		in.Def = &d
		in.Values = InputValues(d.Inputs, in.Event)
		return inner.Run(ctx, in)
	}), version, nil
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
