package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Workflows, stage E0 (REQ-693, IDEA-45): the server keeps a log of events
// (a pull request merged, an idea created, a discussion closed, a point in
// time) and a general table of runs. A dispatcher turns each new event into
// the runs of the triggers that match it; an executor (internal/workflow)
// takes queued runs one by one and reports how they ended. The full text of a
// run (inputs, answers) lives in a table of its own, so that it can be deleted
// after the retention period of the owner while cost, result and version stay.
//
// Workflows themselves come with stage E2. Until then a trigger names a
// workflow by its name only (a built-in block such as ghosttree/echo).

// Kinds of events.
const (
	EventPRMerged             = "pr.merged"
	EventIdeaCreated          = "idea.created"
	EventIdeaDiscussionClosed = "idea.discussion_closed"
	EventSchedule             = "schedule"
)

// WorkflowEventKinds lists every kind of event the log accepts.
var WorkflowEventKinds = []string{EventPRMerged, EventIdeaCreated, EventIdeaDiscussionClosed, EventSchedule}

// WorkflowTriggerKinds lists the kinds of event a trigger may be set on. It
// leaves out idea.discussion_closed: nothing writes that event before stage E3,
// so a trigger on it would look switched on and never fire.
var WorkflowTriggerKinds = []string{EventPRMerged, EventIdeaCreated, EventSchedule}

// Where an event came from.
const (
	OriginHuman  = "human"
	OriginAgent  = "agent"
	OriginRun    = "run"
	OriginSystem = "system"
)

// States of a run.
const (
	RunQueued  = "queued"
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
	RunBudget  = "budget"
	RunSkipped = "skipped"
)

// Reasons a run was skipped by the dispatcher.
const (
	SkipChain      = "chain"
	SkipChainDepth = "chain_depth"
	// SkipRate: the trigger already started WorkflowTriggerHourlyCap runs in the
	// last hour.
	SkipRate = "rate_limit"
)

// WorkflowTriggerHourlyCap is how many runs one trigger may start per hour,
// the second line of defense against chains and floods after the chain guard.
// Runs over the cap are written as skipped with the reason SkipRate. A variable
// so that tests can lower it.
var WorkflowTriggerHourlyCap = 30

const (
	// WorkflowMaxChainDepth is how many runs a chain may hold: a run started by
	// a person's or the system's event is stage 1; an event that run causes may,
	// where the trigger allows chains, start stage 2; what stage 2 causes starts
	// nothing.
	WorkflowMaxChainDepth = 2
	// WorkflowRunRefPrefix marks the session reference of everything a run
	// writes ("workflow-run:42"). An event whose actor carries it has the run
	// as its origin and falls under the chain guard. The prefix is reserved:
	// the API refuses it from everyone but the run itself (a Principal with
	// RunID, see ReservedForWorkflowRun).
	WorkflowRunRefPrefix = "workflow-run:"
	// WorkflowRunKind is the Principal.TokenKind of a run's own token. It arises
	// only on the server, bound to one run (Principal.RunID), never from a request.
	WorkflowRunKind = "workflow-run"
	// WorkflowRetentionDefault is how many days the full text of a run is kept
	// when the owner set nothing.
	WorkflowRetentionDefault = 90
	workflowRetentionMax     = 3650
	workflowTextMax          = 1 << 20
	workflowResultMax        = 2000
)

var (
	ErrWorkflowEvent    = errors.New("invalid workflow event")
	ErrWorkflowTrigger  = errors.New("invalid workflow trigger")
	ErrWorkflowRun      = errors.New("unknown or finished workflow run")
	ErrWorkflowSettings = fmt.Errorf("%w: the full text of runs is kept 1 to 3650 days", ErrInvalidInput)
)

// EventOrigin says who caused an event: a person, an agent, a run (with its
// number) or the system (a webhook, the clock).
type EventOrigin struct {
	Kind   string `json:"kind"`
	Person string `json:"person,omitempty"`
	RunID  int64  `json:"run_id,omitempty"`
}

// OriginOfActor derives the origin of an event from the writer of an entry:
// a session reference of a run makes the run the origin, an agent flag an
// agent, anything else a person.
func OriginOfActor(person, sessionRef string, agent bool) EventOrigin {
	if id, ok := WorkflowRunOfRef(sessionRef); ok {
		return EventOrigin{Kind: OriginRun, Person: person, RunID: id}
	}
	if agent {
		return EventOrigin{Kind: OriginAgent, Person: person}
	}
	return EventOrigin{Kind: OriginHuman, Person: person}
}

// ReservedForWorkflowRun says whether a session reference claims to come from
// a run: the prefix "workflow-run:" in any spelling of case or surrounding
// space. Like ReservedForGardener, the API refuses such a reference from
// everyone but the run's own principal.
func ReservedForWorkflowRun(ref string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), WorkflowRunRefPrefix)
}

// RunRef is the session reference everything this principal writes carries:
// "workflow-run:<id>" for the token of a run, empty for everyone else.
func (p Principal) RunRef() string {
	if p.TokenKind != WorkflowRunKind || p.RunID <= 0 {
		return ""
	}
	return WorkflowRunRefPrefix + strconv.FormatInt(p.RunID, 10)
}

// WorkflowRunOfRef reads the run number from a session reference
// "workflow-run:<n>".
func WorkflowRunOfRef(ref string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(ref)), WorkflowRunRefPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		// A malformed reference still claims to come from a run: it falls under
		// the guard with an unknown run, which starts nothing.
		return -1, true
	}
	return id, true
}

