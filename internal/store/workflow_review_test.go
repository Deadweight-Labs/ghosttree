package store

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Findings of the review of E0 (REQ-693, PR 280).

// textRun makes one finished run with a full text in the project and returns it.
func textRun(t *testing.T, st *Store, project, key string) WorkflowRun {
	t.Helper()
	if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: project, SubjectKind: "idea", SubjectID: key,
		Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "idea.created:" + project + ":" + key}); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	run, _, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone, Result: "ok", CostUSD: 0.5, Inputs: "TEXT " + key, Output: "TEXT"}); err != nil {
		t.Fatal(err)
	}
	return run
}

func hasText(t *testing.T, st *Store, id int64) bool {
	t.Helper()
	_, text, err := st.WorkflowRunByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return text != nil
}

// Finding 1: retention and cost go by the org the project belongs to now. A
// run created while the project was in org A keeps the retention of A only
// until the project moves to B.
func TestRetentionAndCostFollowTheProjectWhenItMovesToAnotherOrg(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	alpha := mustOrg(t, st, "person:1", "Alpha", "alpha")
	beta := mustOrg(t, st, "person:1", "Beta", "beta")
	const proj = "github.com/alpha/app"
	if _, err := st.ClaimProject("person:1", proj, "alpha"); err != nil {
		t.Fatal(err)
	}
	mustTrigger(t, st, WorkflowTrigger{Project: proj, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	if err := st.SetWorkflowSettings("person:1", WorkflowSettings{OrgID: alpha.ID, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkflowSettings("person:1", WorkflowSettings{OrgID: beta.ID, RetentionDays: 400}); err != nil {
		t.Fatal(err)
	}
	moved := textRun(t, st, proj, "1")
	created, _ := time.Parse(time.RFC3339, moved.CreatedAt)

	// Still in alpha: 30 days.
	if cost, _ := st.WorkflowCostSince(alpha.ID, created.Add(-time.Hour)); cost != 0.5 {
		t.Fatalf("alpha counts the run while the project is there: %v", cost)
	}
	if _, err := st.MoveProject("person:1", proj, "beta"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 31)); err != nil || n != 0 || !hasText(t, st, moved.ID) {
		t.Fatalf("after the move beta's 400 days apply, not alpha's 30: purged %d %v", n, err)
	}
	if a, _ := st.WorkflowCostSince(alpha.ID, created.Add(-time.Hour)); a != 0 {
		t.Fatalf("alpha still counts the cost after the move: %v", a)
	}
	if b, _ := st.WorkflowCostSince(beta.ID, created.Add(-time.Hour)); b != 0.5 {
		t.Fatalf("beta counts the cost after the move: %v", b)
	}
	if n, err := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 401)); err != nil || n != 1 || hasText(t, st, moved.ID) {
		t.Fatalf("after 400 days: %d %v", n, err)
	}

	// And the other way round: a run of beta (400 days) goes after alpha's 30
	// once the project moves back.
	back := textRun(t, st, proj, "2")
	if _, err := st.MoveProject("person:1", proj, "alpha"); err != nil {
		t.Fatal(err)
	}
	createdBack, _ := time.Parse(time.RFC3339, back.CreatedAt)
	if n, err := st.PurgeWorkflowRunTexts(createdBack.AddDate(0, 0, 31)); err != nil || n != 1 || hasText(t, st, back.ID) {
		t.Fatalf("after moving back to alpha: %d %v", n, err)
	}
}

// Finding 9: the retention of a personal namespace is its owner's setting.
func TestRetentionInThePersonalNamespace(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	const proj = "github.com/robin/own"
	p, err := st.ClaimProject("person:1", proj, "")
	if err != nil {
		t.Fatal(err)
	}
	mustTrigger(t, st, WorkflowTrigger{Project: proj, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	if err := st.SetWorkflowSettings("person:2", WorkflowSettings{OrgID: p.OrgID, RetentionDays: 10}); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("someone else sets the retention of a personal namespace: %v", err)
	}
	if err := st.SetWorkflowSettings("person:1", WorkflowSettings{OrgID: p.OrgID, RetentionDays: 10}); err != nil {
		t.Fatal(err)
	}
	run := textRun(t, st, proj, "1")
	created, _ := time.Parse(time.RFC3339, run.CreatedAt)
	if n, _ := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 9)); n != 0 {
		t.Fatalf("after 9 days: %d", n)
	}
	if n, err := st.PurgeWorkflowRunTexts(created.AddDate(0, 0, 10)); err != nil || n != 1 || hasText(t, st, run.ID) {
		t.Fatalf("after 10 days: %d %v", n, err)
	}
}

