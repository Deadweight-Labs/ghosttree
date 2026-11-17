package store

import (
	"errors"
	"testing"
	"time"
)

// gardenerRun starts a run of the gardener's unit for the owner o and gives
// it a token for the model proxy. The gardener's alias and settings are set.
func gardenerRun(t *testing.T, st *Store, o Org, budgetRun float64) (int64, WorkflowRun, string) {
	t.Helper()
	if err := st.SetGardenerAccount(2); err != nil {
		t.Fatal(err)
	}
	c := aiConn(t, st, "person:1", o.ID, "team", AIFormatAnthropic, "claude-haiku-5-5")
	if _, err := st.SetAIAlias("person:1", AIAlias{Level: AILevelOwner, OrgID: o.ID, Alias: "gaertner", ConnectionID: c.ID, Model: "claude-haiku-5-5"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGardenSettings("person:1", GardenSettings{OrgID: o.ID, Enabled: true, Frequency: GardenDaily, BudgetRun: budgetRun, BudgetMon: 90, ModelAlias: "gaertner"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.StartGardenRun("person:2", o.ID, aiProject)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := st.GardenRunModel("person:2", id)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.IssueAIRunToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id, run, token
}

// Review of #287, 1: a restart of the server ends the runs of the engine and
// their tokens, but not the gardener's run, which goes on outside the engine:
// its token holds, so that the model stays reachable for the rest of the run.
func TestRestartKeepsTheTokenOfTheGardenersRun(t *testing.T) {
	st, o, _ := aiFixture(t)
	_, garden, gToken := gardenerRun(t, st, o, 3)
	engine := aiRun(t, st, aiProject, "wf/a")
	eToken, err := st.IssueAIRunToken(engine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.AbandonWorkflowRuns(); err != nil || n != 1 {
		t.Fatalf("abandon: %d %v", n, err)
	}
	if _, err := st.AIRunOfToken(eToken); !errors.Is(err, ErrAIToken) {
		t.Fatalf("token of the interrupted run: %v", err)
	}
	if run, err := st.AIRunOfToken(gToken); err != nil || run.ID != garden.ID {
		t.Fatalf("token of the gardener's run after the restart: %v", err)
	}
}

// Review of #285, 8: a gardener's run whose unit was killed hard never reports.
// The sweep leaves it for as long as its token holds, and then ends it as
// interrupted, together with its row in the gardener's tab.
func TestSweepEndsAGardenerRunOnceItsTokenHasRunOut(t *testing.T) {
	st, o, _ := aiFixture(t)
	id, run, _ := gardenerRun(t, st, o, 3)
	if st.StaleWorkflowRuns(time.Now().Add(time.Hour)) != 0 {
		t.Fatal("a fresh gardener run counts as stale")
	}
	if n, err := st.AbandonStaleWorkflowRuns(time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("sweep after one hour: %d %v", n, err)
	}
	old := time.Now().UTC().Add(-aiExternalTokenTTL - time.Minute).Format(time.RFC3339)
	if _, err := st.db.Exec(`UPDATE workflow_runs SET started_at=? WHERE id=?`, old, run.ID); err != nil {
		t.Fatal(err)
	}
	if st.StaleWorkflowRuns(time.Now()) != 1 {
		t.Fatal("a gardener run older than its token is not counted")
	}
	if n, err := st.AbandonStaleWorkflowRuns(time.Now()); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	got, _, err := st.WorkflowRunByID(run.ID)
	if err != nil || got.State != RunFailed || got.Reason != "interrupted" || got.FinishedAt == "" {
		t.Fatalf("run after the sweep: %+v %v", got, err)
	}
	runs, _ := st.GardenRuns(o.ID, 5)
	if len(runs) != 1 || runs[0].ID != id || runs[0].State != "failed" || runs[0].Note != "interrupted" {
		t.Fatalf("gardener's tab after the sweep: %+v", runs)
	}
}

// Review of #287, 4: at the proxy the budget per run in the gardener's
// settings is the cap of a gardener's run, not the owner's cap per run; the
// owner's cap for the month still holds.
func TestGardenerRunIsCappedByItsOwnBudgetNotTheOwnersRunCap(t *testing.T) {
	st, o, _ := aiFixture(t)
	_, run, _ := gardenerRun(t, st, o, 3)
	// Claude Code asks for 32k output tokens: about one dollar in the estimate.
	if err := st.SetAICap("person:1", AICap{OrgID: o.ID, RunUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	r, err := st.AIRouteFor(run, "gaertner")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Remaining != 3 || r.TightestCap != AICapRun {
		t.Fatalf("the run may spend %v under %s, want 3 under %s", r.Remaining, r.TightestCap, AICapRun)
	}
	id, err := st.ReserveAICall(r, 1)
	if err != nil {
		t.Fatalf("a call of one dollar under the owner's run cap of 0.5: %v", err)
	}
	var be *AIBudgetError
	if err := st.ReleaseAICall(id); err != nil {
		t.Fatal(err)
	}
	// The month of the owner holds all the same.
	if err := st.SetAICap("person:1", AICap{OrgID: o.ID, RunUSD: 0.5, MonthUSD: 0.8}); err != nil {
		t.Fatal(err)
	}
	r, err = st.AIRouteFor(run, "gaertner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveAICall(r, 1); !errors.As(err, &be) || be.Cap != AICapOwner {
		t.Fatalf("a call over the owner's month: %v", err)
	}
	// A call over the gardener's own budget ends the run.
	if err := st.SetAICap("person:1", AICap{OrgID: o.ID, RunUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	r, err = st.AIRouteFor(run, "gaertner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveAICall(r, 3.5); !errors.As(err, &be) || be.Cap != AICapRun || !be.Final {
		t.Fatalf("a call over the gardener's budget: %v", err)
	}
}

// Review of #286, 5: a monthly cap that only other runs' open reservations fill
// refuses the call and leaves the run unmarked, so that its next call goes
// through once they are settled or released. A cap that settled calls used up
// ends the run.
func TestMonthlyCapFilledByOtherRunsReservationsDoesNotMarkTheRun(t *testing.T) {
	st, o, _ := aiFixture(t)
	aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	if err := st.SetAICap("person:1", AICap{OrgID: o.ID, MonthUSD: 1}); err != nil {
		t.Fatal(err)
	}
	a, b := aiRun(t, st, aiProject, "wf/a"), aiRun(t, st, aiProject, "wf/b")
	ra, _ := st.AIRouteFor(a, "gpt-a")
	rb, _ := st.AIRouteFor(b, "gpt-a")
	open, err := st.ReserveAICall(ra, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	var be *AIBudgetError
	if _, err := st.ReserveAICall(rb, 0.5); !errors.As(err, &be) || be.Cap != AICapOwner || be.Final {
		t.Fatalf("a call that does not fit beside an open reservation: %v", err)
	}
	if u, _ := st.AIRunUsageOf(b.ID); u.Cap != "" {
		t.Fatalf("run b was marked with %q by a call that only did not fit", u.Cap)
	}
	// The route itself is refused as well when the open reservations fill the cap.
	if _, err := st.ReserveAICall(ra, 0.1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRouteFor(b, "gpt-a"); !errors.As(err, &be) || be.Cap != AICapOwner || be.Final {
		t.Fatalf("route beside open reservations: %v", err)
	}
	if err := st.ReleaseAICall(open); err != nil {
		t.Fatal(err)
	}
	rb, err = st.AIRouteFor(b, "gpt-a")
	if err != nil {
		t.Fatalf("after the release run b may call: %v", err)
	}
	if _, err := st.ReserveAICall(rb, 0.5); err != nil {
		t.Fatalf("a call after the release: %v", err)
	}
	// Settled spending that uses the month up does end the run.
	if _, err := st.RecordAICall(AICall{RunID: a.ID, Route: ra, CostUSD: 1, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AIRouteFor(a, "gpt-a"); !errors.As(err, &be) || be.Cap != AICapOwner || !be.Final {
		t.Fatalf("a call over settled spending: %v", err)
	}
	if u, _ := st.AIRunUsageOf(a.ID); u.Cap != AICapOwner {
		t.Fatalf("run a after settled spending: %+v", u)
	}
}

// Review of #286, 6: a reservation left open by a process that died is booked
// at its estimate when the next one starts, and no longer stands open.
func TestOpenReservationsAreBookedAtTheEstimateAtStart(t *testing.T) {
	st, o, _ := aiFixture(t)
	aiConn(t, st, "person:1", o.ID, "openai-a", AIFormatOpenAI, "gpt-a")
	run := aiRun(t, st, aiProject, "wf/a")
	r, _ := st.AIRouteFor(run, "gpt-a")
	if _, err := st.ReserveAICall(r, 0.25); err != nil {
		t.Fatal(err)
	}
	done, err := st.ReserveAICall(r, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SettleAICall(done, AICall{CostUSD: 0.1, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.SettleOpenAICalls(); err != nil || n != 1 {
		t.Fatalf("settle open: %d %v", n, err)
	}
	var open int
	var cost float64
	st.db.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE status=?`, aiCallPending).Scan(&open)
	st.db.QueryRow(`SELECT cost_usd FROM ai_calls WHERE status=499`).Scan(&cost)
	if open != 0 || cost != 0.25 {
		t.Fatalf("open reservations %d, the booked one costs %v", open, cost)
	}
	if u, _ := st.AIRunUsageOf(run.ID); u.CostUSD != 0.35 || u.Calls != 2 {
		t.Fatalf("run cost: %+v", u)
	}
	if n, _ := st.SettleOpenAICalls(); n != 0 {
		t.Fatalf("second start: %d", n)
	}
}