// WorkflowEventInput is a new entry of the event log. DedupeKey makes the
// event idempotent: the same key twice is one event.
type WorkflowEventInput struct {
	Kind        string
	Project     string
	SubjectKind string // pr, idea, schedule
	SubjectID   string // the number of the pull request or idea, the trigger of a schedule
	Data        map[string]string
	Origin      EventOrigin
	DedupeKey   string
}

// WorkflowEvent is one entry of the event log.
type WorkflowEvent struct {
	ID           int64             `json:"id"`
	Kind         string            `json:"kind"`
	Project      string            `json:"project"`
	OrgID        int64             `json:"org_id"`
	SubjectKind  string            `json:"subject_kind"`
	SubjectID    string            `json:"subject_id"`
	Data         map[string]string `json:"data,omitempty"`
	Origin       EventOrigin       `json:"origin"`
	ChainDepth   int               `json:"chain_depth"`
	DedupeKey    string            `json:"dedupe_key"`
	CreatedAt    string            `json:"created_at"`
	DispatchedAt string            `json:"dispatched_at,omitempty"`
}

// WorkflowTrigger connects a kind of event in a project to a workflow. Branch
// narrows pr.merged: empty means the default branch, "*" every branch.
// EveryMinutes is the period of a schedule.
type WorkflowTrigger struct {
	ID        int64  `json:"id"`
	Project   string `json:"project"`
	Workflow  string `json:"workflow"`
	EventKind string `json:"event_kind"`
	Branch    string `json:"branch,omitempty"`
	// BranchGlobs are the branch patterns of a workflow definition (one per
	// line, release/*); when set they replace Branch.
	BranchGlobs  string `json:"branch_globs,omitempty"`
	EveryMinutes int    `json:"every_minutes,omitempty"`
	AllowChain   bool   `json:"allow_chain"`
	Enabled      bool   `json:"enabled"`
	CreatedBy    string `json:"created_by,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// WorkflowRun is one run of a workflow for one event. Cost, result and version
// stay for good; the full text is in WorkflowRunText until the retention ends.
type WorkflowRun struct {
	ID int64 `json:"id"`
	// OrgID is the namespace of the project when the run was created. It does
	// not follow a project that moves: retention and cost sums go by the
	// project's current org (PurgeWorkflowRunTexts, WorkflowCostSince).
	OrgID           int64   `json:"org_id"`
	Project         string  `json:"project"`
	Workflow        string  `json:"workflow"`
	WorkflowVersion string  `json:"workflow_version,omitempty"`
	TriggerID       int64   `json:"trigger_id"`
	EventID         int64   `json:"event_id"`
	EventKind       string  `json:"event_kind"`
	SubjectKind     string  `json:"subject_kind"`
	SubjectID       string  `json:"subject_id"`
	ChainDepth      int     `json:"chain_depth"`
	State           string  `json:"state"`
	Reason          string  `json:"reason,omitempty"`
	Connection      string  `json:"connection,omitempty"`
	Model           string  `json:"model,omitempty"`
	CostUSD         float64 `json:"cost_usd"`
	Result          string  `json:"result,omitempty"`
	CreatedAt       string  `json:"created_at"`
	StartedAt       string  `json:"started_at,omitempty"`
	FinishedAt      string  `json:"finished_at,omitempty"`
	TextPurgedAt    string  `json:"text_purged_at,omitempty"`
}

// WorkflowRunText is the full text of a run: what went in and what came back.
type WorkflowRunText struct {
	RunID  int64  `json:"run_id"`
	Inputs string `json:"inputs"`
	Output string `json:"output"`
}

// WorkflowRunOutcome is how a run ended, as the executor reports it.
type WorkflowRunOutcome struct {
	State           string
	Reason          string
	Result          string
	WorkflowVersion string
	Connection      string
	Model           string
	CostUSD         float64
	Inputs          string
	Output          string
}

// WorkflowSettings is what an owner set for the workflows of an organization
// or personal namespace.
type WorkflowSettings struct {
	OrgID         int64  `json:"org_id"`
	RetentionDays int    `json:"retention_days"`
	UpdatedBy     int64  `json:"updated_by,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// WorkflowDispatchReport counts what one pass of the dispatcher did.
type WorkflowDispatchReport struct {
	Events  int
	Queued  int
	Skipped int
}

const workflowSchema = `
CREATE TABLE IF NOT EXISTS workflow_events(
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('pr.merged','idea.created','idea.discussion_closed','schedule')),
  project TEXT NOT NULL,
  org_id INTEGER NOT NULL DEFAULT 0,
  subject_kind TEXT NOT NULL DEFAULT '',
  subject_id TEXT NOT NULL DEFAULT '',
  data TEXT NOT NULL DEFAULT '{}',
  origin_kind TEXT NOT NULL CHECK(origin_kind IN ('human','agent','run','system')),
  origin_person TEXT NOT NULL DEFAULT '',
  origin_run_id INTEGER NOT NULL DEFAULT 0,
  chain_depth INTEGER NOT NULL DEFAULT 0,
  dedupe_key TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  dispatched_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS workflow_events_pending ON workflow_events(id) WHERE dispatched_at='';
CREATE INDEX IF NOT EXISTS workflow_events_subject ON workflow_events(project, subject_kind, subject_id);
CREATE TABLE IF NOT EXISTS workflow_triggers(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  workflow TEXT NOT NULL,
  event_kind TEXT NOT NULL CHECK(event_kind IN ('pr.merged','idea.created','idea.discussion_closed','schedule')),
  branch TEXT NOT NULL DEFAULT '',
  every_minutes INTEGER NOT NULL DEFAULT 0,
  allow_chain INTEGER NOT NULL DEFAULT 0 CHECK(allow_chain IN (0,1)),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(project, workflow, event_kind));
CREATE TABLE IF NOT EXISTS workflow_runs(
  id INTEGER PRIMARY KEY,
  org_id INTEGER NOT NULL DEFAULT 0,
  project TEXT NOT NULL,
  workflow TEXT NOT NULL,
  workflow_version TEXT NOT NULL DEFAULT '',
  trigger_id INTEGER NOT NULL DEFAULT 0,
  event_id INTEGER NOT NULL REFERENCES workflow_events(id) ON DELETE RESTRICT,
  chain_depth INTEGER NOT NULL DEFAULT 1,
  state TEXT NOT NULL CHECK(state IN ('queued','running','done','failed','budget','skipped')),
  reason TEXT NOT NULL DEFAULT '',
  connection TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  result TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  text_purged_at TEXT NOT NULL DEFAULT '',
  UNIQUE(event_id, workflow));
CREATE INDEX IF NOT EXISTS workflow_runs_project ON workflow_runs(project, id DESC);
CREATE INDEX IF NOT EXISTS workflow_runs_queued ON workflow_runs(id) WHERE state='queued';
CREATE TABLE IF NOT EXISTS workflow_run_texts(
  run_id INTEGER PRIMARY KEY REFERENCES workflow_runs(id) ON DELETE CASCADE,
  inputs TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workflow_settings(
  org_id INTEGER PRIMARY KEY REFERENCES orgs(id) ON DELETE RESTRICT,
  retention_days INTEGER NOT NULL DEFAULT 90 CHECK(retention_days BETWEEN 1 AND 3650),
  updated_by INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '');
`

func ensureWorkflows(db *sql.DB) error {
	_, err := db.Exec(workflowSchema)
	return err
}

// recordWorkflowEventTx writes an event inside the transaction of the change
// that caused it. created is false when the dedupe key was known already; the
// stored event is returned either way.
func recordWorkflowEventTx(tx *sql.Tx, in WorkflowEventInput) (WorkflowEvent, bool, error) {
	if !slices.Contains(WorkflowEventKinds, in.Kind) || strings.TrimSpace(in.Project) == "" || strings.TrimSpace(in.DedupeKey) == "" || len(in.DedupeKey) > 500 {
		return WorkflowEvent{}, false, ErrWorkflowEvent
	}
	switch in.Origin.Kind {
	case OriginHuman, OriginAgent, OriginSystem:
		in.Origin.RunID = 0
	case OriginRun:
	default:
		return WorkflowEvent{}, false, ErrWorkflowEvent
	}
	depth := 0
	if in.Origin.Kind == OriginRun {
		// An event of a run sits one stage below that run. A run that is not
		// known (or a malformed reference) counts as the deepest stage, so that
		// it can never start anything.
		depth = WorkflowMaxChainDepth
		if in.Origin.RunID > 0 {
			var d int
			if err := tx.QueryRow(`SELECT chain_depth FROM workflow_runs WHERE id=?`, in.Origin.RunID).Scan(&d); err == nil {
				depth = d
			}
		}
	}
	var orgID int64
	if p, ok := projectTx(tx, in.Project); ok {
		orgID = p.OrgID
	}
	data, err := json.Marshal(in.Data)
	if err != nil || in.Data == nil {
		data = []byte("{}")
	}
	res, err := tx.Exec(`INSERT INTO workflow_events(kind,project,org_id,subject_kind,subject_id,data,origin_kind,origin_person,origin_run_id,chain_depth,dedupe_key,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dedupe_key) DO NOTHING`,
		in.Kind, in.Project, orgID, in.SubjectKind, in.SubjectID, string(data), in.Origin.Kind, in.Origin.Person, in.Origin.RunID, depth, in.DedupeKey, now())
	if err != nil {
		return WorkflowEvent{}, false, err
	}
	n, _ := res.RowsAffected()
	ev, err := scanWorkflowEvent(tx.QueryRow(`SELECT `+workflowEventCols+` FROM workflow_events WHERE dedupe_key=?`, in.DedupeKey))
	return ev, n > 0, err
}

// RecordWorkflowEvent writes an event that no other write carries (the
// clock, a closed discussion, tests). The same dedupe key twice is one event.
func (s *Store) RecordWorkflowEvent(in WorkflowEventInput) (WorkflowEvent, bool, error) {
	if s.writer != nil {
		type out struct {
			ev      WorkflowEvent
			created bool
		}
		o, err := queueValue(s, []any{in}, func(d *Store, p []any) (out, error) {
			ev, created, err := d.RecordWorkflowEvent(p[0].(WorkflowEventInput))
			return out{ev, created}, err
		})
		return o.ev, o.created, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowEvent{}, false, err
	}
	defer tx.Rollback()
	ev, created, err := recordWorkflowEventTx(tx, in)
	if err != nil {
		return WorkflowEvent{}, false, err
	}
	return ev, created, tx.Commit()
}

const workflowEventCols = `id, kind, project, org_id, subject_kind, subject_id, data, origin_kind, origin_person, origin_run_id, chain_depth, dedupe_key, created_at, dispatched_at`

func scanWorkflowEvent(r rowScanner) (WorkflowEvent, error) {
	var e WorkflowEvent
	var data string
	if err := r.Scan(&e.ID, &e.Kind, &e.Project, &e.OrgID, &e.SubjectKind, &e.SubjectID, &data, &e.Origin.Kind, &e.Origin.Person, &e.Origin.RunID, &e.ChainDepth, &e.DedupeKey, &e.CreatedAt, &e.DispatchedAt); err != nil {
		return WorkflowEvent{}, err
	}
	_ = json.Unmarshal([]byte(data), &e.Data)
	return e, nil
}

// WorkflowEventByID reads one event.
func (s *Store) WorkflowEventByID(id int64) (WorkflowEvent, error) {
	if s.reader != nil {
		return s.reader.WorkflowEventByID(id)
	}
	return scanWorkflowEvent(s.db.QueryRowContext(s.ctx(), `SELECT `+workflowEventCols+` FROM workflow_events WHERE id=?`, id))
}

// WorkflowWorkPending says whether the dispatcher or the executor has
// something to do: an event not dispatched yet, or a queued run. It is a
// cheap read, so that an idle tick does not go through the writer.
func (s *Store) WorkflowWorkPending() (events, runs bool) {
	if s.reader != nil {
		return s.reader.WorkflowWorkPending()
	}
	var n int
	if s.db.QueryRowContext(s.ctx(), `SELECT 1 FROM workflow_events WHERE dispatched_at='' LIMIT 1`).Scan(&n) == nil {
		events = true
	}
	if s.db.QueryRowContext(s.ctx(), `SELECT 1 FROM workflow_runs WHERE state='queued' LIMIT 1`).Scan(&n) == nil {
		runs = true
	}
	return events, runs
}

// cleanWorkflowName accepts names like ghosttree/echo: lower-case letters,
// digits, '-', '_', '.', '/' and '@', up to 100 bytes.
func cleanWorkflowName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./@", r)) {
			return "", false
		}
	}
	return name, true
}

