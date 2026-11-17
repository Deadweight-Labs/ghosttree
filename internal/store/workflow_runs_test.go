package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

func mustTrigger(t *testing.T, st *Store, tr WorkflowTrigger) WorkflowTrigger {
	t.Helper()
	tr.Enabled = true
	got, err := st.SetWorkflowTriggerAsOperator(tr)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustDispatch(t *testing.T, st *Store) WorkflowDispatchReport {
	t.Helper()
	rep, err := st.DispatchWorkflowEvents(0)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func runsOf(t *testing.T, st *Store, project string, f WorkflowRunFilter) []WorkflowRun {
	t.Helper()
	runs, err := st.WorkflowRunsIn(project, f)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// AC-693.1: the merge of a pull request into the default branch is one event
// in the same transaction, the next dispatch makes one run linked to the pull
// request, and a second delivery of the same merge makes neither a second
// event nor a second run.
func TestMergeIntoTheDefaultBranchMakesOneRunEvenWhenDeliveredTwice(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventPRMerged})
	if _, err := st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	if rep := mustDispatch(t, st); rep.Events != 0 {
		t.Fatalf("an opened pull request is no event: %+v", rep)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.RecordPullRequest(prEvent(7, "closed", "merged", "feat/a")); err != nil {
			t.Fatal(err)
		}
	}
	// The same event once more through the general entry, as a retry would.
	if _, created, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventPRMerged, Project: prProject, SubjectKind: "pr", SubjectID: "7",
		Origin: EventOrigin{Kind: OriginSystem}, DedupeKey: EventPRMerged + ":" + prProject + "#7"}); err != nil || created {
		t.Fatalf("a known dedupe key is no new event: created=%v %v", created, err)
	}
	var events int
	st.db.QueryRow(`SELECT COUNT(*) FROM workflow_events WHERE kind='pr.merged'`).Scan(&events)
	if events != 1 {
		t.Fatalf("events after two deliveries: %d", events)
	}
	if rep := mustDispatch(t, st); rep.Events != 1 || rep.Queued != 1 {
		t.Fatalf("the next dispatch queues the run: %+v", rep)
	}
	if rep := mustDispatch(t, st); rep.Events != 0 || rep.Queued != 0 {
		t.Fatalf("a second dispatch does nothing: %+v", rep)
	}
	runs := runsOf(t, st, prProject, WorkflowRunFilter{SubjectKind: "pr", SubjectID: "7"})
	if len(runs) != 1 || runs[0].State != RunQueued || runs[0].Workflow != "ghosttree/echo" || runs[0].EventKind != EventPRMerged || runs[0].ChainDepth != 1 {
		t.Fatalf("runs of PR 7: %+v", runs)
	}
	ev, err := st.WorkflowEventByID(runs[0].EventID)
	if err != nil || ev.Data["default_branch"] != "1" || ev.Data["base"] != "main" || ev.Origin.Kind != OriginSystem {
		t.Fatalf("event: %+v %v", ev, err)
	}
}

func TestMergeIntoAnotherBranchStartsOnlyATriggerForThatBranch(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventPRMerged})
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo@release", EventKind: EventPRMerged, Branch: "release"})
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo@any", EventKind: EventPRMerged, Branch: "*"})
	c := prEvent(8, "closed", "merged", "feat/b")
	c.Base = "release"
	if _, err := st.RecordPullRequest(c); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	var names []string
	for _, r := range runsOf(t, st, prProject, WorkflowRunFilter{}) {
		names = append(names, r.Workflow)
	}
	if len(names) != 2 || names[0] != "ghosttree/echo@any" || names[1] != "ghosttree/echo@release" {
		t.Fatalf("runs for a merge into release: %v", names)
	}
}

