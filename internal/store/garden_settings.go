package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The gardener per organization or person (REQ-624). The server keeps what an
// owner set in the web interface: whether the gardener works for them, how
// often, in which repositories, with which budget, and which narrowly defined
// proposals it may approve on its own. Running it stays where it is safe: the
// sandboxed gardener unit asks the server what is due (`ctx garden run-due`),
// runs, and reports each run back. Every automatic approval is logged with
// what it changed and can be undone.

// Rules of automatic approval. Each changes nothing a person wrote and can be
// undone; everything else (corrections, staleness, merges, contradictions)
// always waits for a person.
const (
	// GardenRuleProof: a criterion whose proof went missing gets the new proof
	// the gardener found (the commit or test exists, the gardener checked it).
	GardenRuleProof = "proof"
	// GardenRuleDescription: a file without any description gets the proposed
	// one; an existing description is never replaced.
	GardenRuleDescription = "description"
	// GardenRuleTest: the named test that covers a pitfall exists; only the link
	// is recorded.
	GardenRuleTest = "test"

	GardenDaily    = "daily"
	GardenWeekdays = "weekdays"
	GardenWeekly   = "weekly"

	gardenMaxRepos = 200
)

// GardenRules lists the rules in the order the settings show them.
var GardenRules = []string{GardenRuleProof, GardenRuleDescription, GardenRuleTest}

var (
	ErrGardenSettings = errors.New("invalid gardener settings")
	// ErrNotGardener: only the gardener's account reports runs.
	ErrNotGardener    = errors.New("only the gardener's account may do this")
	ErrGardenAutoGone = errors.New("that automatic approval was undone already or does not exist")
)

// GardenSettings is what an owner set for the gardener of one organization or
// personal namespace.
type GardenSettings struct {
	OrgID     int64    `json:"org_id"`
	Enabled   bool     `json:"enabled"`
	Frequency string   `json:"frequency"`
	AtMinute  int      `json:"at_minute"` // minutes after midnight, UTC
	Weekday   int      `json:"weekday"`   // weekly: 0 Sunday ... 6 Saturday
	Repos     []string `json:"repos"`     // empty: every repository of the owner
	BudgetRun float64  `json:"budget_run_usd"`
	BudgetMon float64  `json:"budget_month_usd"`
	Rules     []string `json:"rules"`
	UpdatedBy int64    `json:"updated_by,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// DefaultGardenSettings is what a namespace gets when the gardener is first
// switched on: daily at 03:05 UTC, 3 USD a run, 90 a month, no automatic
// approvals.
func DefaultGardenSettings(orgID int64) GardenSettings {
	return GardenSettings{OrgID: orgID, Frequency: GardenDaily, AtMinute: 3*60 + 5, Weekday: 1, BudgetRun: 3, BudgetMon: 90}
}

// HasRule says whether the rule of automatic approval is on.
func (g GardenSettings) HasRule(rule string) bool { return g.Enabled && slices.Contains(g.Rules, rule) }

// GardenRun is one run of the gardener in one repository.
type GardenRun struct {
	ID         int64   `json:"id"`
	OrgID      int64   `json:"org_id"`
	Project    string  `json:"project"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at,omitempty"`
	State      string  `json:"state"`
	Proposals  int     `json:"proposals"`
	CostUSD    float64 `json:"cost_usd"`
	Note       string  `json:"note,omitempty"`
}

// GardenJob is a namespace whose gardener is due, with what the run may do.
type GardenJob struct {
	OrgID     int64    `json:"org_id"`
	Slug      string   `json:"slug"`
	Projects  []string `json:"projects"`
	BudgetRun float64  `json:"budget_run_usd"`
	BudgetMon float64  `json:"budget_month_usd"`
	// SpentMon is what the namespace's runs reported this month so far; the
	// runner checks the rest of the month before each repository.
	SpentMon float64 `json:"spent_month_usd"`
}