// SetWorkflowTrigger switches a workflow on for a kind of event in a project,
// or changes how it is triggered. Only a lead or owner of the project may.
// Stage E2 replaces triggers with workflow definitions; until then this is the
// one way to switch a workflow on.
func (s *Store) SetWorkflowTrigger(actorPrincipal string, t WorkflowTrigger) (WorkflowTrigger, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, t}, func(d *Store, p []any) (WorkflowTrigger, error) {
			return d.SetWorkflowTrigger(p[0].(string), p[1].(WorkflowTrigger))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return WorkflowTrigger{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowTrigger{}, err
	}
	defer tx.Rollback()
	if RoleRank(projectRoleTx(tx, t.Project, actor).Role) < 3 {
		return WorkflowTrigger{}, ErrAccessForbidden
	}
	t, err = setWorkflowTriggerTx(tx, t, actorPrincipal)
	if err != nil {
		return WorkflowTrigger{}, err
	}
	return t, tx.Commit()
}

// SetWorkflowTriggerAsOperator is SetWorkflowTrigger for the operator of the
// server (ctx workflow trigger --db), without a person's role.
func (s *Store) SetWorkflowTriggerAsOperator(t WorkflowTrigger) (WorkflowTrigger, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowTrigger{}, err
	}
	defer tx.Rollback()
	if _, ok := projectTx(tx, t.Project); !ok {
		return WorkflowTrigger{}, fmt.Errorf("%w: unknown project %q", ErrWorkflowTrigger, t.Project)
	}
	t, err = setWorkflowTriggerTx(tx, t, "operator")
	if err != nil {
		return WorkflowTrigger{}, err
	}
	return t, tx.Commit()
}

