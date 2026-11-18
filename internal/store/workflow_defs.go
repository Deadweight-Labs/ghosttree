package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Workflow definitions (REQ-695, stage E2): the files that say what a workflow
// does live on the server, not in the repository (DEC-40, DEC-44). Each project
// has any number of them; every saved change is a version with its author and
// time, and the next run takes the newest version at once (DEC-45). Who may
// create and change them is the project's lead or owner (ResWorkflow ActEdit);
// members read them. The text is parsed by internal/workflow, which the store
// does not know: the caller hands in what the trigger of the file says, and the
// store keeps the trigger table in step with the newest version, in the same
// transaction.

var (
	// ErrWorkflowDefName: names are 2 to 64 lower-case letters, digits, '-' and '_'.
	ErrWorkflowDefName = fmt.Errorf("%w: a workflow name has 2 to 64 lower-case letters, digits, - and _", ErrInvalidInput)
	// ErrWorkflowDefConflict: the workflow changed since the editor read it.
	ErrWorkflowDefConflict = errors.New("the workflow was changed in the meantime")
	// ErrWorkflowDefExists: a new workflow has the name of an existing one.
	ErrWorkflowDefExists = errors.New("a workflow with this name exists")
	// ErrWorkflowDefMissing: no workflow of that name in the project.
	ErrWorkflowDefMissing = errors.New("no such workflow")
)

// Where a version came from.
const (
	WorkflowViaWeb = "web"
	WorkflowViaMCP = "mcp"
	WorkflowViaAPI = "api"
)

// WorkflowDef is one workflow of a project with its newest version.
type WorkflowDef struct {
	ID       int64   `json:"id"`
	Project  string  `json:"project"`
	Name     string  `json:"name"`
	Enabled  bool    `json:"enabled"`
	Version  int     `json:"version"`
	Text     string  `json:"text"`
	Hash     string  `json:"hash"`
	Event    string  `json:"event"`
	Uses     string  `json:"uses"`
	Summary  string  `json:"description,omitempty"`
	RunUSD   float64 `json:"run_usd,omitempty"`
	MonthUSD float64 `json:"month_usd,omitempty"`
	// Author and UpdatedAt are those of the newest version.
	Author    int64  `json:"author"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

// WorkflowDefVersion is one saved version of a workflow.
type WorkflowDefVersion struct {
	Version   int    `json:"version"`
	Text      string `json:"text"`
	Hash      string `json:"hash"`
	Author    int64  `json:"author"`
	Via       string `json:"via"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// WorkflowDefSave is a new version of a workflow, with what its text says
// about the trigger, already parsed and checked by the caller.
type WorkflowDefSave struct {
	Project string
	Name    string
	Text    string
	Note    string
	Via     string
	// Base is the version the editor started from: 0 creates a new workflow,
	// a number refuses the save when the newest version is another one, -1
	// saves over whatever is there.
	Base int
	// What the text says.
	Event        string
	Branches     []string
	EveryMinutes int
	AllowChain   bool
	Uses         string
	Summary      string
	RunUSD       float64
	MonthUSD     float64
}

// WorkflowDefSaved says what a save did.
type WorkflowDefSaved struct {
	Def     WorkflowDef `json:"workflow"`
	Created bool        `json:"created"`
	Changed bool        `json:"changed"`
}

const workflowDefSchema = `
CREATE TABLE IF NOT EXISTS workflow_defs(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  head INTEGER NOT NULL DEFAULT 0,
  event TEXT NOT NULL DEFAULT '',
  uses TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  run_usd REAL NOT NULL DEFAULT 0,
  month_usd REAL NOT NULL DEFAULT 0,
  created_by INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(project, name));
CREATE TABLE IF NOT EXISTS workflow_def_versions(
  def_id INTEGER NOT NULL REFERENCES workflow_defs(id) ON DELETE RESTRICT,
  version INTEGER NOT NULL,
  text TEXT NOT NULL,
  hash TEXT NOT NULL,
  author INTEGER NOT NULL,
  via TEXT NOT NULL DEFAULT 'web',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(def_id, version));
`

func ensureWorkflowDefs(db *sql.DB) error {
	if _, err := db.Exec(workflowDefSchema); err != nil {
		return err
	}
	// A definition's branch patterns (release/*); empty keeps the single
	// branch of stage E0 (empty: the default branch, *: every branch).
	return addColumnIfMissing(db, "workflow_triggers", "branch_globs", "TEXT NOT NULL DEFAULT ''")
}

// WorkflowDefHash identifies a text: the first twelve hex digits of its SHA-256.
func WorkflowDefHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

// CleanWorkflowDefName checks the name of a workflow. A '/' is not allowed: it
// marks the blocks of the catalogue (ghosttree/echo).
func CleanWorkflowDefName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	// "new" and "runs" are addresses of the tab (the form, the pages of runs).
	if len(name) < 2 || len(name) > 64 || name == "new" || name == "runs" {
		return "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", false
		}
	}
	return name, true
}

