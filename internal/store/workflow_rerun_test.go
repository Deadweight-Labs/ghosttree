package store

import (
	"errors"
	"strconv"
	"testing"
)

// A rerun (S02, usability run 5) does not walk around the dispatcher's guards:
// a run stopped by the hourly cap or the chain guard is not started again, a
// switched-off trigger starts nothing, and reruns count toward the cap.
func TestARerunKeepsTheGuardsOfTheDispatcher(t *testing.T) {
	old := WorkflowTriggerHourlyCap
	WorkflowTriggerHourlyCap = 2
	defer func() { WorkflowTriggerHourlyCap = old }()
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	for i := 0; i < 3; i++ {
		if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: prProject, SubjectKind: "idea", SubjectID: strconv.Itoa(i),
			Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "rerun-guard:" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	mustDispatch(t, st)
	capped := runsOf(t, st, prProject, WorkflowRunFilter{State: RunSkipped})
	if len(capped) != 1 || capped[0].Reason != SkipRate || capped[0].Rerunnable() {
		t.Fatalf("run over the cap: %+v", capped)
	}
	if _, err := st.RerunWorkflowRun("person:1", capped[0].ID); !errors.Is(err, ErrWorkflowRerun) {
		t.Fatalf("rerun of a capped run: %v", err)
	}
	// End the two queued runs as failed: rerunnable, but the cap is full.
	var failed int64
	for {
		run, _, ok, err := st.ClaimWorkflowRun()
		if err != nil || !ok {
			break
		}
		if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunFailed, Reason: "LLM HTTP 404: x"}); err != nil {
			t.Fatal(err)
		}
		failed = run.ID
	}
	if _, err := st.RerunWorkflowRun("person:1", failed); !errors.Is(err, ErrWorkflowRerun) {
		t.Fatalf("rerun over the hourly cap: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE workflow_runs SET created_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	run, err := st.RerunWorkflowRun("person:1", failed)
	if err != nil || run.State != RunQueued {
		t.Fatalf("rerun after the hour: %+v %v", run, err)
	}
	if (WorkflowRun{State: RunSkipped, Reason: SkipChainDepth}).Rerunnable() || (WorkflowRun{State: RunDone}).Rerunnable() {
		t.Fatal("a chain-stopped or done run is offered again")
	}
	// A switched-off trigger starts nothing again.
	if _, err := st.db.Exec(`UPDATE workflow_runs SET created_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetWorkflowTriggerAsOperator(WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RerunWorkflowRun("person:1", failed); !errors.Is(err, ErrWorkflowRerun) {
		t.Fatalf("rerun with the trigger off: %v", err)
	}
}