func setWorkflowTriggerTx(tx *sql.Tx, t WorkflowTrigger, by string) (WorkflowTrigger, error) {
	name, ok := cleanWorkflowName(t.Workflow)
	if !ok {
		return WorkflowTrigger{}, ErrWorkflowTrigger
	}
	if t.EventKind == EventIdeaDiscussionClosed {
		return WorkflowTrigger{}, fmt.Errorf("%w: %s fires from stage E3 on, nothing writes that event yet", ErrWorkflowTrigger, EventIdeaDiscussionClosed)
	}
	if !slices.Contains(WorkflowTriggerKinds, t.EventKind) {
		return WorkflowTrigger{}, ErrWorkflowTrigger
	}
	t.Workflow = name
	branch, okBranch := cleanRef(t.Branch)
	switch {
	case t.Branch == "" || t.Branch == "*":
	case okBranch:
		t.Branch = branch
	default:
		return WorkflowTrigger{}, ErrWorkflowTrigger
	}
	if t.EventKind == EventSchedule {
		if t.EveryMinutes < 5 || t.EveryMinutes > 7*24*60 {
			return WorkflowTrigger{}, fmt.Errorf("%w: a schedule runs every 5 minutes to 7 days", ErrWorkflowTrigger)
		}
	} else {
		t.EveryMinutes = 0
	}
	ts := now()
	if _, err := tx.Exec(`INSERT INTO workflow_triggers(project,workflow,event_kind,branch,every_minutes,allow_chain,enabled,created_by,created_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project,workflow,event_kind) DO UPDATE SET branch=excluded.branch, branch_globs='',
		every_minutes=excluded.every_minutes, allow_chain=excluded.allow_chain, enabled=excluded.enabled`,
		t.Project, t.Workflow, t.EventKind, t.Branch, t.EveryMinutes, boolInt(t.AllowChain), boolInt(t.Enabled), by, ts); err != nil {
		return WorkflowTrigger{}, err
	}
	return scanWorkflowTrigger(tx.QueryRow(`SELECT `+workflowTriggerCols+` FROM workflow_triggers WHERE project=? AND workflow=? AND event_kind=?`, t.Project, t.Workflow, t.EventKind))
}

const workflowTriggerCols = `id, project, workflow, event_kind, branch, branch_globs, every_minutes, allow_chain, enabled, created_by, created_at`

