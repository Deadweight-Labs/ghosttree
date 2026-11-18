package store

import (
	"errors"
	"testing"
	"time"
)

// REQ-695 (AC-695.3): a gardener run hands its workflow run and its owner's
// alias only to the gardener's account, and its token, which the sweep never
// ends, holds at most aiExternalTokenTTL.
func TestGardenRunModelAndTheTokenOfAnExternalRun(t *testing.T) {
	st, o, _ := aiFixture(t)
	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	c := aiConn(t, st, "person:1", o.ID, "team", AIFormatAnthropic, "claude-haiku-5-5")
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "gaertner", ConnectionID: c.ID, Model: "claude-haiku-5-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGardenSettings("person:1", GardenSettings{OrgID: o.ID, Enabled: true, Frequency: GardenDaily, BudgetRun: 3, BudgetMon: 90, ModelAlias: "gaertner"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.StartGardenRun("person:2", o.ID, aiProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GardenRunModel("person:1", id); !errors.Is(err, ErrNotGardener) {
		t.Fatalf("another account asks: %v", err)
	}
	run, alias, err := st.GardenRunModel("person:2", id)
	if err != nil || alias != "gaertner" || run.Workflow != GardenBlock || run.TriggerID != -1 || run.Project != aiProject {
		t.Fatalf("model of the run: %+v %q %v", run, alias, err)
	}
	if r, err := st.AIRouteFor(run, alias); err != nil || r.Connection.ID != c.ID {
		t.Fatalf("route: %+v %v", r, err)
	}
	token, err := st.IssueAIRunToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRunOfToken(token); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
	defer func(f func() time.Time) { aiNow = f }(aiNow)
	later := time.Now().UTC().Add(aiExternalTokenTTL + time.Minute)
	aiNow = func() time.Time { return later }
	if _, err := st.AIRunOfToken(token); !errors.Is(err, ErrAIToken) {
		t.Fatalf("token of an external run after %v: %v", aiExternalTokenTTL, err)
	}
	aiNow = time.Now
	if err := st.FinishGardenRun("person:2", id, "done", 0.5, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GardenRunModel("person:2", id); !errors.Is(err, ErrGardenAutoGone) {
		t.Fatalf("a finished run: %v", err)
	}
	var left int
	st.db.QueryRow(`SELECT COUNT(*) FROM ai_run_tokens WHERE run_id=?`, run.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("tokens left after the run: %d", left)
	}
}
