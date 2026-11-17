package server

import (
	"net/http"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The session reference "workflow-run:<id>" marks everything a workflow run
// writes, and the chain guard (REQ-693) treats an event with that origin as a
// run's own. So only the run may carry it: the prefix is reserved the way the
// gardener's is (gateGardener). A run writes with a Principal of TokenKind
// store.WorkflowRunKind and a RunID, which only the server creates; whatever
// it writes carries that run's reference, whatever the request says. Everyone
// else (people, agents, the gardener) is refused when they name such a
// reference, and the session header with it counts as absent.

// gateWorkflowRunRef refuses a reserved session reference from a caller that
// is not the run it names.
func (a *api) gateWorkflowRunRef(w http.ResponseWriter, r *http.Request, ref string) bool {
	if !store.ReservedForWorkflowRun(ref) {
		return true
	}
	if own := principalOf(r).RunRef(); own != "" && strings.EqualFold(strings.TrimSpace(ref), own) {
		return true
	}
	writeCoded(w, http.StatusForbidden, "workflow_run_reserved", `"workflow-run:" references are reserved for the workflow run they name`)
	return false
}

// runRefOf is the reference a write must carry: the run's own for the token of
// a run, else what the caller named.
func runRefOf(r *http.Request, named string) string {
	if own := principalOf(r).RunRef(); own != "" {
		return own
	}
	return named
}
