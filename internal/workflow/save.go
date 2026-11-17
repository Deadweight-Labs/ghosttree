package workflow

import (
	"errors"
	"slices"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// ErrCheck: the definition did not pass the check; the report says why.
var ErrCheck = errors.New("the workflow does not pass the check")

// EnvFor is the environment of a check on the server for a project: the
// catalogue, the aliases a run there can use and the owner's cap for a run.
// Every way of saving (web, MCP, API) checks against the same environment.
func EnvFor(st *store.Store, project string) Env {
	env := Env{Catalog: DefaultCatalog()}
	aliases, run, err := st.WorkflowModelAliases(project)
	if err != nil {
		return env
	}
	env.Alias = func(name string) bool { return slices.Contains(aliases, name) }
	env.OwnerRunUSD = run
	return env
}

// SaveDef checks a definition and stores it as a new version: the one way
// the web interface, the MCP tool and the API save a workflow, so that all
// three give the same version on the server. A definition with errors is not
// stored (ErrCheck, with the report).
func SaveDef(st *store.Store, principal, project, name, text, note, via string, base int) (store.WorkflowDefSaved, CheckReport, error) {
	d, rep := Check(text, EnvFor(st, project))
	if !rep.OK() {
		return store.WorkflowDefSaved{}, rep, ErrCheck
	}
	saved, err := st.SaveWorkflowDef(principal, store.WorkflowDefSave{Project: project, Name: name, Text: text, Note: note, Via: via, Base: base,
		Event: d.On, Branches: d.Branches, EveryMinutes: d.Every, AllowChain: d.Chain, Uses: d.Uses, Summary: d.Description,
		RunUSD: d.RunUSD, MonthUSD: d.MonthUSD})
	return saved, rep, err
}