const workflowDefCols = `d.id, d.project, d.name, d.enabled, d.head, d.event, d.uses, d.summary, d.run_usd, d.month_usd, d.created_at,
	COALESCE(v.text,''), COALESCE(v.hash,''), COALESCE(v.author,0), COALESCE(v.created_at,'')`

const workflowDefFrom = ` FROM workflow_defs d LEFT JOIN workflow_def_versions v ON v.def_id=d.id AND v.version=d.head `

func scanWorkflowDef(r rowScanner) (WorkflowDef, error) {
	var d WorkflowDef
	var enabled int
	if err := r.Scan(&d.ID, &d.Project, &d.Name, &enabled, &d.Version, &d.Event, &d.Uses, &d.Summary, &d.RunUSD, &d.MonthUSD, &d.CreatedAt,
		&d.Text, &d.Hash, &d.Author, &d.UpdatedAt); err != nil {
		return WorkflowDef{}, err
	}
	d.Enabled = enabled == 1
	return d, nil
}

// SaveWorkflowDef stores a new version of a workflow, or creates it. Only a
// lead or owner of the project may (ResWorkflow ActEdit). A text equal to the
// newest version stores nothing. The trigger of the project follows the new
// version in the same transaction, so that the next event runs it.
func (s *Store) SaveWorkflowDef(actorPrincipal string, in WorkflowDefSave) (WorkflowDefSaved, error) {
	if s.writer != nil {
		return queueValue(s, []any{actorPrincipal, in}, func(d *Store, p []any) (WorkflowDefSaved, error) {
			return d.SaveWorkflowDef(p[0].(string), p[1].(WorkflowDefSave))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return WorkflowDefSaved{}, err
	}
	name, ok := CleanWorkflowDefName(in.Name)
	if !ok {
		return WorkflowDefSaved{}, ErrWorkflowDefName
	}
	in.Name = name
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > 64<<10 {
		return WorkflowDefSaved{}, fmt.Errorf("%w: a workflow has 1 byte to 64 KiB of text", ErrInvalidInput)
	}
	if err := refuseSecrets("text", in.Text, "note", in.Note); err != nil {
		return WorkflowDefSaved{}, err
	}
	switch in.Via {
	case WorkflowViaWeb, WorkflowViaMCP, WorkflowViaAPI:
	default:
		in.Via = WorkflowViaAPI
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WorkflowDefSaved{}, err
	}
	defer tx.Rollback()
	if !canTx(tx, in.Project, actor, RightWorkflowEdit) {
		return WorkflowDefSaved{}, ErrAccessForbidden
	}
	out, err := saveWorkflowDefTx(tx, actor, in)
	if err != nil {
		return WorkflowDefSaved{}, err
	}
	return out, tx.Commit()
}

func saveWorkflowDefTx(tx *sql.Tx, actor int64, in WorkflowDefSave) (WorkflowDefSaved, error) {
	ts := now()
	var out WorkflowDefSaved
	cur, err := scanWorkflowDef(tx.QueryRow(`SELECT `+workflowDefCols+workflowDefFrom+`WHERE d.project=? AND d.name=?`, in.Project, in.Name))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if in.Base > 0 {
			return out, ErrWorkflowDefMissing
		}
		res, err := tx.Exec(`INSERT INTO workflow_defs(project,name,created_by,created_at) VALUES(?,?,?,?)`, in.Project, in.Name, actor, ts)
		if err != nil {
			return out, err
		}
		cur.ID, _ = res.LastInsertId()
		cur.Project, cur.Name, cur.Enabled, cur.CreatedAt = in.Project, in.Name, true, ts
		out.Created = true
	case err != nil:
		return out, err
	case in.Base == 0:
		return out, ErrWorkflowDefExists
	case in.Base > 0 && in.Base != cur.Version:
		return out, ErrWorkflowDefConflict
	}
	hash := WorkflowDefHash(in.Text)
	if !out.Created && hash == cur.Hash && in.Text == cur.Text {
		out.Def = cur
		return out, nil
	}
	next := cur.Version + 1
	if _, err := tx.Exec(`INSERT INTO workflow_def_versions(def_id,version,text,hash,author,via,note,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		cur.ID, next, in.Text, hash, actor, in.Via, cutRunes(strings.TrimSpace(in.Note), 500), ts); err != nil {
		return out, err
	}
	if _, err := tx.Exec(`UPDATE workflow_defs SET head=?, event=?, uses=?, summary=?, run_usd=?, month_usd=? WHERE id=?`,
		next, in.Event, in.Uses, cutRunes(strings.TrimSpace(in.Summary), 300), in.RunUSD, in.MonthUSD, cur.ID); err != nil {
		return out, err
	}
	if err := syncWorkflowDefTriggerTx(tx, cur.ID, in.Project, in.Name, cur.Enabled, in, fmt.Sprintf("person:%d", actor)); err != nil {
		return out, err
	}
	out.Changed = true
	out.Def, err = scanWorkflowDef(tx.QueryRow(`SELECT `+workflowDefCols+workflowDefFrom+`WHERE d.id=?`, cur.ID))
	return out, err
}

// syncWorkflowDefTriggerTx makes the trigger table say what the newest version
// says: one trigger for its event, enabled with the workflow, and every other
// trigger of the workflow switched off (kept, because runs name its number).
func syncWorkflowDefTriggerTx(tx *sql.Tx, defID int64, project, name string, enabled bool, in WorkflowDefSave, by string) error {
	if _, err := tx.Exec(`UPDATE workflow_triggers SET enabled=0 WHERE project=? AND workflow=? AND event_kind<>?`, project, name, in.Event); err != nil {
		return err
	}
	if in.Event == "" {
		return nil
	}
	branch, globs := "", ""
	switch {
	case len(in.Branches) == 1 && in.Branches[0] == "*":
		branch = "*"
	case len(in.Branches) > 0:
		globs = strings.Join(in.Branches, "\n")
	}
	every := in.EveryMinutes
	if in.Event != EventSchedule {
		every = 0
	}
	_, err := tx.Exec(`INSERT INTO workflow_triggers(project,workflow,event_kind,branch,branch_globs,every_minutes,allow_chain,enabled,created_by,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project,workflow,event_kind) DO UPDATE SET branch=excluded.branch, branch_globs=excluded.branch_globs,
		every_minutes=excluded.every_minutes, allow_chain=excluded.allow_chain, enabled=excluded.enabled`,
		project, name, in.Event, branch, globs, every, boolInt(in.AllowChain), boolInt(enabled), by, now())
	return err
}

// SetWorkflowDefEnabled switches a workflow on or off; only a lead or owner of
// the project may. Off, its trigger starts nothing; its versions stay.
func (s *Store) SetWorkflowDefEnabled(actorPrincipal, project, name string, enabled bool) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, project, name, enabled}, func(d *Store, p []any) error {
			return d.SetWorkflowDefEnabled(p[0].(string), p[1].(string), p[2].(string), p[3].(bool))
		})
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !canTx(tx, project, actor, RightWorkflowEdit) {
		return ErrAccessForbidden
	}
	var id int64
	var event string
	if err := tx.QueryRow(`SELECT id, event FROM workflow_defs WHERE project=? AND name=?`, project, name).Scan(&id, &event); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowDefMissing
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE workflow_defs SET enabled=? WHERE id=?`, boolInt(enabled), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE workflow_triggers SET enabled=? WHERE project=? AND workflow=? AND event_kind=?`, boolInt(enabled), project, name, event); err != nil {
		return err
	}
	return tx.Commit()
}

// WorkflowDefsIn lists the workflows of a project by name. Access is checked
// by the caller (ProjectAccess.WorkflowDefs).
func (s *Store) WorkflowDefsIn(project string) ([]WorkflowDef, error) {
	if s.reader != nil {
		return s.reader.WorkflowDefsIn(project)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+workflowDefCols+workflowDefFrom+`WHERE d.project=? ORDER BY d.name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowDef
	for rows.Next() {
		d, err := scanWorkflowDef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// WorkflowDefByName reads a workflow with its newest version.
func (s *Store) WorkflowDefByName(project, name string) (WorkflowDef, error) {
	if s.reader != nil {
		return s.reader.WorkflowDefByName(project, name)
	}
	d, err := scanWorkflowDef(s.db.QueryRowContext(s.ctx(), `SELECT `+workflowDefCols+workflowDefFrom+`WHERE d.project=? AND d.name=?`, project, name))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrWorkflowDefMissing
	}
	return d, err
}

// WorkflowDefVersions lists the versions of a workflow, newest first.
func (s *Store) WorkflowDefVersions(project, name string) ([]WorkflowDefVersion, error) {
	if s.reader != nil {
		return s.reader.WorkflowDefVersions(project, name)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT v.version, v.text, v.hash, v.author, v.via, v.note, v.created_at
		FROM workflow_def_versions v JOIN workflow_defs d ON d.id=v.def_id WHERE d.project=? AND d.name=? ORDER BY v.version DESC`, project, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkflowDefVersion
	for rows.Next() {
		var v WorkflowDefVersion
		if err := rows.Scan(&v.Version, &v.Text, &v.Hash, &v.Author, &v.Via, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrWorkflowDefMissing
	}
	return out, nil
}

// LastWorkflowRuns is the newest run of each workflow of a project, by name.
func (s *Store) LastWorkflowRuns(project string) (map[string]WorkflowRun, error) {
	if s.reader != nil {
		return s.reader.LastWorkflowRuns(project)
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT `+workflowRunCols+workflowRunFrom+`
		WHERE r.id IN (SELECT MAX(id) FROM workflow_runs WHERE project=? GROUP BY workflow)`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WorkflowRun{}
	for rows.Next() {
		run, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		out[run.Workflow] = run
	}
	return out, rows.Err()
}

// branchGlobsMatch says whether a branch matches one of the patterns of a
// definition (one per line, path.Match syntax).
func branchGlobsMatch(globs, branch string) bool {
	for _, g := range strings.Split(globs, "\n") {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		if ok, _ := path.Match(g, branch); ok {
			return true
		}
	}
	return false
}

// -------- access

// WorkflowDefs lists the workflows of a project for a member or above.
func (a *ProjectAccess) WorkflowDefs(project string) ([]WorkflowDef, error) {
	if err := a.Check(project, ResWorkflow, ActRead, Object{}); err != nil {
		return nil, err
	}
	return a.st.WorkflowDefsIn(project)
}

// WorkflowDef reads one workflow of a project for a member or above.
func (a *ProjectAccess) WorkflowDef(project, name string) (WorkflowDef, error) {
	if err := a.Check(project, ResWorkflow, ActRead, Object{}); err != nil {
		return WorkflowDef{}, err
	}
	return a.st.WorkflowDefByName(project, name)
}

// WorkflowDefVersions lists the versions of a workflow for a member or above.
func (a *ProjectAccess) WorkflowDefVersions(project, name string) ([]WorkflowDefVersion, error) {
	if err := a.Check(project, ResWorkflow, ActRead, Object{}); err != nil {
		return nil, err
	}
	return a.st.WorkflowDefVersions(project, name)
}

// CanEditWorkflows says whether the viewer may create and change the
// workflows of a project: a lead or owner, until IDEA-46 cuts the right finer.
func (a *ProjectAccess) CanEditWorkflows(project string) bool {
	return a != nil && a.Check(project, ResWorkflow, ActEdit, Object{}) == nil
}

// WorkflowVersionLabel is how a run names the version of a definition it ran
// with ("3"); built-in blocks keep their own label.
func WorkflowVersionLabel(version int) string { return strconv.Itoa(version) }

// WorkflowModelAliases are the model aliases a run in the project can use
// (the repository's, the owner's and the server's) and the owner's cap for
// one run, for the check of a workflow (REQ-695). Nothing here reaches a key.
func (s *Store) WorkflowModelAliases(project string) ([]string, float64, error) {
	if s.reader != nil {
		return s.reader.WorkflowModelAliases(project)
	}
	p, ok := projectTx(s.db, project)
	if !ok {
		return nil, 0, ErrWorkflowDefMissing
	}
	rows, err := s.db.QueryContext(s.ctx(), `SELECT DISTINCT alias FROM ai_aliases WHERE (level='repo' AND org_id=? AND project=?)
		OR (level='owner' AND org_id=?) OR level='server' ORDER BY alias`, p.OrgID, project, p.OrgID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	var run float64
	_ = s.db.QueryRowContext(s.ctx(), `SELECT run_usd FROM ai_caps WHERE org_id=? AND workflow=''`, p.OrgID).Scan(&run)
	return out, run, rows.Err()
}