// AC-693.2: what a run causes starts nothing without the trigger's leave, and
// with it the chain ends after two stages.
func TestEventsOfARunStartNothingWithoutLeaveAndAtMostTwoStagesWithIt(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	// A member may not switch a workflow on, a lead may.
	if _, err := st.SetWorkflowTrigger(principalOfID(3), WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated, Enabled: true}); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("member switches a workflow on: %v", err)
	}
	tr, err := st.SetWorkflowTrigger(principalOfID(2), WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	create := func(title, ref string) {
		t.Helper()
		if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: title, Body: "x", Actor: idea.Actor{Person: "mia", SessionRef: ref}}); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(id int64) {
		t.Helper()
		run, _, ok, err := st.ClaimWorkflowRun()
		if err != nil || !ok || run.ID != id {
			t.Fatalf("claim %d: %+v %v %v", id, run, ok, err)
		}
		if err := st.FinishWorkflowRun(id, WorkflowRunOutcome{State: RunDone, Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	lastRun := func() WorkflowRun {
		t.Helper()
		runs := runsOf(t, st, roleProject, WorkflowRunFilter{Limit: 1})
		if len(runs) == 0 {
			t.Fatal("no run")
		}
		return runs[0]
	}

	create("Erste Idee eines Menschen", "")
	mustDispatch(t, st)
	first := lastRun()
	if first.State != RunQueued || first.ChainDepth != 1 {
		t.Fatalf("a person's idea starts stage 1: %+v", first)
	}
	finish(first.ID)

	// Without leave: an idea the run writes is skipped with the reason.
	create("Idee aus einem Lauf", WorkflowRunRefPrefix+strconv.FormatInt(first.ID, 10))
	if rep := mustDispatch(t, st); rep.Queued != 0 || rep.Skipped != 1 {
		t.Fatalf("an event of a run without leave: %+v", rep)
	}
	if r := lastRun(); r.State != RunSkipped || r.Reason != SkipChain {
		t.Fatalf("skipped run: %+v", r)
	}

	// With leave: stage 2 runs, what stage 2 causes does not.
	tr.AllowChain = true
	if _, err := st.SetWorkflowTrigger(principalOfID(2), tr); err != nil {
		t.Fatal(err)
	}
	create("Zweite Idee aus dem Lauf", WorkflowRunRefPrefix+strconv.FormatInt(first.ID, 10))
	mustDispatch(t, st)
	second := lastRun()
	if second.State != RunQueued || second.ChainDepth != 2 {
		t.Fatalf("stage 2 with leave: %+v", second)
	}
	finish(second.ID)
	create("Idee aus Stufe zwei", WorkflowRunRefPrefix+strconv.FormatInt(second.ID, 10))
	mustDispatch(t, st)
	if r := lastRun(); r.State != RunSkipped || r.Reason != SkipChainDepth || r.ChainDepth != 3 {
		t.Fatalf("stage 3 never runs: %+v", r)
	}
	// A run reference that names no run, or is malformed, never starts anything.
	create("Idee eines unbekannten Laufs", WorkflowRunRefPrefix+"999999")
	create("Idee eines kaputten Verweises", WorkflowRunRefPrefix+"x")
	mustDispatch(t, st)
	for _, r := range runsOf(t, st, roleProject, WorkflowRunFilter{Limit: 2}) {
		if r.State != RunSkipped {
			t.Fatalf("unknown run origin started a run: %+v", r)
		}
	}
	// An agent's idea counts like a person's: the guard is about runs only.
	if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Idee eines Agenten", Body: "x", Actor: idea.Actor{Person: "mia", Agent: true}}); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	if r := lastRun(); r.State != RunQueued || r.ChainDepth != 1 {
		t.Fatalf("agent's idea: %+v", r)
	}
	ev, _ := st.WorkflowEventByID(lastRun().EventID)
	if ev.Origin.Kind != OriginAgent || ev.Origin.Person != "mia" {
		t.Fatalf("origin of an agent's idea: %+v", ev.Origin)
	}
}

// AC-693.3: the full text of a run goes after the retention of its owner, 90
// days unless the owner set otherwise; cost, result and version stay.
func TestFullTextOfARunGoesAfterTheRetentionAndTheRestStays(t *testing.T) {
	w := newPainWorld(t)
	st := w.st
	mustTrigger(t, st, WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	if _, err := w.pa["mia"].CreateIdea(CreateIdeaInput{Project: roleProject, Title: "Eine Idee", Body: "x", Actor: ideaActor("mia")}); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	run, _, _, err := st.ClaimWorkflowRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone, Result: "zwei Vorschläge", WorkflowVersion: "v3", Connection: "anthropic-team", Model: "haiku",
		CostUSD: 0.042, Inputs: "EINGABE-VOLLTEXT", Output: "ANTWORT-VOLLTEXT"}); err != nil {
		t.Fatal(err)
	}
	created, _ := time.Parse(time.RFC3339, run.CreatedAt)

	org := st.WorkflowSettingsFor(run.OrgID)
	if org.RetentionDays != WorkflowRetentionDefault {
		t.Fatalf("default retention: %+v", org)
	}
	if n, err := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 89)); err != nil || n != 0 {
		t.Fatalf("after 89 days: %d %v", n, err)
	}
	// Only the owner of the organization sets the period.
	if err := st.SetWorkflowSettings(principalOfID(2), WorkflowSettings{OrgID: run.OrgID, RetentionDays: 30}); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("a lead sets the retention: %v", err)
	}
	for _, bad := range []int{0, -1, 3651} {
		if err := st.SetWorkflowSettings(principalOfID(1), WorkflowSettings{OrgID: run.OrgID, RetentionDays: bad}); !errors.Is(err, ErrWorkflowSettings) {
			t.Fatalf("retention %d: %v", bad, err)
		}
	}
	if err := st.SetWorkflowSettings(principalOfID(1), WorkflowSettings{OrgID: run.OrgID, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 29)); n != 0 {
		t.Fatalf("after 29 of 30 days: %d", n)
	}
	got, text, err := st.WorkflowRunByID(run.ID)
	if err != nil || text == nil || text.Inputs != "EINGABE-VOLLTEXT" || text.Output != "ANTWORT-VOLLTEXT" {
		t.Fatalf("text before the end: %+v %v", text, err)
	}
	if n, err := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 30)); err != nil || n != 1 {
		t.Fatalf("after 30 days: %d %v", n, err)
	}
	got, text, err = st.WorkflowRunByID(run.ID)
	if err != nil || text != nil {
		t.Fatalf("text after the end: %+v %v", text, err)
	}
	if got.CostUSD != 0.042 || got.Result != "zwei Vorschläge" || got.WorkflowVersion != "v3" || got.Connection != "anthropic-team" || got.Model != "haiku" || got.TextPurgedAt == "" || got.State != RunDone {
		t.Fatalf("what stays: %+v", got)
	}
	var left int
	st.db.QueryRow(`SELECT COUNT(*) FROM workflow_run_texts`).Scan(&left)
	if left != 0 {
		t.Fatalf("texts left: %d", left)
	}
}

