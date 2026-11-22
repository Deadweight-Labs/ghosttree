package web

import (
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The cost overview of an organization shows every member what the organization
// spent, but names the workflows, runs and projects only of what the member may
// read; the owner reads all of it.
func TestAICostOverviewNamesOnlyWhatTheViewerMayRead(t *testing.T) {
	e, _ := aiWebTesting(t, time.Second)
	at := time.Now().UTC().Format(time.RFC3339)
	for _, call := range []struct {
		run              int64
		project, wf, via string
	}{
		{41, "github.com/x/never-claimed", "secret-nightly-sweep", "own"},
		{0, "", store.AITestWorkflow, "own"},
	} {
		if _, err := e.St.DB().Exec(`INSERT INTO ai_calls(run_id, org_id, project, workflow, connection_id, connection, via, model, cost_usd, status, created_at)
			VALUES(?,?,?,?,1,'team',?,'modell-x',0.5,200,?)`, call.run, e.org.ID, call.project, call.wf, call.via, at); err != nil {
			t.Fatal(err)
		}
	}
	_, owner := fetchPage(t, e.Owner, e.Base+"/ui/orgs?org=alpha")
	mustContain(t, "owner costs", owner, "secret-nightly-sweep", "never-claimed")
	_, member := fetchPage(t, e.Member, e.Base+"/ui/orgs?org=alpha")
	mustNotContain(t, "member costs", member, "secret-nightly-sweep", "never-claimed", "#41")
	// What the organization spent as a whole is not cut: the member sees both calls.
	mustContain(t, "member costs", member, "2 calls")
}