// Finding 6: one statement per kind, a created_at nobody can read does not
// keep the text for good, and a purged run says when.
func TestPurgeIsOneBatchAndDropsTextOfDamagedRows(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, textRun(t, st, prProject, strconv.Itoa(i)).ID)
	}
	if _, err := st.db.Exec(`UPDATE workflow_runs SET created_at='yesterday-ish' WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	later := time.Now().AddDate(0, 0, 91)
	n, err := st.PurgeWorkflowRunTexts(later)
	if err != nil || n != 5 {
		t.Fatalf("purged %d %v", n, err)
	}
	var left int
	st.db.QueryRow(`SELECT COUNT(*) FROM workflow_run_texts`).Scan(&left)
	var unmarked int
	st.db.QueryRow(`SELECT COUNT(*) FROM workflow_runs WHERE text_purged_at=''`).Scan(&unmarked)
	if left != 0 || unmarked != 0 {
		t.Fatalf("texts left %d, runs without a purge time %d", left, unmarked)
	}
	if n, _ := st.PurgeWorkflowRunTexts(later); n != 0 {
		t.Fatalf("a second purge: %d", n)
	}
}

// Stage E3 writes idea.discussion_closed (DEC-46), so a trigger on it is
// accepted and listed among the kinds (it was refused while nothing wrote it).
func TestATriggerOnAClosedDiscussionIsAcceptedSinceE3(t *testing.T) {
	st := prFixture(t)
	if _, err := st.SetWorkflowTriggerAsOperator(WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaDiscussionClosed, Enabled: true}); err != nil {
		t.Fatalf("trigger on a closed discussion: %v", err)
	}
	if !slices.Contains(WorkflowTriggerKinds, EventIdeaDiscussionClosed) {
		t.Fatal("the trigger kinds leave out idea.discussion_closed")
	}
}

// Finding 3: the second line of defense. One trigger starts at most the cap
// per hour; what is over is written as skipped with the reason, and what the
// chain guard stopped does not count against the cap.
func TestATriggerStartsAtMostTheCapPerHour(t *testing.T) {
	old := WorkflowTriggerHourlyCap
	WorkflowTriggerHourlyCap = 3
	defer func() { WorkflowTriggerHourlyCap = old }()
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	for i := 0; i < 6; i++ {
		if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: prProject, SubjectKind: "idea", SubjectID: strconv.Itoa(i),
			Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "idea.created:flood:" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	rep := mustDispatch(t, st)
	if rep.Queued != 3 || rep.Skipped != 3 {
		t.Fatalf("a flood of 6 with a cap of 3: %+v", rep)
	}
	skipped := runsOf(t, st, prProject, WorkflowRunFilter{State: RunSkipped})
	if len(skipped) != 3 || skipped[0].Reason != SkipRate {
		t.Fatalf("skipped runs: %+v", skipped)
	}
	// An hour later the trigger may start runs again.
	if _, err := st.db.Exec(`UPDATE workflow_runs SET created_at=?`, time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: prProject, SubjectKind: "idea", SubjectID: "late",
		Origin: EventOrigin{Kind: OriginHuman, Person: "robin"}, DedupeKey: "idea.created:flood:late"}); err != nil {
		t.Fatal(err)
	}
	if rep := mustDispatch(t, st); rep.Queued != 1 {
		t.Fatalf("after the hour: %+v", rep)
	}
}

// Finding 3: the reserved prefix is recognized in any spelling, and only the
// token of a run carries a reference of its own.
func TestTheRunReferenceIsReservedAndOnlyARunsPrincipalHasOne(t *testing.T) {
	for ref, want := range map[string]bool{"workflow-run:4": true, "WORKFLOW-RUN:4": true, "  workflow-run:x": true, "workflow-run": false, "my-workflow-run:4": false, "": false} {
		if got := ReservedForWorkflowRun(ref); got != want {
			t.Errorf("ReservedForWorkflowRun(%q) = %v", ref, got)
		}
	}
	if (Principal{ID: "person:1", RunID: 4}).RunRef() != "" || (Principal{ID: "person:1", TokenKind: WebSessionKind, RunID: 4}).RunRef() != "" {
		t.Error("a RunID without the run kind of token counts")
	}
	if got := (Principal{ID: "person:1", TokenKind: WorkflowRunKind, RunID: 4}).RunRef(); got != "workflow-run:4" {
		t.Errorf("the run's reference: %q", got)
	}
	// A session named like a run would let its owner write as the run.
	st := orgStore(t, "robin")
	if _, err := st.UpsertSession(Session{Harness: "claude", ExternalID: "Workflow-Run:3", AccountID: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("session named like a run: %v", err)
	}
	if _, err := st.UpsertSession(Session{Harness: "claude", ExternalID: "plain-session", AccountID: 1}); err != nil {
		t.Fatal(err)
	}
}

// Finding 5: the pull request's runs are found from the events of the
// subject, through workflow_events_subject, not by reading every run of the
// project.
func TestRunsOfOnePullRequestUseTheSubjectIndex(t *testing.T) {
	st := prFixture(t)
	q := workflowRunsOfSubject + ` ORDER BY r.id DESC LIMIT ?`
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN `+q, prProject, "pr", "7", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	all := strings.Join(plan, " | ")
	if !strings.Contains(all, "workflow_events_subject") || strings.Contains(all, "SCAN r") || strings.Contains(all, "SCAN workflow_runs") {
		t.Fatalf("query plan: %s", all)
	}
	// The real list returns the same as before.
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventPRMerged})
	if _, err := st.RecordPullRequest(prEvent(7, "closed", "merged", "feat/a")); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	if runs := runsOf(t, st, prProject, WorkflowRunFilter{SubjectKind: "pr", SubjectID: "7"}); len(runs) != 1 || runs[0].EventKind != EventPRMerged {
		t.Fatalf("runs of PR 7: %+v", runs)
	}
	if runs := runsOf(t, st, "github.com/deadweight-labs/other", WorkflowRunFilter{SubjectKind: "pr", SubjectID: "7"}); len(runs) != 0 {
		t.Fatalf("runs of another project: %+v", runs)
	}
}