func scanWorkflowTrigger(r rowScanner) (WorkflowTrigger, error) {
	var t WorkflowTrigger
	var chain, enabled int
	if err := r.Scan(&t.ID, &t.Project, &t.Workflow, &t.EventKind, &t.Branch, &t.BranchGlobs, &t.EveryMinutes, &chain, &enabled, &t.CreatedBy, &t.CreatedAt); err != nil {
		return WorkflowTrigger{}, err
	}
	t.AllowChain, t.Enabled = chain == 1, enabled == 1
	return t, nil
}

type workflowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func workflowTriggersTx(q workflowQuerier, query string, args ...any) ([]WorkflowTrigger, error) {
	rows, err := q.Query(`SELECT `+workflowTriggerCols+` FROM workflow_triggers WHERE `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowTrigger
	for rows.Next() {
		t, err := scanWorkflowTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// WorkflowTriggersOf lists the triggers of a project.
func (s *Store) WorkflowTriggersOf(project string) ([]WorkflowTrigger, error) {
	if s.reader != nil {
		return s.reader.WorkflowTriggersOf(project)
	}
	return workflowTriggersTx(s.db, `project=? ORDER BY workflow, event_kind`, project)
}

// triggerMatches says whether a trigger reacts to an event, apart from the
// chain guard.
func triggerMatches(t WorkflowTrigger, ev WorkflowEvent) bool {
	if !t.Enabled || t.EventKind != ev.Kind || t.Project != ev.Project {
		return false
	}
	switch ev.Kind {
	case EventPRMerged:
		if t.BranchGlobs != "" {
			return branchGlobsMatch(t.BranchGlobs, ev.Data["base"])
		}
		switch t.Branch {
		case "*":
			return true
		case "":
			return ev.Data["default_branch"] == "1"
		default:
			return ev.Data["base"] == t.Branch
		}
	case EventSchedule:
		return ev.SubjectID == strconv.FormatInt(t.ID, 10)
	}
	return true
}

// chainVerdict applies the chain guard to an event and a trigger that matches
// it: the depth of the run it would start, or the reason it starts none.
func chainVerdict(t WorkflowTrigger, ev WorkflowEvent) (int, string) {
	if ev.Origin.Kind != OriginRun {
		return 1, ""
	}
	if !t.AllowChain {
		return ev.ChainDepth + 1, SkipChain
	}
	if ev.ChainDepth+1 > WorkflowMaxChainDepth {
		return ev.ChainDepth + 1, SkipChainDepth
	}
	return ev.ChainDepth + 1, ""
}

// DispatchWorkflowEvents takes up to limit events not dispatched yet, oldest
// first, and creates the runs of the triggers that match each: queued, or
// skipped with the reason where the chain guard stops them. An event and a
// workflow make one run at most, however often either is seen.
func (s *Store) DispatchWorkflowEvents(limit int) (WorkflowDispatchReport, error) {
	if s.writer != nil {
		return queueValue(s, []any{limit}, func(d *Store, p []any) (WorkflowDispatchReport, error) {
			return d.DispatchWorkflowEvents(p[0].(int))
		})
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowDispatchReport{}, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+workflowEventCols+` FROM workflow_events WHERE dispatched_at='' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return WorkflowDispatchReport{}, err
	}
	var events []WorkflowEvent
	for rows.Next() {
		ev, err := scanWorkflowEvent(rows)
		if err != nil {
			rows.Close()
			return WorkflowDispatchReport{}, err
		}
		events = append(events, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WorkflowDispatchReport{}, err
	}
	var rep WorkflowDispatchReport
	ts := now()
	hourAgo := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, ev := range events {
		triggers, err := workflowTriggersTx(tx, `project=? AND event_kind=? AND enabled=1 ORDER BY id`, ev.Project, ev.Kind)
		if err != nil {
			return rep, err
		}
		for _, t := range triggers {
			if !triggerMatches(t, ev) {
				continue
			}
			depth, skip := chainVerdict(t, ev)
			if skip == "" {
				// Runs the guards stopped did not run; only the others count.
				var started int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_runs WHERE trigger_id=? AND created_at>? AND reason NOT IN (?,?,?)`,
					t.ID, hourAgo, SkipChain, SkipChainDepth, SkipRate).Scan(&started); err != nil {
					return rep, err
				}
				if started >= WorkflowTriggerHourlyCap {
					skip = SkipRate
				}
			}
			state, finished := RunQueued, ""
			if skip != "" {
				state, finished = RunSkipped, ts
			}
			res, err := tx.Exec(`INSERT INTO workflow_runs(org_id,project,workflow,trigger_id,event_id,chain_depth,state,reason,created_at,finished_at)
				VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id,workflow) DO NOTHING`,
				ev.OrgID, ev.Project, t.Workflow, t.ID, ev.ID, depth, state, skip, ts, finished)
			if err != nil {
				return rep, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if skip != "" {
					rep.Skipped++
				} else {
					rep.Queued++
				}
			}
		}
		if _, err := tx.Exec(`UPDATE workflow_events SET dispatched_at=? WHERE id=?`, ts, ev.ID); err != nil {
			return rep, err
		}
		rep.Events++
	}
	return rep, tx.Commit()
}

// scheduleSlot is the period of a schedule trigger that holds at, as its start
// and the dedupe key of its event. ok is false while that period began before
// the trigger existed: a trigger fires from the first period boundary after it
// was switched on, never at once for the period it was created in.
func scheduleSlot(t WorkflowTrigger, at time.Time) (slot, key string, ok bool) {
	period := time.Duration(t.EveryMinutes) * time.Minute
	start := at.UTC().Truncate(period)
	if created, err := time.Parse(time.RFC3339, t.CreatedAt); err == nil && !start.After(created) {
		return "", "", false
	}
	slot = start.Format(time.RFC3339)
	return slot, "schedule:" + strconv.FormatInt(t.ID, 10) + ":" + slot, true
}

