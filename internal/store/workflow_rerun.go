package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrWorkflowRerun: the run cannot be started again (it is still queued or
// running, or it is a run of another project).
var ErrWorkflowRerun = errors.New("this run cannot be started again")

// Rerunnable says whether a person may start a run again: once it ended
// without a result (failed, stopped at a budget, skipped). A run that ended
// done already delivered; starting it again would propose everything twice.
func (r WorkflowRun) Rerunnable() bool {
	switch r.State {
	case RunFailed, RunBudget, RunSkipped:
		return true
	}
	return false
}

// RerunWorkflowRun queues the workflow of a run again for the same subject
// (S02, usability run 5). A run is one per event and workflow, so the event
// is copied: same kind, subject and data, the person as origin, dispatched at
// once so that it starts only this workflow and no other trigger. The chain
// depth of the original stays, so that a rerun cannot lengthen a chain.
func (s *Store) RerunWorkflowRun(actorPrincipal string, id int64) (WorkflowRun, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, id}, func(d *Store, p []any) (WorkflowRun, error) {
			return d.RerunWorkflowRun(p[0].(string), p[1].(int64))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRun{}, err
	}
	defer tx.Rollback()
	old, err := scanWorkflowRun(tx.QueryRow(`SELECT `+workflowRunCols+workflowRunFrom+` WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowRun{}, ErrWorkflowRerun
	}
	if err != nil {
		return WorkflowRun{}, err
	}
	if !old.Rerunnable() {
		return WorkflowRun{}, ErrWorkflowRerun
	}
	ev, err := scanWorkflowEvent(tx.QueryRow(`SELECT `+workflowEventCols+` FROM workflow_events WHERE id=?`, old.EventID))
	if err != nil {
		return WorkflowRun{}, err
	}
	ts := now()
	data, err := json.Marshal(ev.Data)
	if err != nil || ev.Data == nil {
		data = []byte("{}")
	}
	key := fmt.Sprintf("rerun:%d:%d", old.ID, time.Now().UnixNano())
	res, err := tx.Exec(`INSERT INTO workflow_events(kind,project,org_id,subject_kind,subject_id,data,origin_kind,origin_person,origin_run_id,chain_depth,dedupe_key,created_at,dispatched_at)
		VALUES(?,?,?,?,?,?,?,?,0,?,?,?,?)`,
		ev.Kind, ev.Project, ev.OrgID, ev.SubjectKind, ev.SubjectID, string(data), OriginHuman, actorPrincipal, ev.ChainDepth, key, ts, ts)
	if err != nil {
		return WorkflowRun{}, err
	}
	evID, _ := res.LastInsertId()
	res, err = tx.Exec(`INSERT INTO workflow_runs(org_id,project,workflow,trigger_id,event_id,chain_depth,state,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		old.OrgID, old.Project, old.Workflow, old.TriggerID, evID, old.ChainDepth, RunQueued, ts)
	if err != nil {
		return WorkflowRun{}, err
	}
	runID, _ := res.LastInsertId()
	run, err := scanWorkflowRun(tx.QueryRow(`SELECT `+workflowRunCols+workflowRunFrom+` WHERE r.id=?`, runID))
	if err != nil {
		return WorkflowRun{}, err
	}
	return run, tx.Commit()
}

// RerunWorkflowRun starts a run of the project again; who may edit the
// project's workflows may (leads and owners).
func (a *ProjectAccess) RerunWorkflowRun(project string, id int64) (WorkflowRun, error) {
	if err := a.Check(project, ResWorkflow, ActEdit, Object{}); err != nil {
		return WorkflowRun{}, err
	}
	run, _, err := a.st.WorkflowRunByID(id)
	if err != nil || run.Project != project {
		return WorkflowRun{}, ErrAccessNotFound
	}
	return a.st.RerunWorkflowRun(a.principal.ID, id)
}

// WorkflowProposalsWaiting counts the results of a project's runs that wait
// for a person on the review page.
func (s *Store) WorkflowProposalsWaiting(project string) int {
	if s.reader != nil {
		return s.reader.WorkflowProposalsWaiting(project)
	}
	var n int
	_ = s.db.QueryRowContext(s.ctx(), `SELECT COUNT(*) FROM workflow_run_results WHERE project=? AND state=?`, project, ResultProposed).Scan(&n)
	return n
}