// Finding 7: a schedule fires from the first period boundary after it was
// switched on, and a pass in which nothing is due writes nothing.
func TestAScheduleDoesNotFireAtOnceForThePeriodItWasCreatedIn(t *testing.T) {
	st := prFixture(t)
	tr := mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventSchedule, EveryMinutes: 60})
	created, _ := time.Parse(time.RFC3339, tr.CreatedAt)
	if n, err := st.EmitWorkflowSchedules(created); err != nil || n != 0 {
		t.Fatalf("at the moment it was created: %d %v", n, err)
	}
	if n, err := st.EmitWorkflowSchedules(created.Add(30 * time.Second)); err != nil || n != 0 {
		t.Fatalf("still inside the period: %d %v", n, err)
	}
	next := created.Truncate(time.Hour).Add(time.Hour)
	if due, err := schedulesDue(st.db, next); err != nil || len(due) != 1 {
		t.Fatalf("due at the next boundary: %+v %v", due, err)
	}
	if n, err := st.EmitWorkflowSchedules(next); err != nil || n != 1 {
		t.Fatalf("at the next boundary: %d %v", n, err)
	}
	if due, err := schedulesDue(st.db, next.Add(time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("nothing due once the event is written (the idle pass reads only): %+v %v", due, err)
	}
}