// GardenAuto is one automatic approval: which rule, which proposal, what it
// changed (before and after), on whose authority, and whether it was undone.
type GardenAuto struct {
	ID         int64  `json:"id"`
	OrgID      int64  `json:"org_id"`
	Rule       string `json:"rule"`
	NoteID     int64  `json:"note_id"`
	Project    string `json:"project"`
	TargetKind string `json:"target_kind"` // criterion, ghost, regression
	TargetID   string `json:"target_id"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	EnabledBy  int64  `json:"enabled_by"`
	CreatedAt  string `json:"created_at"`
	UndoneAt   string `json:"undone_at,omitempty"`
	UndoneBy   int64  `json:"undone_by,omitempty"`
}

const gardenSettingsSchema = `
CREATE TABLE IF NOT EXISTS garden_settings(
  org_id INTEGER PRIMARY KEY REFERENCES orgs(id) ON DELETE RESTRICT,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  frequency TEXT NOT NULL DEFAULT 'daily' CHECK(frequency IN ('daily','weekdays','weekly')),
  at_minute INTEGER NOT NULL DEFAULT 185 CHECK(at_minute BETWEEN 0 AND 1439),
  weekday INTEGER NOT NULL DEFAULT 1 CHECK(weekday BETWEEN 0 AND 6),
  repos TEXT NOT NULL DEFAULT '',
  budget_run_usd REAL NOT NULL DEFAULT 3,
  budget_month_usd REAL NOT NULL DEFAULT 90,
  rules TEXT NOT NULL DEFAULT '',
  updated_by INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS garden_runs(
  id INTEGER PRIMARY KEY,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  project TEXT NOT NULL,
  started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK(state IN ('running','done','failed','budget')),
  proposals INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS garden_runs_org ON garden_runs(org_id, id DESC);
CREATE TABLE IF NOT EXISTS garden_auto_log(
  id INTEGER PRIMARY KEY,
  org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE RESTRICT,
  rule TEXT NOT NULL, note_id INTEGER NOT NULL, project TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK(target_kind IN ('criterion','ghost','regression')),
  target_id TEXT NOT NULL,
  before TEXT NOT NULL DEFAULT '', after TEXT NOT NULL DEFAULT '',
  enabled_by INTEGER NOT NULL, created_at TEXT NOT NULL,
  undone_at TEXT NOT NULL DEFAULT '', undone_by INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS garden_auto_note ON garden_auto_log(note_id);
CREATE INDEX IF NOT EXISTS garden_auto_org ON garden_auto_log(org_id, id DESC);
CREATE TRIGGER IF NOT EXISTS garden_auto_log_no_delete BEFORE DELETE ON garden_auto_log
  BEGIN SELECT RAISE(ABORT, 'garden_auto_log is append-only'); END;
`

func ensureGardenSettings(db *sql.DB) error {
	_, err := db.Exec(gardenSettingsSchema)
	return err
}

func scanGardenSettings(r rowScanner) (GardenSettings, error) {
	var g GardenSettings
	var enabled int
	var repos, rules string
	if err := r.Scan(&g.OrgID, &enabled, &g.Frequency, &g.AtMinute, &g.Weekday, &repos, &g.BudgetRun, &g.BudgetMon, &rules, &g.UpdatedBy, &g.UpdatedAt); err != nil {
		return GardenSettings{}, err
	}
	g.Enabled = enabled == 1
	g.Repos, g.Rules = gardenLines(repos), gardenLines(rules)
	return g, nil
}

func gardenLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

const gardenSettingsCols = `org_id, enabled, frequency, at_minute, weekday, repos, budget_run_usd, budget_month_usd, rules, updated_by, updated_at`

func gardenSettingsTx(q rowQuerier, orgID int64) (GardenSettings, bool) {
	g, err := scanGardenSettings(q.QueryRow(`SELECT `+gardenSettingsCols+` FROM garden_settings WHERE org_id=?`, orgID))
	if err != nil {
		return DefaultGardenSettings(orgID), false
	}
	return g, true
}

// GardenSettingsFor are the gardener settings of a namespace; ok is false when
// nobody set any yet (then the defaults come back, switched off).
func (s *Store) GardenSettingsFor(orgID int64) (GardenSettings, bool) {
	if s.reader != nil {
		return s.reader.GardenSettingsFor(orgID)
	}
	return gardenSettingsTx(s.db, orgID)
}

// SetGardenSettings stores the gardener settings of a namespace. Only its
// owner may (the web layer also demands an interactive session). Repos must
// belong to the namespace; rules must be known ones.
func (s *Store) SetGardenSettings(actorPrincipal string, in GardenSettings) error {
	if s.writer != nil {
		return queueWrite(s, []any{actorPrincipal, in}, func(d *Store, p []any) error {
			return d.SetGardenSettings(p[0].(string), p[1].(GardenSettings))
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
	if orgRoleTx(tx, in.OrgID, actor) != OrgOwner {
		return ErrNotOrgOwner
	}
	if err := checkGardenSettingsTx(tx, &in); err != nil {
		return err
	}
	var before []string
	if old, ok := gardenSettingsTx(tx, in.OrgID); ok && old.Enabled {
		before = old.Repos
	}
	_, err = tx.Exec(`INSERT INTO garden_settings(`+gardenSettingsCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(org_id) DO UPDATE SET enabled=excluded.enabled, frequency=excluded.frequency, at_minute=excluded.at_minute,
		weekday=excluded.weekday, repos=excluded.repos, budget_run_usd=excluded.budget_run_usd, budget_month_usd=excluded.budget_month_usd,
		rules=excluded.rules, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		in.OrgID, boolInt(in.Enabled), in.Frequency, in.AtMinute, in.Weekday, strings.Join(in.Repos, "\n"), in.BudgetRun, in.BudgetMon,
		strings.Join(in.Rules, "\n"), actor, now())
	if err != nil {
		return err
	}
	// The gardener is a service: it works only where a project names it. The
	// owner who picks repositories gives it the role member there, in a
	// personal namespace as a guest of it (security review 16). Repositories
	// the settings named before and no longer name (all of them when switched
	// off) lose that role again; roles somebody granted by hand stay.
	if gardener := gardenerIDTx(tx); gardener > 0 {
		var keep []string
		if in.Enabled {
			keep = in.Repos
		}
		for _, remote := range keep {
			if err := grantGardenerTx(tx, in.OrgID, gardener, remote, actor); err != nil {
				return err
			}
		}
		var drop []string
		for _, remote := range before {
			if !slices.Contains(keep, remote) {
				drop = append(drop, remote)
			}
		}
		if err := revokeGardenerTx(tx, in.OrgID, gardener, drop, actor); err != nil {
			return err
		}
	}
	if err := orgEvent(tx, in.OrgID, "garden_settings", principalOfID(actor), fmt.Sprintf("enabled=%t", in.Enabled),
		fmt.Sprintf("%s %02d:%02d run=%.2f month=%.2f rules=%s repos=%d", in.Frequency, in.AtMinute/60, in.AtMinute%60, in.BudgetRun, in.BudgetMon, strings.Join(in.Rules, ","), len(in.Repos))); err != nil {
		return err
	}
	return tx.Commit()
}

// grantGardenerTx gives the gardener the role member in one repository of the
// namespace, as a guest of the organization when it is no member.
func grantGardenerTx(tx execQueryer, orgID, gardener int64, remote string, actor int64) error {
	var pid int64
	if tx.QueryRow(`SELECT id FROM projects WHERE remote=? AND org_id=?`, remote, orgID).Scan(&pid) != nil {
		return ErrProjectNotFound
	}
	if orgRoleTx(tx, orgID, gardener) == "" {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_members(org_id, account_id, role, joined_at) VALUES(?,?,'member',?)`, orgID, gardener, now()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO org_guests(org_id, account_id) VALUES(?,?)`, orgID, gardener); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO project_members(project_id, account_id, role, can_review, granted_by, granted_at) VALUES(?,?,?,0,?,?)`,
		pid, gardener, RoleMember, actor, now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return roleEvent(tx, remote, principalOfID(gardener), "", RoleMember, principalOfID(actor), RoleViaWeb)
	}
	return nil
}

// revokeGardenerTx takes the gardener's role away in the named repositories of
// the namespace, and its guest seat once it has no project role left there.
func revokeGardenerTx(tx execQueryer, orgID, gardener int64, remotes []string, actor int64) error {
	for _, remote := range remotes {
		var pid int64
		var role string
		if tx.QueryRow(`SELECT p.id, m.role FROM project_members m JOIN projects p ON p.id=m.project_id
			WHERE m.account_id=? AND p.org_id=? AND p.remote=?`, gardener, orgID, remote).Scan(&pid, &role) != nil {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM project_members WHERE project_id=? AND account_id=?`, pid, gardener); err != nil {
			return err
		}
		if err := roleEvent(tx, remote, principalOfID(gardener), role, "", principalOfID(actor), RoleViaWeb); err != nil {
			return err
		}
	}
	if len(remotes) == 0 || !orgGuestTx(tx, orgID, gardener) {
		return nil
	}
	var left int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.account_id=? AND p.org_id=?`,
		gardener, orgID).Scan(&left)
	if left > 0 {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM org_guests WHERE org_id=? AND account_id=?`, orgID, gardener); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM org_members WHERE org_id=? AND account_id=?`, orgID, gardener)
	return err
}

func checkGardenSettingsTx(q rowQuerier, in *GardenSettings) error {
	if !slices.Contains([]string{GardenDaily, GardenWeekdays, GardenWeekly}, in.Frequency) ||
		in.AtMinute < 0 || in.AtMinute > 1439 || in.Weekday < 0 || in.Weekday > 6 ||
		in.BudgetRun < 0 || in.BudgetRun > 1000 || in.BudgetMon < 0 || in.BudgetMon > 100000 || in.BudgetRun != in.BudgetRun || in.BudgetMon != in.BudgetMon ||
		len(in.Repos) > gardenMaxRepos {
		return ErrGardenSettings
	}
	var rules []string
	for _, r := range in.Rules {
		if !slices.Contains(GardenRules, r) {
			return fmt.Errorf("%w: unknown rule %q", ErrGardenSettings, r)
		}
		if !slices.Contains(rules, r) {
			rules = append(rules, r)
		}
	}
	in.Rules = rules
	var repos []string
	for _, r := range in.Repos {
		var orgID int64
		if q.QueryRow(`SELECT org_id FROM projects WHERE remote=?`, r).Scan(&orgID) != nil || orgID != in.OrgID {
			return fmt.Errorf("%w: %q is not a repository of this owner", ErrGardenSettings, r)
		}
		if !slices.Contains(repos, r) {
			repos = append(repos, r)
		}
	}
	slices.Sort(repos)
	in.Repos = repos
	return nil
}

// lastGardenSlot is the latest scheduled start at or before t (UTC).
func lastGardenSlot(g GardenSettings, t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		slot := day.Add(time.Duration(g.AtMinute) * time.Minute)
		ok := !slot.After(t)
		switch g.Frequency {
		case GardenWeekdays:
			ok = ok && slot.Weekday() != time.Saturday && slot.Weekday() != time.Sunday
		case GardenWeekly:
			ok = ok && int(slot.Weekday()) == g.Weekday
		}
		if ok {
			return slot
		}
		day = day.AddDate(0, 0, -1)
	}
	return time.Time{}
}

// GardenDue lists the namespaces whose gardener is due at t: switched on, a
// scheduled start has passed since their last run, and the month's budget is
// not spent. The repositories are the chosen ones that still exist, else all
// of the namespace (archived ones never).
func (s *Store) GardenDue(t time.Time) ([]GardenJob, error) {
	if s.reader != nil {
		return s.reader.GardenDue(t)
	}
	rows, err := s.db.Query(`SELECT ` + gardenSettingsCols + ` FROM garden_settings WHERE enabled=1 ORDER BY org_id`)
	if err != nil {
		return nil, err
	}
	var all []GardenSettings
	for rows.Next() {
		g, err := scanGardenSettings(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []GardenJob
	for _, g := range all {
		slot := lastGardenSlot(g, t)
		if slot.IsZero() {
			continue
		}
		var last string
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(started_at),'') FROM garden_runs WHERE org_id=?`, g.OrgID).Scan(&last)
		if last != "" && last >= slot.Format(time.RFC3339) {
			continue
		}
		spent := s.gardenSpent(g.OrgID, t)
		if g.BudgetMon > 0 && spent >= g.BudgetMon {
			continue
		}
		o, err := orgByIDTx(s.db, g.OrgID)
		if err != nil {
			continue
		}
		job := GardenJob{OrgID: g.OrgID, Slug: o.Slug, BudgetRun: g.BudgetRun, BudgetMon: g.BudgetMon, SpentMon: spent}
		gardener := s.GardenerAccountID()
		rr, err := s.db.Query(`SELECT remote FROM projects WHERE org_id=? AND archived_at='' ORDER BY remote LIMIT ?`, g.OrgID, maxListRows)
		if err != nil {
			return nil, err
		}
		for rr.Next() {
			var r string
			if rr.Scan(&r) == nil && (len(g.Repos) == 0 || slices.Contains(g.Repos, r)) {
				job.Projects = append(job.Projects, r)
			}
		}
		rr.Close()
		// Only where the gardener holds a role: it is a service.
		kept := job.Projects[:0]
		for _, r := range job.Projects {
			if gardener > 0 && projectRoleTx(s.db, r, gardener).Role != "" {
				kept = append(kept, r)
			}
		}
		job.Projects = kept
		if len(job.Projects) > 0 {
			out = append(out, job)
		}
	}
	return out, nil
}

// gardenSpent is what the runs of this calendar month (UTC) cost.
func (s *Store) gardenSpent(orgID int64, t time.Time) float64 {
	month := t.UTC().Format("2006-01")
	var spent float64
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(cost_usd),0) FROM garden_runs WHERE org_id=? AND substr(started_at,1,7)=?`, orgID, month).Scan(&spent)
	return spent
}

// GardenSpent is the month's spending of a namespace's gardener (UTC month).
func (s *Store) GardenSpent(orgID int64, t time.Time) float64 {
	if s.reader != nil {
		return s.reader.GardenSpent(orgID, t)
	}
	return s.gardenSpent(orgID, t)
}

func (s *Store) requireGardener(accountPrincipal string) error {
	id, err := parsePersonPrincipalID(accountPrincipal)
	if err != nil {
		return err
	}
	if g := s.GardenerAccountID(); g == 0 || g != id {
		return ErrNotGardener
	}
	return nil
}

// StartGardenRun records that the gardener began in one repository of a
// namespace. Only the gardener's account reports runs, and only for a
// repository of that namespace whose gardener is switched on.
func (s *Store) StartGardenRun(accountPrincipal string, orgID int64, project string) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{accountPrincipal, orgID, project}, func(d *Store, p []any) (int64, error) {
			return d.StartGardenRun(p[0].(string), p[1].(int64), p[2].(string))
		})
	}
	if err := s.requireGardener(accountPrincipal); err != nil {
		return 0, err
	}
	g, ok := gardenSettingsTx(s.db, orgID)
	if !ok || !g.Enabled {
		return 0, ErrGardenSettings
	}
	var owner int64
	if s.db.QueryRow(`SELECT org_id FROM projects WHERE remote=?`, project).Scan(&owner) != nil || owner != orgID {
		return 0, ErrProjectNotFound
	}
	res, err := s.db.Exec(`INSERT INTO garden_runs(org_id, project, started_at, state) VALUES(?,?,?,'running')`, orgID, project, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishGardenRun records how a run ended. The number of proposals is counted
// here from what the gardener filed in that repository since the start, not
// taken from the report.
func (s *Store) FinishGardenRun(accountPrincipal string, runID int64, state string, costUSD float64, note string) error {
	if s.writer != nil {
		return queueWrite(s, []any{accountPrincipal, runID, state, costUSD, note}, func(d *Store, p []any) error {
			return d.FinishGardenRun(p[0].(string), p[1].(int64), p[2].(string), p[3].(float64), p[4].(string))
		})
	}
	if err := s.requireGardener(accountPrincipal); err != nil {
		return err
	}
	if !slices.Contains([]string{"done", "failed", "budget"}, state) || costUSD < 0 || costUSD > 1000 || costUSD != costUSD {
		return ErrGardenSettings
	}
	if len(note) > 500 || hasControlRunes(note) {
		note = ""
	}
	var project, started string
	if err := s.db.QueryRow(`SELECT project, started_at FROM garden_runs WHERE id=? AND state='running'`, runID).Scan(&project, &started); err != nil {
		return ErrGardenAutoGone
	}
	var proposals int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE project=? AND harness=? AND created_at>=?`, project, GardenerHarness, started).Scan(&proposals)
	_, err := s.db.Exec(`UPDATE garden_runs SET finished_at=?, state=?, proposals=?, cost_usd=?, note=? WHERE id=?`, now(), state, proposals, costUSD, note, runID)
	return err
}

// GardenRuns are the latest runs of a namespace, newest first.
func (s *Store) GardenRuns(orgID int64, limit int) ([]GardenRun, error) {
	if s.reader != nil {
		return s.reader.GardenRuns(orgID, limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, org_id, project, started_at, finished_at, state, proposals, cost_usd, note FROM garden_runs WHERE org_id=? ORDER BY id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GardenRun
	for rows.Next() {
		var g GardenRun
		if err := rows.Scan(&g.ID, &g.OrgID, &g.Project, &g.StartedAt, &g.FinishedAt, &g.State, &g.Proposals, &g.CostUSD, &g.Note); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RecordGardenAuto logs one automatic approval. The caller has applied it on
// the authority of the owner who switched the rule on (EnabledBy).
func (s *Store) RecordGardenAuto(in GardenAuto) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (int64, error) { return d.RecordGardenAuto(p[0].(GardenAuto)) })
	}
	if !slices.Contains(GardenRules, in.Rule) || in.NoteID <= 0 || in.EnabledBy <= 0 {
		return 0, ErrGardenSettings
	}
	res, err := s.db.Exec(`INSERT INTO garden_auto_log(org_id, rule, note_id, project, target_kind, target_id, before, after, enabled_by, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, in.OrgID, in.Rule, in.NoteID, in.Project, in.TargetKind, in.TargetID, in.Before, in.After, in.EnabledBy, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanGardenAuto(r rowScanner) (GardenAuto, error) {
	var g GardenAuto
	err := r.Scan(&g.ID, &g.OrgID, &g.Rule, &g.NoteID, &g.Project, &g.TargetKind, &g.TargetID, &g.Before, &g.After, &g.EnabledBy, &g.CreatedAt, &g.UndoneAt, &g.UndoneBy)
	return g, err
}

const gardenAutoCols = `id, org_id, rule, note_id, project, target_kind, target_id, before, after, enabled_by, created_at, undone_at, undone_by`

// GardenAutoLog lists the automatic approvals of a namespace, newest first.
func (s *Store) GardenAutoLog(orgID int64, limit int) ([]GardenAuto, error) {
	if s.reader != nil {
		return s.reader.GardenAutoLog(orgID, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+gardenAutoCols+` FROM garden_auto_log WHERE org_id=? ORDER BY id DESC LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GardenAuto
	for rows.Next() {
		g, err := scanGardenAuto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GardenAutoByID reads one logged approval.
func (s *Store) GardenAutoByID(id int64) (GardenAuto, error) {
	if s.reader != nil {
		return s.reader.GardenAutoByID(id)
	}
	g, err := scanGardenAuto(s.db.QueryRow(`SELECT `+gardenAutoCols+` FROM garden_auto_log WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return GardenAuto{}, ErrGardenAutoGone
	}
	return g, err
}

// GardenAutoNoted says whether a proposal was approved automatically before;
// an undone one is not approved again.
func (s *Store) GardenAutoNoted(noteID int64) bool {
	if s.reader != nil {
		return s.reader.GardenAutoNoted(noteID)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM garden_auto_log WHERE note_id=?`, noteID).Scan(&n)
	return n > 0
}

// MarkGardenAutoUndone records that a person took an automatic approval back.
// The caller has reverted the change itself.
func (s *Store) MarkGardenAutoUndone(id int64, actorPrincipal string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, actorPrincipal}, func(d *Store, p []any) error { return d.MarkGardenAutoUndone(p[0].(int64), p[1].(string)) })
	}
	actor, err := parsePersonPrincipalID(actorPrincipal)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE garden_auto_log SET undone_at=?, undone_by=? WHERE id=? AND undone_at=''`, now(), actor, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGardenAutoGone
	}
	return nil
}

// FormatGardenTime is HH:MM of a minute of the day.
func FormatGardenTime(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// ParseGardenTime reads HH:MM.
func ParseGardenTime(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// GardenSettingsAll lists every namespace that has the gardener on, for the
// server's automatic approvals.
func (s *Store) GardenSettingsAll() ([]GardenSettings, error) {
	if s.reader != nil {
		return s.reader.GardenSettingsAll()
	}
	rows, err := s.db.Query(`SELECT ` + gardenSettingsCols + ` FROM garden_settings WHERE enabled=1 ORDER BY org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GardenSettings
	for rows.Next() {
		g, err := scanGardenSettings(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UndoGardenGhost removes a description the gardener wrote on its own, but
// only while it is still exactly that one (same session reference): what a
// person wrote since is never taken away. ok is false when it changed.
func (s *Store) UndoGardenGhost(project, path, sessionRef string) (bool, error) {
	if s.writer != nil {
		return queueValue(s, []any{project, path, sessionRef}, func(d *Store, p []any) (bool, error) {
			return d.UndoGardenGhost(p[0].(string), p[1].(string), p[2].(string))
		})
	}
	res, err := s.db.Exec(`DELETE FROM ghost_files WHERE project=? AND path=? AND session_ref=? AND kind='file'`, project, path, sessionRef)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// NextGardenRun is the next scheduled start after t, zero when switched off.
func NextGardenRun(g GardenSettings, t time.Time) time.Time {
	if !g.Enabled {
		return time.Time{}
	}
	for d := 1; d <= 8; d++ {
		if slot := lastGardenSlot(g, t.Add(time.Duration(d)*24*time.Hour)); slot.After(t) {
			// The earliest slot after t is the last one at or before some later moment.
			for back := lastGardenSlot(g, slot.Add(-time.Minute)); back.After(t); back = lastGardenSlot(g, back.Add(-time.Minute)) {
				slot = back
			}
			return slot
		}
	}
	return time.Time{}
}

// commitKnownTx: a commit with this sha (or sha prefix, 7 hex at least) is in
// the project's history as far as it is synchronized: a commit of a merged
// pull request (still part of it, in_pr=1), or the commit a merged pull
// request landed as on the base branch (merge_sha, also a squash). Open or
// closed pull requests do not count; pull requests from forks are never
// recorded (RecordPullRequest).
func commitKnownTx(q rowQuerier, project, sha string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) < 7 || strings.Trim(sha, "0123456789abcdef") != "" {
		return false
	}
	var n int
	_ = q.QueryRow(`SELECT
		(SELECT COUNT(*) FROM git_pr_commits c JOIN git_pull_requests p ON p.project=c.project AND p.number=c.pr AND p.state='merged'
			WHERE c.project=? AND c.in_pr=1 AND substr(c.sha,1,?)=?)
		+ (SELECT COUNT(*) FROM git_pull_requests WHERE project=? AND state='merged' AND length(merge_sha)>=? AND substr(lower(merge_sha),1,?)=?)`,
		project, len(sha), sha, project, len(sha), len(sha), sha).Scan(&n)
	return n > 0
}

// leadingSHA is the commit a recorded commit proof starts with.
func leadingSHA(ref string) string {
	f := strings.Fields(ref)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// ErrNotVanished: the criterion is not met, its proof still stands, or the new
// proof is no synchronized commit. The rule acts on nothing else.
var ErrNotVanished = errors.New("only a met criterion whose commit proof vanished gets a new, synchronized commit as proof")

// ReplaceVanishedEvidence is the gardener's rule "missing proof, new proof"
// (REQ-624): a criterion that is met and whose every commit proof is no longer
// among the synchronized commits gets the proposed commit as an added proof.
// The state never changes, and the new commit must exist among the
// synchronized commits; the server has no checkout, so tests and files never
// qualify. It returns the number of the added evidence row.
func (s *Store) ReplaceVanishedEvidence(criterionID int64, project, ref, person string) (int64, error) {
	if s.writer != nil {
		return queueValue(s, []any{criterionID, project, ref, person}, func(d *Store, p []any) (int64, error) {
			return d.ReplaceVanishedEvidence(p[0].(int64), p[1].(string), p[2].(string), p[3].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var requestID int64
	var state, reqProject string
	if err := tx.QueryRow(`SELECT c.request_id, c.state, r.project FROM request_criteria c JOIN requests r ON r.id=c.request_id WHERE c.id=?`, criterionID).Scan(&requestID, &state, &reqProject); err != nil {
		return 0, ErrNotVanished
	}
	if state != "met" || reqProject != project || !commitKnownTx(tx, project, leadingSHA(ref)) {
		return 0, ErrNotVanished
	}
	rows, err := tx.Query(`SELECT kind, ref FROM request_evidence WHERE criterion_id=?`, criterionID)
	if err != nil {
		return 0, err
	}
	commits := 0
	vanished := true
	for rows.Next() {
		var kind, r string
		if rows.Scan(&kind, &r) != nil || kind != "commit" {
			continue
		}
		commits++
		if commitKnownTx(tx, project, leadingSHA(r)) {
			vanished = false
		}
	}
	rows.Close()
	if commits == 0 || !vanished {
		return 0, ErrNotVanished
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO request_evidence(request_id,criterion_id,kind,ref,person,created_at) VALUES(?,?,?,?,?,?)`, requestID, criterionID, "commit", ref, person, ts)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,?,?,?,?)`, requestID, "criterion.evidence_added", person, fmt.Sprintf("AC-%d", criterionID), ts); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// RemoveAutoEvidence takes back a proof the gardener's rule added, as long as
// it is still exactly that one (same row, criterion and reference).
func (s *Store) RemoveAutoEvidence(evidenceID, criterionID int64, ref string) error {
	if s.writer != nil {
		return queueWrite(s, []any{evidenceID, criterionID, ref}, func(d *Store, p []any) error {
			return d.RemoveAutoEvidence(p[0].(int64), p[1].(int64), p[2].(string))
		})
	}
	res, err := s.db.Exec(`DELETE FROM request_evidence WHERE id=? AND criterion_id=? AND ref=?`, evidenceID, criterionID, ref)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGardenAutoGone
	}
	return nil
}

// gardenerIDTx reads the gardener's account inside a transaction (a second
// connection would wait for this one).
func gardenerIDTx(q rowQuerier) int64 {
	var v string
	if q.QueryRow(`SELECT value FROM org_state WHERE key=?`, gardenerAccountKey).Scan(&v) != nil {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || id < 1 {
		return 0
	}
	return id
}
