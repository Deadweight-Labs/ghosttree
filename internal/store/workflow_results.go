package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// What a run creates (REQ-695, stage E2). A block hands the engine results of
// declared kinds; the engine checks kind, number and evidence and writes each
// as a proposal a person accepts on the review page, or applies it at once
// where the owner switched on the automatic approval for exactly that kind.
// workflow_run_results is the record of every result: its state, the reason a
// result was refused, what it created, and whether an automatic approval was
// undone. Reasons are codes, never model text: the row stays for good, its
// payload (the proposal's text, needed to apply it on acceptance) goes with the
// run's full text at the end of the retention (KN-4795).

// States of a result.
const (
	ResultProposed = "proposed"
	ResultAccepted = "accepted"
	ResultAuto     = "auto"
	ResultRejected = "rejected"
	ResultUndone   = "undone"
)

// Reasons a result was refused.
const (
	ResultNotDeclared = "not_declared"
	ResultOverMax     = "over_max"
	ResultNoEvidence  = "no_evidence"
	ResultPolicy      = "policy"
	ResultBadTarget   = "bad_target"
	ResultInvalid     = "invalid"
	// ResultExists: the result is already there (a wish that an open issue
	// carries); the record points to it in ref instead of proposing it again.
	ResultExists = "exists"
)

// WorkflowAutoKinds are the kinds of result an owner may let apply without a
// person's acceptance.
var WorkflowAutoKinds = []string{"knowledge", "issue", "criterion", "relation"}

var (
	ErrWorkflowResult = errors.New("unknown or settled workflow result")
)

// WorkflowPolicy is what the owner of a namespace forbids its workflows and
// which kinds of result apply without acceptance.
type WorkflowPolicy struct {
	DenyInstruction bool     `json:"deny_instruction,omitempty"`
	DenyPrompt      bool     `json:"deny_prompt,omitempty"`
	DenyResearch    bool     `json:"deny_research,omitempty"`
	DenyKinds       []string `json:"deny_kinds,omitempty"`
	AutoKinds       []string `json:"auto_kinds,omitempty"`
}

// Auto says whether results of the kind apply without acceptance.
func (p WorkflowPolicy) Auto(kind string) bool { return slices.Contains(p.AutoKinds, kind) }