// Finding 4: only runs that began long enough ago are ended as interrupted.
func TestOnlyStaleRunningRunsAreAbandoned(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventIdeaCreated, Project: prProject, SubjectKind: "idea", SubjectID: "1",
		Origin: EventOrigin{Kind: OriginHuman}, DedupeKey: "idea.created:s1"}); err != nil {
		t.Fatal(err)
	}
	mustDispatch(t, st)
	run, _, ok, err := st.ClaimWorkflowRun()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// A run another process just started stays.
	if n, err := st.AbandonStaleWorkflowRuns(time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("a fresh run: %d %v", n, err)
	}
	if got, _, _ := st.WorkflowRunByID(run.ID); got.State != RunRunning {
		t.Fatalf("fresh run: %+v", got)
	}
	if n, err := st.AbandonStaleWorkflowRuns(time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("a stale run: %d %v", n, err)
	}
	if got, _, _ := st.WorkflowRunByID(run.ID); got.State != RunFailed || got.Reason != "interrupted" {
		t.Fatalf("stale run: %+v", got)
	}
}

// Finding 4: a run that is not running any more cannot be finished again, and
// a finish with a state that does not end a run is refused (the error paths
// the engine retries around).
func TestFinishRefusesWhatIsNotRunning(t *testing.T) {
	st := prFixture(t)
	mustTrigger(t, st, WorkflowTrigger{Project: prProject, Workflow: "ghosttree/echo", EventKind: EventIdeaCreated})
	run := textRun(t, st, prProject, "1")
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone}); !errors.Is(err, ErrWorkflowRun) {
		t.Fatalf("finishing a finished run: %v", err)
	}
	if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunRunning}); !errors.Is(err, ErrWorkflowRun) {
		t.Fatalf("finishing with a state that is not an end: %v", err)
	}
	if err := st.FinishWorkflowRun(99999, WorkflowRunOutcome{State: RunDone}); !errors.Is(err, ErrWorkflowRun) {
		t.Fatalf("finishing a run that does not exist: %v", err)
	}
}

// Finding 9: events delivered at the same time through the runtime writer
// make one event and one run, and two executors never claim the same run.
func TestConcurrentDeliveryAndClaimsThroughTheRuntimeWriter(t *testing.T) {
	st, err := OpenRuntime(filepath.Join(t.TempDir(), "wf.db"), DefaultWriterConfig())
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
	if _, err := st.SetWorkflowTrigger("person:1", WorkflowTrigger{Project: roleProject, Workflow: "ghosttree/echo", EventKind: EventPRMerged, Branch: "*", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	const prs, deliveries = 8, 6
	var wg sync.WaitGroup
	for pr := 0; pr < prs; pr++ {
		for d := 0; d < deliveries; d++ {
			wg.Add(1)
			go func(pr int) {
				defer wg.Done()
				if _, _, err := st.RecordWorkflowEvent(WorkflowEventInput{Kind: EventPRMerged, Project: roleProject, SubjectKind: "pr", SubjectID: strconv.Itoa(pr),
					Data: map[string]string{"base": "main"}, Origin: EventOrigin{Kind: OriginSystem, Person: "github"},
					DedupeKey: "pr.merged:" + roleProject + "#" + strconv.Itoa(pr)}); err != nil {
					t.Error(err)
				}
				if _, err := st.DispatchWorkflowEvents(50); err != nil {
					t.Error(err)
				}
			}(pr)
		}
	}
	wg.Wait()
	if _, err := st.DispatchWorkflowEvents(50); err != nil {
		t.Fatal(err)
	}
	runs, err := st.WorkflowRunsIn(roleProject, WorkflowRunFilter{})
	if err != nil || len(runs) != prs {
		t.Fatalf("%d deliveries of %d merges made %d runs (%v)", deliveries, prs, len(runs), err)
	}
	var mu sync.Mutex
	claimed := map[int64]int{}
	for ex := 0; ex < 4; ex++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				run, _, ok, err := st.ClaimWorkflowRun()
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claimed[run.ID]++
				mu.Unlock()
				if err := st.FinishWorkflowRun(run.ID, WorkflowRunOutcome{State: RunDone}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if len(claimed) != prs {
		t.Fatalf("claimed %d of %d runs", len(claimed), prs)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("run %d claimed %d times", id, n)
		}
	}
}