func TestRunsAreForMembersAndOnlyInTheirProject(t *testing.T) {
	w := newPainWorld(t)
	mustTrigger(t, w.st, WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	newIdea(t, w, "mia", "Sichtbarkeit")
	mustDispatch(t, w.st)
	runs, err := w.pa["mia"].WorkflowRuns(roleProject, WorkflowRunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("member: %+v %v", runs, err)
	}
	for _, who := range []string{"gus", "nora"} {
		if _, err := w.pa[who].WorkflowRuns(roleProject, WorkflowRunFilter{}); err == nil {
			t.Fatalf("%s reads runs", who)
		}
		if _, _, err := w.pa[who].WorkflowRun(roleProject, runs[0].ID); err == nil {
			t.Fatalf("%s reads a run", who)
		}
	}
	if _, _, err := w.pa["mia"].WorkflowRun("github.com/dw/other", runs[0].ID); err == nil {
		t.Fatal("a run under another project")
	}
}

func TestScheduleWritesOneEventPerPeriod(t *testing.T) {
	st := prFixture(t)
	tr := mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventSchedule, EveryMinutes: 60})
	if _, err := st.SetWorkflowTriggerAsOperator(WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventSchedule, EveryMinutes: 1, Enabled: true}); !errors.Is(err, ErrWorkflowTrigger) {
		t.Fatalf("a schedule every minute: %v", err)
	}
	at := time.Date(2026, 11, 17, 10, 5, 0, 0, time.UTC)
	for _, step := range []struct {
		at   time.Time
		want int
	}{{at, 1}, {at.Add(50 * time.Minute), 0}, {at.Add(55 * time.Minute), 1}} {
		if n, err := st.EmitWorkflowSchedules(step.at); err != nil || n != step.want {
			t.Fatalf("at %s: %d %v", step.at, n, err)
		}
	}
	if rep := mustDispatch(t, st); rep.Queued != 2 {
		t.Fatalf("schedule runs: %+v", rep)
	}
	runs := runsOf(t, st, prProject, WorkflowRunFilter{SubjectKind: "schedule", SubjectID: strconv.FormatInt(tr.ID, 10)})
	if len(runs) != 2 {
		t.Fatalf("runs of the schedule: %+v", runs)
	}
}

// The runtime store routes every write through the writer; the whole path
// works there too, and an interrupted run does not stay "running" forever.
func TestWorkflowPathThroughTheRuntimeWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wf.db")
	st, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg("person:1", "Alpha", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", roleProject, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetWorkflowTrigger("person:1", WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: roleProject, SubjectKind: "idea", SubjectID: "1",
		Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "idea.created:1"}); err != nil || !created {
		t.Fatalf("event: %v %v", created, err)
	}
	if events, _ := st.WorkflowWorkPending(); !events {
		t.Fatal("the new event is pending")
	}
	if rep, err := st.DispatchWorkflowEvents(10); err != nil || rep.Queued != 1 {
		t.Fatalf("dispatch: %+v %v", rep, err)
	}
	run, ev, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok || ev.SubjectID != "1" || run.State != RunRunning {
		t.Fatalf("claim: %+v %+v %v %v", run, ev, ok, err)
	}
	if n, err := st.AbandonWorkflowRuns(); err != nil || n != 1 {
		t.Fatalf("abandon: %d %v", n, err)
	}
	got, _, err := st.WorkflowRunByID(run.ID)
	if err != nil || got.State != RunFailed || got.Reason != "interrupted" {
		t.Fatalf("abandoned run: %+v %v", got, err)
	}
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone}); !errors.Is(err, ErrWorkflowRun) {
		t.Fatalf("finishing an ended run: %v", err)
	}
}

// ctx github sync catches up on old merges; they are history and start no run.
func TestMergeCaughtUpBySyncIsNoEvent(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventPRMerged, Branch: "*"})
	c := prEvent(9, "sync", "merged", "feat/old")
	if res, err := st.RecordPullRequest(c); err != nil || !res.Merged {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if rep := mustDispatch(t, st); rep.Events != 0 {
		t.Fatalf("a synced merge is an event: %+v", rep)
	}
}