// WorkflowResult is one result of a run.
type WorkflowResult struct {
	ID      int64  `json:"id"`
	RunID   int64  `json:"run_id"`
	Project string `json:"project"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	// Ref is what the result created: KN-n, REQ-n, a criterion REQ-n#m, a
	// relation's number.
	Ref string `json:"ref,omitempty"`
	// NoteID is the staged entry on the review page that stands for it.
	NoteID    int64  `json:"note_id,omitempty"`
	Payload   string `json:"-"`
	DecidedBy int64  `json:"decided_by,omitempty"`
	CreatedAt string `json:"created_at"`
	UndoneAt  string `json:"undone_at,omitempty"`
}

const workflowResultSchema = `
CREATE TABLE IF NOT EXISTS workflow_run_results(
  id INTEGER PRIMARY KEY,
  run_id INTEGER NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('proposed','accepted','auto','rejected','undone')),
  reason TEXT NOT NULL DEFAULT '',
  ref TEXT NOT NULL DEFAULT '',
  note_id INTEGER NOT NULL DEFAULT 0,
  payload TEXT NOT NULL DEFAULT '',
  decided_by INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  undone_at TEXT NOT NULL DEFAULT '',
  undone_by INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS workflow_run_results_run ON workflow_run_results(run_id, id);
CREATE INDEX IF NOT EXISTS workflow_run_results_note ON workflow_run_results(note_id) WHERE note_id>0;
`

func ensureWorkflowResults(db *sql.DB) error {
	if _, err := db.Exec(workflowResultSchema); err != nil {
		return err
	}
	return addColumnIfMissing(db, "workflow_settings", "policy", "TEXT NOT NULL DEFAULT ''")
}

// WorkflowPolicyFor is the policy of a namespace; the empty policy forbids
// nothing and applies nothing by itself.
func (s *Store) WorkflowPolicyFor(orgID int64) WorkflowPolicy {
	if s.reader != nil {
		return s.reader.WorkflowPolicyFor(orgID)
	}
	return workflowPolicyTx(s.db, orgID)
}

func workflowPolicyTx(q rowQuerier, orgID int64) WorkflowPolicy {
	var raw string
	var p WorkflowPolicy
	if q.QueryRow(`SELECT policy FROM workflow_settings WHERE org_id=?`, orgID).Scan(&raw) == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	return p
}

// WorkflowPolicyOfProject is the policy of the namespace the project belongs
// to now.
func (s *Store) WorkflowPolicyOfProject(project string) WorkflowPolicy {
	if s.reader != nil {
		return s.reader.WorkflowPolicyOfProject(project)
	}
	p, ok := projectTx(s.db, project)
	if !ok {
		return WorkflowPolicy{}
	}
	return workflowPolicyTx(s.db, p.OrgID)
}

// SetWorkflowPolicy stores the policy of a namespace; only its owner may.
func (s *Store) SetWorkflowPolicy(actorPrincipal string, orgID int64, p WorkflowPolicy) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, orgID, p}, func(d *Store, a []any) error {
			return d.SetWorkflowPolicy(a[0].(string), a[1].(int64), a[2].(WorkflowPolicy))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	for _, k := range p.AutoKinds {
		if !slices.Contains(WorkflowAutoKinds, k) {
			return ErrWorkflowSettings
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !orgCanTx(tx, orgID, actor, RightOrgAI) {
		return ErrNotOrgOwner
	}
	if _, err := tx.Exec(`INSERT INTO workflow_settings(org_id,retention_days,policy,updated_by,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(org_id) DO UPDATE SET policy=excluded.policy, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		orgID, WorkflowRetentionDefault, string(raw), actor, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordWorkflowResult writes the record of one result.
func (s *Store) RecordWorkflowResult(r WorkflowResult) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{r}, func(d *Store, p []any) (int64, error) { return d.RecordWorkflowResult(p[0].(WorkflowResult)) })
	}
	res, err := s.db.Exec(`INSERT INTO workflow_run_results(run_id,project,kind,state,reason,ref,note_id,payload,decided_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.Project, r.Kind, r.State, cutRunes(r.Reason, 100), cutRunes(r.Ref, 100), r.NoteID, r.Payload, r.DecidedBy, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const workflowResultCols = `id, run_id, project, kind, state, reason, ref, note_id, payload, decided_by, created_at, undone_at`

func scanWorkflowResult(r rowScanner) (WorkflowResult, error) {
	var w WorkflowResult
	err := r.Scan(&w.ID, &w.RunID, &w.Project, &w.Kind, &w.State, &w.Reason, &w.Ref, &w.NoteID, &w.Payload, &w.DecidedBy, &w.CreatedAt, &w.UndoneAt)
	return w, err
}

// WorkflowResultsOf lists the results of a run in the order they came.
func (s *Store) WorkflowResultsOf(runID int64) ([]WorkflowResult, error) {
	if s.reader != nil {
		return s.reader.WorkflowResultsOf(runID)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+workflowResultCols+` FROM workflow_run_results WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowResult
	for rows.Next() {
		w, err := scanWorkflowResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkflowResultByID reads one result.
func (s *Store) WorkflowResultByID(id int64) (WorkflowResult, error) {
	if s.reader != nil {
		return s.reader.WorkflowResultByID(id)
	}
	w, err := scanWorkflowResult(s.db.QueryRowContext(s.ctx(), `SELECT `+workflowResultCols+` FROM workflow_run_results WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrWorkflowResult
	}
	return w, err
}

// WorkflowResultOfNote is the proposed result a staged entry stands for.
func (s *Store) WorkflowResultOfNote(noteID int64) (WorkflowResult, error) {
	if s.reader != nil {
		return s.reader.WorkflowResultOfNote(noteID)
	}
	w, err := scanWorkflowResult(s.db.QueryRowContext(s.ctx(), `SELECT `+workflowResultCols+` FROM workflow_run_results WHERE note_id=? AND state='proposed'`, noteID))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrWorkflowResult
	}
	return w, err
}

// SettleWorkflowResult moves a result on: proposed to accepted (with what it
// created), auto or accepted to undone. A result in another state is not
// touched (ErrWorkflowResult), so that a double click applies nothing twice.
func (s *Store) SettleWorkflowResult(id int64, from, to, ref string, by int64) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, from, to, ref, by}, func(d *Store, p []any) error {
			return d.SettleWorkflowResult(p[0].(int64), p[1].(string), p[2].(string), p[3].(string), p[4].(int64))
		})
	}
	q := `UPDATE workflow_run_results SET state=?, decided_by=?, ref=CASE WHEN ?<>'' THEN ? ELSE ref END WHERE id=? AND state=?`
	args := []any{to, by, ref, ref, id, from}
	if to == ResultUndone {
		q = `UPDATE workflow_run_results SET state=?, undone_by=?, undone_at=?, ref=CASE WHEN ?<>'' THEN ? ELSE ref END WHERE id=? AND state=?`
		args = []any{to, by, now(), ref, ref, id, from}
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWorkflowResult
	}
	return nil
}

// WorkflowRunAccount is the account a run acts for: the author of the version
// of its workflow it ran with, or whoever switched a block on directly with a
// trigger. 0 when there is none (a trigger the operator set): such a run
// writes nothing.
func (s *Store) WorkflowRunAccount(run WorkflowRun) int64 {
	if s.reader != nil {
		return s.reader.WorkflowRunAccount(run)
	}
	var id int64
	if !strings.Contains(run.Workflow, "/") {
		_ = s.db.QueryRowContext(s.ctx(), `SELECT v.author FROM workflow_def_versions v JOIN workflow_defs d ON d.id=v.def_id
			WHERE d.project=? AND d.name=? AND CAST(v.version AS TEXT)=?`, run.Project, run.Workflow, run.WorkflowVersion).Scan(&id)
		return id
	}
	var by string
	if s.db.QueryRowContext(s.ctx(), `SELECT created_by FROM workflow_triggers WHERE id=?`, run.TriggerID).Scan(&by) == nil {
		if n, err := parsePersonPrincipalID(by); err == nil {
			id = n
		}
	}
	return id
}

// WorkflowRunActor is WorkflowRunAccount while the account may still act for
// the run: it is active and holds at least the role lead in the run's project
// now (the right it needed to save the workflow). 0 otherwise: the account was
// disabled, removed from the project or demoted after it saved the version,
// and the run writes nothing in its name.
func (s *Store) WorkflowRunActor(run WorkflowRun) int64 {
	if s.reader != nil {
		return s.reader.WorkflowRunActor(run)
	}
	account := s.WorkflowRunAccount(run)
	if account <= 0 {
		return 0
	}
	id := principalOfID(account)
	acc, err := s.AccountByPrincipalID(id)
	if err != nil || acc.State != "active" || !s.ProjectRole(run.Project, id).Has(RightWorkflowEdit) {
		return 0
	}
	return account
}

// MarkWorkflowRunVersion records the version a running run executes, as soon
// as it is known, so that its token acts for the author of that version.
func (s *Store) MarkWorkflowRunVersion(id int64, version string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, version}, func(d *Store, p []any) error { return d.MarkWorkflowRunVersion(p[0].(int64), p[1].(string)) })
	}
	_, err := s.db.Exec(`UPDATE workflow_runs SET workflow_version=? WHERE id=? AND state='running'`, cutRunes(version, 100), id)
	return err
}

// PrincipalOfRunToken makes the token of a running run a principal (REQ-695):
// the account the run acts for, with TokenKind WorkflowRunKind and the run's
// number, so that the reserved reference "workflow-run:<id>" goes on whatever
// it writes. A token of a run that ended, or of a run that acts for nobody,
// is no principal.
func (s *Store) PrincipalOfRunToken(token string) (Principal, bool) {
	if s.reader != nil {
		return s.reader.PrincipalOfRunToken(token)
	}
	run, err := s.AIRunOfToken(token)
	if err != nil {
		return Principal{}, false
	}
	account := s.WorkflowRunActor(run)
	if account <= 0 {
		return Principal{}, false
	}
	id := principalOfID(account)
	acc, err := s.AccountByPrincipalID(id)
	if err != nil {
		return Principal{}, false
	}
	return Principal{ID: id, Label: acc.Name, TokenKind: WorkflowRunKind, RunID: run.ID}, true
}