// schedulesDue lists the schedule triggers whose current period has no event
// yet. It only reads, so an idle tick need not go through the writer.
func schedulesDue(db interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}, at time.Time) ([]WorkflowTrigger, error) {
	triggers, err := workflowTriggersTx(db, `event_kind='schedule' AND enabled=1 AND every_minutes>0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var due []WorkflowTrigger
	for _, t := range triggers {
		_, key, ok := scheduleSlot(t, at)
		if !ok {
			continue
		}
		var one int
		if err := db.QueryRow(`SELECT 1 FROM workflow_events WHERE dedupe_key=?`, key).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			due = append(due, t)
		} else if err != nil {
			return nil, err
		}
	}
	return due, nil
}

// WorkflowSchedulesDue says whether a schedule trigger has a period at that
// has no event yet. It only reads, so that an idle tick can skip
// EmitWorkflowSchedules and with it the writer.
func (s *Store) WorkflowSchedulesDue(at time.Time) bool {
	if s.reader != nil {
		return s.reader.WorkflowSchedulesDue(at)
	}
	due, err := schedulesDue(s.db, at)
	return err == nil && len(due) > 0
}

// EmitWorkflowSchedules writes one schedule event per enabled schedule trigger
// and period that has begun by at (and after the trigger was created). The
// period start is the dedupe key, so a second pass within the same period
// writes nothing.
func (s *Store) EmitWorkflowSchedules(at time.Time) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{at.UTC()}, func(d *Store, p []any) (int, error) { return d.EmitWorkflowSchedules(p[0].(time.Time)) })
	}
	due, err := schedulesDue(s.db, at)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, t := range due {
		slot, key, _ := scheduleSlot(t, at)
		_, created, err := recordWorkflowEventTx(tx, WorkflowEventInput{Kind: EventSchedule, Project: t.Project, SubjectKind: "schedule",
			SubjectID: strconv.FormatInt(t.ID, 10), Data: map[string]string{"slot": slot}, Origin: EventOrigin{Kind: OriginSystem, Person: "clock"},
			DedupeKey: key})
		if err != nil {
			return n, err
		}
		if created {
			n++
		}
	}
	return n, tx.Commit()
}

const workflowRunCols = `r.id, r.org_id, r.project, r.workflow, r.workflow_version, r.trigger_id, r.event_id, e.kind, e.subject_kind, e.subject_id,
	r.chain_depth, r.state, r.reason, r.connection, r.model, r.cost_usd, r.result, r.created_at, r.started_at, r.finished_at, r.text_purged_at`

const workflowRunFrom = ` FROM workflow_runs r JOIN workflow_events e ON e.id=r.event_id`

func scanWorkflowRun(r rowScanner) (WorkflowRun, error) {
	var w WorkflowRun
	err := r.Scan(&w.ID, &w.OrgID, &w.Project, &w.Workflow, &w.WorkflowVersion, &w.TriggerID, &w.EventID, &w.EventKind, &w.SubjectKind, &w.SubjectID,
		&w.ChainDepth, &w.State, &w.Reason, &w.Connection, &w.Model, &w.CostUSD, &w.Result, &w.CreatedAt, &w.StartedAt, &w.FinishedAt, &w.TextPurgedAt)
	return w, err
}

// ClaimWorkflowRun moves the oldest queued run to running and hands it to the
// executor with its event. ok is false when nothing is queued.
func (s *Store) ClaimWorkflowRun() (WorkflowRun, WorkflowEvent, bool, error) {
	if s.writer != nil {
		type out struct {
			run WorkflowRun
			ev  WorkflowEvent
			ok  bool
		}
		o, err := queueValue(s, []any{}, func(d *Store, _ []any) (out, error) {
			run, ev, ok, err := d.ClaimWorkflowRun()
			return out{run, ev, ok}, err
		})
		return o.run, o.ev, o.ok, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowRun{}, WorkflowEvent{}, false, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM workflow_runs WHERE state='queued' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkflowRun{}, WorkflowEvent{}, false, nil
		}
		return WorkflowRun{}, WorkflowEvent{}, false, err
	}
	res, err := tx.Exec(`UPDATE workflow_runs SET state='running', started_at=? WHERE id=? AND state='queued'`, now(), id)
	if err != nil {
		return WorkflowRun{}, WorkflowEvent{}, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// Somebody else took the run between the read and the update: nothing is
		// claimed, the caller asks again.
		return WorkflowRun{}, WorkflowEvent{}, false, nil
	}
	run, err := scanWorkflowRun(tx.QueryRow(`SELECT `+workflowRunCols+workflowRunFrom+` WHERE r.id=?`, id))
	if err != nil {
		return WorkflowRun{}, WorkflowEvent{}, false, err
	}
	ev, err := scanWorkflowEvent(tx.QueryRow(`SELECT `+workflowEventCols+` FROM workflow_events WHERE id=?`, run.EventID))
	if err != nil {
		return WorkflowRun{}, WorkflowEvent{}, false, err
	}
	return run, ev, true, tx.Commit()
}

// FinishWorkflowRun records how a running run ended. The full text goes into
// its own table; cost, result, version, connection and model stay on the run.
func (s *Store) FinishWorkflowRun(id int64, o WorkflowRunOutcome) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, o}, func(d *Store, p []any) error { return d.FinishWorkflowRun(p[0].(int64), p[1].(WorkflowRunOutcome)) })
	}
	switch o.State {
	case RunDone, RunFailed, RunBudget, RunSkipped:
	default:
		return fmt.Errorf("%w: a run ends done, failed, budget or skipped, not %q", ErrWorkflowRun, o.State)
	}
	if o.CostUSD < 0 {
		o.CostUSD = 0
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`UPDATE workflow_runs SET state=?, reason=?, result=?, workflow_version=?, connection=?, model=?, cost_usd=?, finished_at=?
		WHERE id=? AND state='running'`,
		o.State, cutRunes(o.Reason, 500), cutRunes(o.Result, workflowResultMax), cutRunes(o.WorkflowVersion, 100), cutRunes(o.Connection, 100), cutRunes(o.Model, 100), o.CostUSD, ts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWorkflowRun
	}
	// The run's token for the model proxy ends with the run (REQ-694).
	if err := revokeRunTokensTx(tx, `id=?`, id); err != nil {
		return err
	}
	if o.Inputs != "" || o.Output != "" {
		if _, err := tx.Exec(`INSERT INTO workflow_run_texts(run_id,inputs,output,created_at) VALUES(?,?,?,?)
			ON CONFLICT(run_id) DO UPDATE SET inputs=excluded.inputs, output=excluded.output`,
			id, cutRunes(o.Inputs, workflowTextMax), cutRunes(o.Output, workflowTextMax), ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AbandonWorkflowRuns ends every run still marked running as failed with the
// reason interrupted. The executor calls it once when it starts: one process
// executes runs, so a run running at its start was cut off by a restart.
func (s *Store) AbandonWorkflowRuns() (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{}, func(d *Store, _ []any) (int, error) { return d.AbandonWorkflowRuns() })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// A token of an interrupted run must not outlive it (REQ-694).
	if err := revokeRunTokensTx(tx, `state='running'`); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE workflow_runs SET state='failed', reason='interrupted', finished_at=? WHERE state='running' AND trigger_id>=0`, now())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// AbandonStaleWorkflowRuns is AbandonWorkflowRuns for the runs that started
// before the given time: a run that began after it may belong to another
// process on the same database (a second server, an executor that is alive)
// and stays. The engine passes "now minus the run timeout", so a run cut off
// by a restart is ended once it could not be running any more.
func (s *Store) AbandonStaleWorkflowRuns(before time.Time) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{before.UTC()}, func(d *Store, p []any) (int, error) { return d.AbandonStaleWorkflowRuns(p[0].(time.Time)) })
	}
	// Runs of an external block (trigger -1, the gardener's unit) run for
	// hours outside the engine and end through their own report.
	res, err := s.db.Exec(`UPDATE workflow_runs SET state='failed', reason='interrupted', finished_at=? WHERE state='running' AND trigger_id>=0 AND started_at<=?`,
		now(), before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// StaleWorkflowRuns counts the runs AbandonStaleWorkflowRuns(before) would
// end. It only reads, so that an idle sweep can skip the writer.
func (s *Store) StaleWorkflowRuns(before time.Time) int {
	if s.reader != nil {
		return s.reader.StaleWorkflowRuns(before)
	}
	var n int
	_ = s.db.QueryRowContext(s.ctx(), `SELECT COUNT(*) FROM workflow_runs WHERE state='running' AND trigger_id>=0 AND started_at<=?`, before.UTC().Format(time.RFC3339)).Scan(&n)
	return n
}

func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// WorkflowRunFilter narrows a list of runs: to one subject (pr 12, idea 45),
// to one state.
type WorkflowRunFilter struct {
	SubjectKind string
	SubjectID   string
	State       string
	// Workflow narrows the list to the runs of one workflow.
	Workflow string
	Limit    int
}

// workflowRunsOfSubject lists the runs of one subject of a project from its
// events (index workflow_events_subject), then reaches their runs by
// (event_id, workflow). A run has the project of its event, so the event
// filter is the project filter.
const workflowRunsOfSubject = `SELECT ` + workflowRunCols + ` FROM workflow_events e JOIN workflow_runs r ON r.event_id=e.id
	WHERE e.project=? AND e.subject_kind=? AND e.subject_id=?`

// WorkflowRunsIn lists the runs of a project, newest first. Access is checked
// by ProjectAccess.WorkflowRuns.
func (s *Store) WorkflowRunsIn(project string, f WorkflowRunFilter) ([]WorkflowRun, error) {
	if s.reader != nil {
		return s.reader.WorkflowRunsIn(project, f)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT ` + workflowRunCols + workflowRunFrom + ` WHERE r.project=?`
	args := []any{project}
	if f.SubjectKind != "" {
		// Start from the events of the subject (index workflow_events_subject),
		// then reach their runs by (event_id, workflow): the list of one pull
		// request does not read every run of the project. A run has the project
		// of its event, so the event filter is the project filter.
		q = workflowRunsOfSubject
		args = []any{project, f.SubjectKind, f.SubjectID}
	}
	if f.State != "" {
		q += ` AND r.state=?`
		args = append(args, f.State)
	}
	if f.Workflow != "" {
		q += ` AND r.workflow=?`
		args = append(args, f.Workflow)
	}
	q += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(s.ctx(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowRun
	for rows.Next() {
		w, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkflowRunByID reads one run with its full text; text is nil once the
// retention removed it or when the run kept none.
func (s *Store) WorkflowRunByID(id int64) (WorkflowRun, *WorkflowRunText, error) {
	if s.reader != nil {
		return s.reader.WorkflowRunByID(id)
	}
	run, err := scanWorkflowRun(s.db.QueryRowContext(s.ctx(), `SELECT `+workflowRunCols+workflowRunFrom+` WHERE r.id=?`, id))
	if err != nil {
		return WorkflowRun{}, nil, err
	}
	var t WorkflowRunText
	if err := s.db.QueryRowContext(s.ctx(), `SELECT run_id, inputs, output FROM workflow_run_texts WHERE run_id=?`, id).Scan(&t.RunID, &t.Inputs, &t.Output); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run, nil, nil
		}
		return WorkflowRun{}, nil, err
	}
	return run, &t, nil
}

func workflowSettingsTx(q rowQuerier, orgID int64) WorkflowSettings {
	w := WorkflowSettings{OrgID: orgID, RetentionDays: WorkflowRetentionDefault}
	_ = q.QueryRow(`SELECT retention_days, updated_by, updated_at FROM workflow_settings WHERE org_id=?`, orgID).Scan(&w.RetentionDays, &w.UpdatedBy, &w.UpdatedAt)
	return w
}

// WorkflowSettingsFor are the workflow settings of a namespace, the defaults
// when nobody set any.
func (s *Store) WorkflowSettingsFor(orgID int64) WorkflowSettings {
	if s.reader != nil {
		return s.reader.WorkflowSettingsFor(orgID)
	}
	return workflowSettingsTx(s.db, orgID)
}

// SetWorkflowSettings stores how long the full text of runs is kept in a
// namespace, 1 to 3650 days. Only its owner may.
func (s *Store) SetWorkflowSettings(actorPrincipal string, in WorkflowSettings) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, in}, func(d *Store, p []any) error {
			return d.SetWorkflowSettings(p[0].(string), p[1].(WorkflowSettings))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	if in.RetentionDays < 1 || in.RetentionDays > workflowRetentionMax {
		return ErrWorkflowSettings
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if orgRoleTx(tx, in.OrgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	if _, err := tx.Exec(`INSERT INTO workflow_settings(org_id,retention_days,updated_by,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(org_id) DO UPDATE SET retention_days=excluded.retention_days, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		in.OrgID, in.RetentionDays, actor, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeWorkflowRunTexts deletes the full text of every run older than the
// retention of its namespace at the time at, and notes on the run when that
// happened. Cost, result and version stay. It returns how many texts went.
//
// The namespace is the one the project belongs to now (projects.org_id), not
// the org_id the run was created with: a project that moved to another org
// takes the retention of the new owner with it. What stays for good is
// reason, result (2000 characters), the cost and workflow_events.data; a block
// must keep model excerpts out of those and put them into Inputs and Output.
// The cut-off is computed in SQL; a run whose created_at cannot be read counts
// as past its retention, so that a damaged row never keeps text for good.
func (s *Store) PurgeWorkflowRunTexts(at time.Time) (int, error) {
	if s.writer != nil {
		return queueValue(s, []any{at.UTC()}, func(d *Store, p []any) (int, error) { return d.PurgeWorkflowRunTexts(p[0].(time.Time)) })
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339)
	res, err := tx.Exec(`UPDATE workflow_runs SET text_purged_at=? WHERE id IN (
		SELECT r.id FROM workflow_run_texts t
		JOIN workflow_runs r ON r.id=t.run_id
		LEFT JOIN projects p ON p.remote=r.project
		LEFT JOIN workflow_settings ws ON ws.org_id=p.org_id
		WHERE COALESCE(datetime(r.created_at, '+' || COALESCE(ws.retention_days, ?) || ' days') <= datetime(?), 1))`,
		ts, WorkflowRetentionDefault, ts)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(`DELETE FROM workflow_run_texts WHERE run_id IN (SELECT id FROM workflow_runs WHERE text_purged_at<>'')`); err != nil {
		return 0, err
	}
	// The text of a proposal goes with the run's full text (REQ-695).
	if _, err := tx.Exec(`UPDATE workflow_run_results SET payload='' WHERE payload<>'' AND run_id IN (SELECT id FROM workflow_runs WHERE text_purged_at<>'')`); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// WorkflowCostSince is what the runs of the projects of a namespace cost since
// the given time, counted by the namespace the projects belong to now: a run
// of a project that moved counts for the new owner.
func (s *Store) WorkflowCostSince(orgID int64, since time.Time) (float64, error) {
	if s.reader != nil {
		return s.reader.WorkflowCostSince(orgID, since)
	}
	var usd float64
	err := s.db.QueryRowContext(s.ctx(), `SELECT COALESCE(SUM(r.cost_usd), 0) FROM workflow_runs r JOIN projects p ON p.remote=r.project
		WHERE p.org_id=? AND r.created_at>=?`, orgID, since.UTC().Format(time.RFC3339)).Scan(&usd)
	return usd, err
}

// -------- access

// WorkflowRuns lists the runs of a project the viewer may see: members and
// up, never guests (costs and texts are internal to the team).
func (a *ProjectAccess) WorkflowRuns(project string, f WorkflowRunFilter) ([]WorkflowRun, error) {
	if err := a.Check(project, ResWorkflow, ActRead, Object{}); err != nil {
		return nil, err
	}
	return a.st.WorkflowRunsIn(project, f)
}

// WorkflowRun reads one run of a project with its full text. A run of another
// project answers like one that does not exist.
func (a *ProjectAccess) WorkflowRun(project string, id int64) (WorkflowRun, *WorkflowRunText, error) {
	if err := a.Check(project, ResWorkflow, ActRead, Object{}); err != nil {
		return WorkflowRun{}, nil, err
	}
	run, text, err := a.st.WorkflowRunByID(id)
	if err != nil || run.Project != project {
		return WorkflowRun{}, nil, ErrAccessNotFound
	}
	return run, text, nil
}

// CanSeeWorkflows says whether the viewer sees the workflows of a project.
func (a *ProjectAccess) CanSeeWorkflows(project string) bool {
	return a != nil && a.Check(project, ResWorkflow, ActRead, Object{}) == nil
}
