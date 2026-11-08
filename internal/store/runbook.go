package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/runbook"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// runbookSchema (REQ-604): Abläufe als eigenes Objekt, nicht als Wissensart.
//
// Warum nicht in knowledge: Wissen wird bei jeder Änderung archiviert und die
// Spitze überschrieben; ein Ablauf braucht Fassungen, die für immer
// nebeneinander stehen (jede ist ein Hash, den ein Mensch freigegeben hat),
// Freigaben je Fassung, Läufe und Vorschläge. Das passt in keine Spalte von
// knowledge. Die Fassungen folgen dem Git-Prinzip aus #1444: jede bleibt, nichts
// wird überschrieben oder gelöscht, die Spitze ist die höchste Nummer.
//
// Fassungen, Freigaben und Läufe sind per Trigger unveränderlich. Anker und
// Entstehungskontext hängen wie bei Wissen an der Revision (anchor_id,
// origin_id), hier gleich beim Anlegen der Tabelle.
const runbookSchema = `
CREATE TABLE IF NOT EXISTS runbooks(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL DEFAULT '',
  machine TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL CHECK(name != ''),
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL);
-- Ein Ablauf ohne Projekt und Maschine gehört seinem Autor: zwei Konten dürfen
-- denselben Namen führen, ohne voneinander zu wissen.
CREATE UNIQUE INDEX IF NOT EXISTS runbooks_scope_name ON runbooks(project, machine, name, (CASE WHEN project='' AND machine='' THEN author_id ELSE 0 END));
CREATE INDEX IF NOT EXISTS runbooks_name ON runbooks(name);
CREATE TABLE IF NOT EXISTS runbook_versions(
  id INTEGER PRIMARY KEY,
  runbook_id INTEGER NOT NULL REFERENCES runbooks(id),
  version INTEGER NOT NULL CHECK(version > 0),
  spec TEXT NOT NULL,
  hash TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  anchor_id INTEGER REFERENCES git_anchors(id),
  origin_id INTEGER REFERENCES origin_contexts(id),
  UNIQUE(runbook_id, version));
CREATE TABLE IF NOT EXISTS runbook_approvals(
  version_id INTEGER NOT NULL REFERENCES runbook_versions(id),
  approver_id INTEGER NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(version_id, approver_id));
CREATE TABLE IF NOT EXISTS runbook_runs(
  id INTEGER PRIMARY KEY,
  runbook_id INTEGER NOT NULL REFERENCES runbooks(id),
  version_id INTEGER NOT NULL REFERENCES runbook_versions(id),
  target TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL CHECK(mode IN ('checklist','exec')),
  result TEXT NOT NULL CHECK(result IN ('ok','failed','aborted')),
  params TEXT NOT NULL DEFAULT '{}',
  note TEXT NOT NULL DEFAULT '',
  machine TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  deviations TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  anchor_id INTEGER REFERENCES git_anchors(id),
  origin_id INTEGER REFERENCES origin_contexts(id));
CREATE INDEX IF NOT EXISTS runbook_runs_runbook ON runbook_runs(runbook_id, id);
CREATE TABLE IF NOT EXISTS runbook_proposals(
  id INTEGER PRIMARY KEY,
  runbook_id INTEGER NOT NULL REFERENCES runbooks(id),
  base_version_id INTEGER NOT NULL REFERENCES runbook_versions(id),
  run_id INTEGER REFERENCES runbook_runs(id),
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','accepted','rejected')),
  note TEXT NOT NULL DEFAULT '',
  deviations TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  author_id INTEGER NOT NULL DEFAULT 0,
  decided_by TEXT NOT NULL DEFAULT '',
  result_version_id INTEGER REFERENCES runbook_versions(id),
  created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS runbook_proposals_runbook ON runbook_proposals(runbook_id, state);
CREATE TABLE IF NOT EXISTS runbook_allowances(
  id INTEGER PRIMARY KEY,
  version_id INTEGER NOT NULL REFERENCES runbook_versions(id),
  machine TEXT NOT NULL CHECK(machine != ''),
  hash TEXT NOT NULL,
  person TEXT NOT NULL DEFAULT '',
  account_id INTEGER NOT NULL DEFAULT 0,
  via TEXT NOT NULL,
  created_at TEXT NOT NULL,
  revoked_at TEXT);
CREATE UNIQUE INDEX IF NOT EXISTS runbook_allowances_live ON runbook_allowances(version_id, machine) WHERE revoked_at IS NULL;
CREATE TRIGGER IF NOT EXISTS runbook_allowances_revoke_only
  BEFORE UPDATE OF id,version_id,machine,hash,person,account_id,via,created_at ON runbook_allowances
  BEGIN SELECT RAISE(ABORT,'runbook_allowances: an allowance can only be revoked'); END;
CREATE TRIGGER IF NOT EXISTS runbook_allowances_revoked_once
  BEFORE UPDATE OF revoked_at ON runbook_allowances WHEN old.revoked_at IS NOT NULL
  BEGIN SELECT RAISE(ABORT,'runbook_allowances: already revoked'); END;
CREATE TRIGGER IF NOT EXISTS runbook_allowances_no_delete BEFORE DELETE ON runbook_allowances
  BEGIN SELECT RAISE(ABORT,'runbook_allowances is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_versions_immutable BEFORE UPDATE ON runbook_versions
  BEGIN SELECT RAISE(ABORT,'runbook_versions is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_versions_no_delete BEFORE DELETE ON runbook_versions
  BEGIN SELECT RAISE(ABORT,'runbook_versions is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_approvals_immutable BEFORE UPDATE ON runbook_approvals
  BEGIN SELECT RAISE(ABORT,'runbook_approvals is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_approvals_no_delete BEFORE DELETE ON runbook_approvals
  BEGIN SELECT RAISE(ABORT,'runbook_approvals is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_runs_immutable BEFORE UPDATE ON runbook_runs
  BEGIN SELECT RAISE(ABORT,'runbook_runs is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_runs_no_delete BEFORE DELETE ON runbook_runs
  BEGIN SELECT RAISE(ABORT,'runbook_runs is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbooks_no_delete BEFORE DELETE ON runbooks
  BEGIN SELECT RAISE(ABORT,'runbooks is immutable'); END;
CREATE TRIGGER IF NOT EXISTS runbook_proposals_decide_only
  BEFORE UPDATE OF id,runbook_id,base_version_id,run_id,note,deviations,person,author_id,created_at ON runbook_proposals
  BEGIN SELECT RAISE(ABORT,'runbook_proposals: only the decision may change'); END;
CREATE TRIGGER IF NOT EXISTS runbook_proposals_decided_once
  BEFORE UPDATE OF state ON runbook_proposals WHEN old.state != 'open'
  BEGIN SELECT RAISE(ABORT,'runbook_proposals: already decided'); END;
CREATE TRIGGER IF NOT EXISTS runbook_proposals_no_delete BEFORE DELETE ON runbook_proposals
  BEGIN SELECT RAISE(ABORT,'runbook_proposals is immutable'); END;
`

// Grenzen gegen Zumüllen durch einen einzelnen Schreiber.
const (
	maxRunbookVersions  = 200
	maxRunbooksPerScope = 500
	maxOpenProposals    = 20
)

// ensureRunbooks legt die Tabellen additiv an und darf beliebig oft laufen. Er
// läuft nach ensureGitAnchors und ensureOriginContexts, auf deren Tabellen die
// Fremdschlüssel zeigen.
func ensureRunbooks(db *sql.DB) error {
	_, err := db.Exec(runbookSchema)
	return err
}

// ErrRunbookNotFound: es gibt den Ablauf (oder die Fassung) nicht.
var ErrRunbookNotFound = errors.New("runbook not found")

// ErrRunbookConflict: der Zustand erlaubt die Aktion nicht (veralteter oder
// schon entschiedener Vorschlag, zu viele Fassungen).
type ErrRunbookConflict struct{ Msg string }

func (e *ErrRunbookConflict) Error() string { return e.Msg }

// RunbookRef nennt, was die Zugriffsprüfung von einem Ablauf braucht.
type RunbookRef struct {
	ID       int64
	Project  string
	Machine  string
	Name     string
	AuthorID int64
	Person   string
}

// RunbookWrite ist ein Schreibvorgang: neuer Ablauf oder neue Fassung unter
// (Projekt, Maschine, Name). Approve verlangt die Freigabe dieser Fassung durch
// den Schreiber; der Server setzt es nur, wenn der Schreiber freigeben darf.
type RunbookWrite struct {
	Project  string
	Machine  string
	Name     string
	Spec     runbook.Spec
	Note     string
	Person   string
	AuthorID int64
	Approve  bool
	Anchor   *gitanchor.Anchor
	Origin   *originctx.Context
}

// RunbookWriteResult sagt, was der Schreibvorgang bewirkt hat.
type RunbookWriteResult struct {
	ID         int64
	Version    int
	Hash       string
	NewRunbook bool
	// NewVersion ist false, wenn der Inhalt der Spitze gleicht: nichts wurde
	// angelegt (höchstens eine Freigabe nachgetragen).
	NewVersion bool
	Approved   bool
}

func runbookRefScan(row interface{ Scan(...any) error }) (RunbookRef, error) {
	var r RunbookRef
	err := row.Scan(&r.ID, &r.Project, &r.Machine, &r.Name, &r.AuthorID, &r.Person)
	if err == sql.ErrNoRows {
		return r, ErrRunbookNotFound
	}
	return r, err
}

const runbookRefCols = `id, project, machine, name, author_id, person`

// RunbookRefByID liefert die Eckdaten für die Zugriffsprüfung.
func (s *Store) RunbookRefByID(id int64) (RunbookRef, error) {
	if s.reader != nil {
		return s.reader.RunbookRefByID(id)
	}
	return runbookRefScan(s.db.QueryRow(`SELECT `+runbookRefCols+` FROM runbooks WHERE id=?`, id))
}

// globalOwner ist der Autor, der in der Eindeutigkeit und in der Sicht eines
// Ablaufs ohne Projekt und Maschine zählt; sonst 0.
func globalOwner(project, machine string, account int64) int64 {
	if project == "" && machine == "" {
		return account
	}
	return 0
}

// RunbookRefByScope liefert den Ablauf mit genau dieser Geltung und diesem
// Namen. Ohne Projekt und Maschine ist es der des Kontos account.
func (s *Store) RunbookRefByScope(project, machine, name string, account int64) (RunbookRef, error) {
	if s.reader != nil {
		return s.reader.RunbookRefByScope(project, machine, name, account)
	}
	project, machine = scope.NormalizeRemote(project), canonicalMachine(machine)
	return runbookRefScan(s.db.QueryRow(`SELECT `+runbookRefCols+` FROM runbooks WHERE project=? AND machine=? AND name=?
		AND (CASE WHEN project='' AND machine='' THEN author_id ELSE 0 END)=?`, project, machine, name, globalOwner(project, machine, account)))
}

// applicable ist die Bedingung "gilt im Kontext ax" für die Spalten project und
// machine: global, nur Maschine, nur Projekt, Projekt auf Maschine. Der Branch
// spielt bei Abläufen keine Rolle.
func runbookApplicable(ax scope.Axes, account int64) (string, []any) {
	machine := canonicalMachine(ax.Machine)
	project := scope.NormalizeRemote(ax.Project)
	// Was weder Projekt noch Maschine hat, gehört dem Autor allein.
	clauses := []string{`(project='' AND machine='' AND author_id=?)`}
	args := []any{account}
	if machine != "" {
		clauses = append(clauses, `(project='' AND machine=?)`)
		args = append(args, machine)
	}
	if project != "" {
		clauses = append(clauses, `(project=? AND machine='')`)
		args = append(args, project)
		if machine != "" {
			clauses = append(clauses, `(project=? AND machine=?)`)
			args = append(args, project, machine)
		}
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// RunbookCandidates liefert die Abläufe dieses Namens, die im Kontext gelten,
// den spezifischsten zuerst (Projekt auf Maschine vor Projekt vor Maschine vor
// global). Ein Ablauf mit einer freigegebenen Fassung steht vor jedem ohne
// jede Freigabe, auch vor einem spezifischeren: ein Mitglied kann einen Ablauf
// (P, M, name) anlegen, der sonst den freigegebenen (P, name) auf M verdrängte,
// und ausgeliefert würde eine Fassung, die niemand freigegeben hat. Bei gleichem
// Stand der Freigabe gilt der spezifischere. Die Zugriffsprüfung macht der
// Aufrufer.
func (s *Store) RunbookCandidates(name string, ax scope.Axes, account int64) ([]RunbookRef, error) {
	if s.reader != nil {
		return s.reader.RunbookCandidates(name, ax, account)
	}
	where, args := runbookApplicable(ax, account)
	rows, err := s.db.Query(`SELECT `+runbookRefCols+` FROM runbooks WHERE name=? AND `+where+
		` ORDER BY (project!='')+(machine!='') DESC, id`, append([]any{name}, args...)...)
	if err != nil {
		return nil, err
	}
	var out []RunbookRef
	for rows.Next() {
		r, err := runbookRefScan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Erst schließen: mit einer einzigen Verbindung wartete die Prüfung sonst.
	rows.Close()
	if len(out) < 2 {
		return out, nil
	}
	approved := make(map[int64]bool, len(out))
	for _, r := range out {
		versions, err := loadVersionRows(s.db, r.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if _, ok := approverOf(s.db, r, v.rowID); ok {
				approved[r.ID] = true
				break
			}
		}
	}
	// Ein Ablauf des Projekts geht immer vor einem ohne Projekt (nur Maschine
	// oder global): dessen Freigabe stammt nicht aus dem Projekt. Innerhalb
	// derselben Stufe geht ein freigegebener vor einem spezifischeren ohne jede
	// Freigabe, sonst bleibt die Reihenfolge nach Spezifität.
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Project != "", out[j].Project != ""
		if pi != pj {
			return pi
		}
		return approved[out[i].ID] && !approved[out[j].ID]
	})
	return out, nil
}

// RunbookRefsIn liefert alle Abläufe, die im Kontext gelten.
func (s *Store) RunbookRefsIn(ax scope.Axes, account int64) ([]RunbookRef, error) {
	if s.reader != nil {
		return s.reader.RunbookRefsIn(ax, account)
	}
	where, args := runbookApplicable(ax, account)
	rows, err := s.db.Query(`SELECT `+runbookRefCols+` FROM runbooks WHERE `+where+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunbookRef
	for rows.Next() {
		r, err := runbookRefScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type runbookVersionRow struct {
	rowID     int64
	version   int
	spec      string
	hash      string
	note      string
	person    string
	createdAt string
}

// approvedBy liefert, wer die Fassung freigegeben hat, soweit das noch gilt: im
// Projekt nur, wer dort jetzt Owner oder Lead ist; ohne Projekt nur der Autor
// des Ablaufs, dem diese Geltung allein gehört. Eine Freigabe von jemandem, der
// inzwischen herabgestuft oder entfernt wurde, zählt nicht mehr.
func approverOf(q queryer, ref RunbookRef, versionRowID int64) (string, bool) {
	rows, err := q.Query(`SELECT approver_id, person FROM runbook_approvals WHERE version_id=? ORDER BY created_at, approver_id`, versionRowID)
	if err != nil {
		return "", false
	}
	type approval struct {
		id     int64
		person string
	}
	var list []approval
	for rows.Next() {
		var a approval
		if rows.Scan(&a.id, &a.person) == nil {
			list = append(list, a)
		}
	}
	// Erst schließen, dann nach der Rolle fragen: mit einer einzigen
	// Verbindung wartete die zweite Abfrage sonst auf die erste.
	rows.Close()
	for _, a := range list {
		id, person := a.id, a.person
		if ref.Project == "" {
			if id == ref.AuthorID && id > 0 {
				return person, true
			}
			continue
		}
		if RoleRank(projectRoleTx(q, ref.Project, id).Role) >= 3 {
			return person, true
		}
	}
	return "", false
}

func loadVersionRows(q queryer, runbookID int64) ([]runbookVersionRow, error) {
	rows, err := q.Query(`SELECT id, version, spec, hash, note, person, created_at FROM runbook_versions WHERE runbook_id=? ORDER BY version`, runbookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runbookVersionRow
	for rows.Next() {
		var v runbookVersionRow
		if err := rows.Scan(&v.rowID, &v.version, &v.spec, &v.hash, &v.note, &v.person, &v.createdAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) viewOf(ref RunbookRef, v runbookVersionRow, all []runbookVersionRow) (runbook.View, error) {
	var spec runbook.Spec
	if err := json.Unmarshal([]byte(v.spec), &spec); err != nil {
		return runbook.View{}, fmt.Errorf("runbook %d version %d: %w", ref.ID, v.version, err)
	}
	view := runbook.View{
		ID: ref.ID, Name: ref.Name, Project: ref.Project, Machine: ref.Machine,
		Version: v.version, Versions: len(all), Head: all[len(all)-1].version,
		Hash: v.hash, Spec: spec, Note: v.note, Person: v.person, CreatedAt: v.createdAt,
	}
	if by, ok := approverOf(s.db, ref, v.rowID); ok {
		view.Approved, view.ApprovedBy = true, by
	}
	for i := len(all) - 1; i >= 0; i-- {
		if _, ok := approverOf(s.db, ref, all[i].rowID); ok {
			view.ApprovedVersion = all[i].version
			break
		}
	}
	if anchors, err := s.anchorsFor("runbook_versions", []int64{v.rowID}); err == nil {
		view.GitAnchor = anchors[v.rowID]
	} else {
		return view, err
	}
	if origins, err := s.originsFor("runbook_versions", []int64{v.rowID}); err == nil {
		view.OriginContext = origins[v.rowID]
	} else {
		return view, err
	}
	runs, err := s.runbookRuns(ref.ID, 1, true)
	if err != nil {
		return view, err
	}
	if len(runs) > 0 {
		view.LastSuccess = &runs[0]
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runbook_proposals WHERE runbook_id=? AND state='open'`, ref.ID).Scan(&view.PendingProposals); err != nil {
		return view, err
	}
	return view, nil
}

// RunbookView liefert den Ablauf in einer Fassung; version 0 ist die Spitze.
func (s *Store) RunbookView(id int64, version int) (runbook.View, error) {
	if s.reader != nil {
		return s.reader.RunbookView(id, version)
	}
	ref, err := runbookRefScan(s.db.QueryRow(`SELECT `+runbookRefCols+` FROM runbooks WHERE id=?`, id))
	if err != nil {
		return runbook.View{}, err
	}
	all, err := loadVersionRows(s.db, id)
	if err != nil {
		return runbook.View{}, err
	}
	if len(all) == 0 {
		return runbook.View{}, ErrRunbookNotFound
	}
	pick := all[len(all)-1]
	if version > 0 {
		found := false
		for _, v := range all {
			if v.version == version {
				pick, found = v, true
			}
		}
		if !found {
			return runbook.View{}, ErrRunbookNotFound
		}
	}
	return s.viewOf(ref, pick, all)
}

// RunbookHistory liefert alle Fassungen, älteste zuerst, jede mit Inhalt, Hash,
// Schreiber, Anker und Stand der Freigabe.
func (s *Store) RunbookHistory(id int64) ([]runbook.View, error) {
	if s.reader != nil {
		return s.reader.RunbookHistory(id)
	}
	ref, err := runbookRefScan(s.db.QueryRow(`SELECT `+runbookRefCols+` FROM runbooks WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	all, err := loadVersionRows(s.db, id)
	if err != nil {
		return nil, err
	}
	out := make([]runbook.View, 0, len(all))
	for _, v := range all {
		view, err := s.viewOf(ref, v, all)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

// RunbookSummary ist die Listenzeile eines Ablaufs.
func (s *Store) RunbookSummary(ref RunbookRef) (runbook.Summary, error) {
	if s.reader != nil {
		return s.reader.RunbookSummary(ref)
	}
	all, err := loadVersionRows(s.db, ref.ID)
	if err != nil {
		return runbook.Summary{}, err
	}
	if len(all) == 0 {
		return runbook.Summary{}, ErrRunbookNotFound
	}
	head := all[len(all)-1]
	var spec runbook.Spec
	if err := json.Unmarshal([]byte(head.spec), &spec); err != nil {
		return runbook.Summary{}, err
	}
	sum := runbook.Summary{ID: ref.ID, Name: ref.Name, Project: ref.Project, Machine: ref.Machine,
		Purpose: spec.Purpose, Head: head.version, Targets: len(spec.Targets)}
	for i := len(all) - 1; i >= 0; i-- {
		if _, ok := approverOf(s.db, ref, all[i].rowID); ok {
			sum.ApprovedVersion = all[i].version
			break
		}
	}
	runs, err := s.runbookRuns(ref.ID, 1, true)
	if err != nil {
		return sum, err
	}
	if len(runs) > 0 {
		sum.LastSuccessAt = runs[0].CreatedAt
		if runs[0].GitAnchor != nil {
			sum.LastCommit = runs[0].GitAnchor.Commit
		}
	}
	return sum, nil
}

// RunbookRuns liefert die letzten Läufe, neueste zuerst.
func (s *Store) RunbookRuns(id int64, limit int) ([]runbook.Run, error) {
	if s.reader != nil {
		return s.reader.RunbookRuns(id, limit)
	}
	return s.runbookRuns(id, limit, false)
}

func (s *Store) runbookRuns(id int64, limit int, onlyOK bool) ([]runbook.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT r.id, v.version, r.target, r.mode, r.result, r.params, r.note, r.machine, r.person, r.deviations, r.created_at
		FROM runbook_runs r JOIN runbook_versions v ON v.id=r.version_id WHERE r.runbook_id=?`
	if onlyOK {
		q += ` AND r.result='ok'`
	}
	rows, err := s.db.Query(q+` ORDER BY r.id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	var out []runbook.Run
	var ids []int64
	for rows.Next() {
		var r runbook.Run
		var params, devs string
		if err := rows.Scan(&r.ID, &r.Version, &r.Target, &r.Mode, &r.Result, &params, &r.Note, &r.Machine, &r.Person, &devs, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(params), &r.Params)
		_ = json.Unmarshal([]byte(devs), &r.Deviations)
		out = append(out, r)
		ids = append(ids, r.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	anchors, err := s.anchorsFor("runbook_runs", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].GitAnchor = anchors[out[i].ID]
	}
	return out, nil
}

// RunbookProposals liefert die Vorschläge zu einem Ablauf, neueste zuerst;
// state leer heißt alle.
func (s *Store) RunbookProposals(id int64, state string) ([]runbook.Proposal, error) {
	if s.reader != nil {
		return s.reader.RunbookProposals(id, state)
	}
	q := `SELECT p.id, p.runbook_id, bv.version, COALESCE(p.run_id,0), p.state, p.note, p.deviations, p.person, p.decided_by,
		COALESCE(rv.version,0), p.created_at
		FROM runbook_proposals p JOIN runbook_versions bv ON bv.id=p.base_version_id
		LEFT JOIN runbook_versions rv ON rv.id=p.result_version_id WHERE p.runbook_id=?`
	args := []any{id}
	if state != "" {
		q += ` AND p.state=?`
		args = append(args, state)
	}
	rows, err := s.db.Query(q+` ORDER BY p.id DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProposals(rows)
}

func scanProposals(rows *sql.Rows) ([]runbook.Proposal, error) {
	var out []runbook.Proposal
	for rows.Next() {
		var p runbook.Proposal
		var devs string
		if err := rows.Scan(&p.ID, &p.RunbookID, &p.BaseVersion, &p.RunID, &p.State, &p.Note, &devs, &p.Person, &p.DecidedBy, &p.ResultVer, &p.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(devs), &p.Deviations)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RunbookProposalByID liefert einen Vorschlag; ErrRunbookNotFound ohne.
func (s *Store) RunbookProposalByID(id int64) (runbook.Proposal, error) {
	if s.reader != nil {
		return s.reader.RunbookProposalByID(id)
	}
	rows, err := s.db.Query(`SELECT p.id, p.runbook_id, bv.version, COALESCE(p.run_id,0), p.state, p.note, p.deviations, p.person, p.decided_by,
		COALESCE(rv.version,0), p.created_at
		FROM runbook_proposals p JOIN runbook_versions bv ON bv.id=p.base_version_id
		LEFT JOIN runbook_versions rv ON rv.id=p.result_version_id WHERE p.id=?`, id)
	if err != nil {
		return runbook.Proposal{}, err
	}
	defer rows.Close()
	ps, err := scanProposals(rows)
	if err != nil {
		return runbook.Proposal{}, err
	}
	if len(ps) == 0 {
		return runbook.Proposal{}, ErrRunbookNotFound
	}
	return ps[0], nil
}

// RunbookDeliverable ist ein Ablauf mit Auslösern in der Fassung, die ein Agent
// ohne Rückfrage bekommen darf: der neuesten freigegebenen.
type RunbookDeliverable struct {
	Ref  RunbookRef
	View runbook.View
}

// RunbookTriggerable liefert zu den Abläufen, die im Kontext gelten und
// Auslöser haben, jeweils die neueste freigegebene Fassung. Ein Ablauf ohne
// freigegebene Fassung wird nie von selbst ausgeliefert: was nur ein Mitglied
// geschrieben hat, wird nicht ungefragt zur Anleitung für einen Agenten.
func (s *Store) RunbookTriggerable(ax scope.Axes, account int64) ([]RunbookDeliverable, error) {
	if s.reader != nil {
		return s.reader.RunbookTriggerable(ax, account)
	}
	refs, err := s.RunbookRefsIn(ax, account)
	if err != nil {
		return nil, err
	}
	var out []RunbookDeliverable
	for _, ref := range refs {
		all, err := loadVersionRows(s.db, ref.ID)
		if err != nil || len(all) == 0 {
			continue
		}
		for i := len(all) - 1; i >= 0; i-- {
			if _, ok := approverOf(s.db, ref, all[i].rowID); !ok {
				continue
			}
			view, err := s.viewOf(ref, all[i], all)
			if err != nil {
				return nil, err
			}
			if view.Spec.HasTriggers() {
				out = append(out, RunbookDeliverable{Ref: ref, View: view})
			}
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref.ID < out[j].Ref.ID })
	return out, nil
}

// WriteRunbook legt einen Ablauf oder eine neue Fassung an. Gleicht der
// normalisierte Inhalt dem der Spitze, entsteht keine Fassung; verlangt
// Approve, wird die Freigabe nachgetragen. Hash und Inhalt entstehen hier, nie
// aus dem Aufruf.
func (s *Store) WriteRunbook(in RunbookWrite) (RunbookWriteResult, error) {
	in.Project, in.Machine = scope.NormalizeRemote(in.Project), canonicalMachine(in.Machine)
	if err := runbook.ValidName(in.Name); err != nil {
		return RunbookWriteResult{}, err
	}
	spec, err := runbook.Normalize(in.Spec)
	if err != nil {
		return RunbookWriteResult{}, err
	}
	in.Spec = spec
	if in.Note, err = runbook.CheckNote(in.Note); err != nil {
		return RunbookWriteResult{}, err
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (RunbookWriteResult, error) {
			return d.WriteRunbook(p[0].(RunbookWrite))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return RunbookWriteResult{}, err
	}
	defer tx.Rollback()
	res := RunbookWriteResult{}
	at := now()
	var rid int64
	err = tx.QueryRow(`SELECT id FROM runbooks WHERE project=? AND machine=? AND name=?
		AND (CASE WHEN project='' AND machine='' THEN author_id ELSE 0 END)=?`, in.Project, in.Machine, in.Name, globalOwner(in.Project, in.Machine, in.AuthorID)).Scan(&rid)
	if err == sql.ErrNoRows {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM runbooks WHERE project=? AND machine=? AND (? = 0 OR author_id=?)`, in.Project, in.Machine, globalOwner(in.Project, in.Machine, in.AuthorID), in.AuthorID).Scan(&n); err != nil {
			return res, err
		}
		if n >= maxRunbooksPerScope {
			return res, &ErrRunbookConflict{Msg: fmt.Sprintf("more than %d runbooks in one scope", maxRunbooksPerScope)}
		}
		r, err := tx.Exec(`INSERT INTO runbooks(project,machine,name,person,author_id,created_at) VALUES(?,?,?,?,?,?)`,
			in.Project, in.Machine, in.Name, in.Person, in.AuthorID, at)
		if err != nil {
			return res, err
		}
		if rid, err = r.LastInsertId(); err != nil {
			return res, err
		}
		res.NewRunbook = true
	} else if err != nil {
		return res, err
	}
	hash := runbook.Hash(in.Project, in.Machine, in.Name, in.Spec)
	res.ID, res.Hash = rid, hash
	var headID int64
	var headVersion int
	var headHash string
	err = tx.QueryRow(`SELECT id, version, hash FROM runbook_versions WHERE runbook_id=? ORDER BY version DESC LIMIT 1`, rid).Scan(&headID, &headVersion, &headHash)
	if err != nil && err != sql.ErrNoRows {
		return res, err
	}
	versionRowID := headID
	if err == sql.ErrNoRows || headHash != hash {
		if headVersion >= maxRunbookVersions {
			return res, &ErrRunbookConflict{Msg: fmt.Sprintf("a runbook keeps at most %d versions", maxRunbookVersions)}
		}
		raw, err := json.Marshal(in.Spec)
		if err != nil {
			return res, err
		}
		anchorID, originID, err := insertWriteMetaTx(tx, in.Anchor, in.Origin)
		if err != nil {
			return res, err
		}
		r, err := tx.Exec(`INSERT INTO runbook_versions(runbook_id,version,spec,hash,note,person,author_id,created_at,anchor_id,origin_id) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			rid, headVersion+1, string(raw), hash, in.Note, in.Person, in.AuthorID, at, anchorID, originID)
		if err != nil {
			return res, err
		}
		if versionRowID, err = r.LastInsertId(); err != nil {
			return res, err
		}
		headVersion++
		res.NewVersion = true
	}
	res.Version = headVersion
	if in.Approve {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO runbook_approvals(version_id,approver_id,person,created_at) VALUES(?,?,?,?)`,
			versionRowID, in.AuthorID, in.Person, at); err != nil {
			return res, err
		}
		res.Approved = true
	}
	return res, tx.Commit()
}

// ApproveRunbookVersion trägt die Freigabe einer Fassung nach. Ob der
// Freigebende dürfen darf, hat der Aufrufer geprüft; zählen tut sie später nur,
// solange er Owner oder Lead ist (approverOf).
func (s *Store) ApproveRunbookVersion(id int64, version int, approverID int64, person string) error {
	if s.writer != nil {
		return queueWrite(s, []any{id, version, approverID, person}, func(d *Store, p []any) error {
			return d.ApproveRunbookVersion(p[0].(int64), p[1].(int), p[2].(int64), p[3].(string))
		})
	}
	var rowID int64
	err := s.db.QueryRow(`SELECT id FROM runbook_versions WHERE runbook_id=? AND version=?`, id, version).Scan(&rowID)
	if err == sql.ErrNoRows {
		return ErrRunbookNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO runbook_approvals(version_id,approver_id,person,created_at) VALUES(?,?,?,?)`, rowID, approverID, person, now())
	return err
}

// RunbookRunInput ist die Rückmeldung zu einem Lauf. Weichen Schritte ab,
// entsteht in derselben Transaktion ein Vorschlag.
type RunbookRunInput struct {
	RunbookID  int64
	Version    int
	Target     string
	Mode       string
	Result     string
	Params     map[string]string
	Note       string
	Machine    string
	Person     string
	AuthorID   int64
	Deviations []runbook.Deviation
	Anchor     *gitanchor.Anchor
	Origin     *originctx.Context
}

// RunbookRunResult nennt den Lauf und, falls entstanden, den Vorschlag.
type RunbookRunResult struct {
	RunID      int64
	ProposalID int64
}

// RecordRunbookRun protokolliert einen Lauf mit Fassung, Maschine, Person und
// Anker. Ein Lauf ändert nie einen Ablauf; Abweichungen werden zu einem
// Vorschlag, den ein Owner oder Lead freigeben muss.
func (s *Store) RecordRunbookRun(in RunbookRunInput) (RunbookRunResult, error) {
	switch in.Mode {
	case runbook.ModeChecklist, runbook.ModeExec:
	default:
		return RunbookRunResult{}, fmt.Errorf("mode %q: use checklist or exec", in.Mode)
	}
	switch in.Result {
	case runbook.ResultOK, runbook.ResultFailed, runbook.ResultAborted:
	default:
		return RunbookRunResult{}, fmt.Errorf("result %q: use ok, failed or aborted", in.Result)
	}
	var err error
	if in.Note, err = runbook.CheckNote(in.Note); err != nil {
		return RunbookRunResult{}, err
	}
	if in.Deviations, err = runbook.NormalizeDeviations(in.Deviations); err != nil {
		return RunbookRunResult{}, err
	}
	if in.Target != "" {
		if err := runbook.ValidTargetName(in.Target); err != nil {
			return RunbookRunResult{}, err
		}
	}
	if len(in.Params) > runbook.MaxParams {
		return RunbookRunResult{}, fmt.Errorf("more than %d parameters", runbook.MaxParams)
	}
	for k, v := range in.Params {
		if _, err := runbook.CheckValue(runbook.Param{Name: k, Type: runbook.TypeString}, v); err != nil {
			return RunbookRunResult{}, err
		}
	}
	in.Machine = canonicalMachine(in.Machine)
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (RunbookRunResult, error) {
			return d.RecordRunbookRun(p[0].(RunbookRunInput))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return RunbookRunResult{}, err
	}
	defer tx.Rollback()
	var versionRowID int64
	err = tx.QueryRow(`SELECT id FROM runbook_versions WHERE runbook_id=? AND version=?`, in.RunbookID, in.Version).Scan(&versionRowID)
	if err == sql.ErrNoRows {
		return RunbookRunResult{}, ErrRunbookNotFound
	}
	if err != nil {
		return RunbookRunResult{}, err
	}
	params, _ := json.Marshal(in.Params)
	if in.Params == nil {
		params = []byte("{}")
	}
	devs, _ := json.Marshal(in.Deviations)
	if len(in.Deviations) == 0 {
		devs = []byte("[]")
	}
	anchorID, originID, err := insertWriteMetaTx(tx, in.Anchor, in.Origin)
	if err != nil {
		return RunbookRunResult{}, err
	}
	at := now()
	r, err := tx.Exec(`INSERT INTO runbook_runs(runbook_id,version_id,target,mode,result,params,note,machine,person,author_id,deviations,created_at,anchor_id,origin_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.RunbookID, versionRowID, in.Target, in.Mode, in.Result, string(params), in.Note, in.Machine, in.Person, in.AuthorID, string(devs), at, anchorID, originID)
	if err != nil {
		return RunbookRunResult{}, err
	}
	out := RunbookRunResult{}
	if out.RunID, err = r.LastInsertId(); err != nil {
		return out, err
	}
	if len(in.Deviations) > 0 {
		var open int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM runbook_proposals WHERE runbook_id=? AND state='open'`, in.RunbookID).Scan(&open); err != nil {
			return out, err
		}
		if open < maxOpenProposals {
			p, err := tx.Exec(`INSERT INTO runbook_proposals(runbook_id,base_version_id,run_id,note,deviations,person,author_id,created_at) VALUES(?,?,?,?,?,?,?,?)`,
				in.RunbookID, versionRowID, out.RunID, in.Note, string(devs), in.Person, in.AuthorID, at)
			if err != nil {
				return out, err
			}
			if out.ProposalID, err = p.LastInsertId(); err != nil {
				return out, err
			}
		}
	}
	return out, tx.Commit()
}

// DecideRunbookProposal nimmt einen Vorschlag an oder lehnt ihn ab. Angenommen
// wird nur, was auf der Spitze aufsetzt (sonst veraltet, Konflikt); es entsteht
// eine neue Fassung, die der Entscheidende freigibt. Der Vorschlag ändert nur
// Schritte der Anleitung, nie die ausführbaren Ziele. Ob der Entscheidende
// dürfen darf, hat der Aufrufer geprüft.
func (s *Store) DecideRunbookProposal(proposalID int64, accept bool, approverID int64, person string, anchor *gitanchor.Anchor, origin *originctx.Context) (runbook.Proposal, error) {
	if s.writer != nil {
		return queueValue(s, []any{proposalID, accept, approverID, person, anchor, origin}, func(d *Store, p []any) (runbook.Proposal, error) {
			a, _ := p[4].(*gitanchor.Anchor)
			o, _ := p[5].(*originctx.Context)
			return d.DecideRunbookProposal(p[0].(int64), p[1].(bool), p[2].(int64), p[3].(string), a, o)
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return runbook.Proposal{}, err
	}
	defer tx.Rollback()
	var rid, baseRow int64
	var state, devsRaw, proposer string
	var proposerID int64
	err = tx.QueryRow(`SELECT runbook_id, base_version_id, state, deviations, person, author_id FROM runbook_proposals WHERE id=?`, proposalID).
		Scan(&rid, &baseRow, &state, &devsRaw, &proposer, &proposerID)
	if err == sql.ErrNoRows {
		return runbook.Proposal{}, ErrRunbookNotFound
	}
	if err != nil {
		return runbook.Proposal{}, err
	}
	if state != "open" {
		return runbook.Proposal{}, &ErrRunbookConflict{Msg: "this proposal was already decided"}
	}
	var resultRow any
	newState := "rejected"
	if accept {
		newState = "accepted"
		var devs []runbook.Deviation
		if err := json.Unmarshal([]byte(devsRaw), &devs); err != nil {
			return runbook.Proposal{}, err
		}
		var headRow int64
		var headVersion int
		var headSpecRaw, project, machine, name string
		if err := tx.QueryRow(`SELECT v.id, v.version, v.spec FROM runbook_versions v WHERE v.runbook_id=? ORDER BY v.version DESC LIMIT 1`, rid).
			Scan(&headRow, &headVersion, &headSpecRaw); err != nil {
			return runbook.Proposal{}, err
		}
		if headRow != baseRow {
			return runbook.Proposal{}, &ErrRunbookConflict{Msg: "the runbook changed since this proposal was made; reject it and report again from the new version"}
		}
		if err := tx.QueryRow(`SELECT project, machine, name FROM runbooks WHERE id=?`, rid).Scan(&project, &machine, &name); err != nil {
			return runbook.Proposal{}, err
		}
		var spec runbook.Spec
		if err := json.Unmarshal([]byte(headSpecRaw), &spec); err != nil {
			return runbook.Proposal{}, err
		}
		next, err := runbook.ApplyDeviations(spec, devs)
		if err != nil {
			return runbook.Proposal{}, &ErrRunbookConflict{Msg: "the proposal no longer applies: " + err.Error()}
		}
		raw, _ := json.Marshal(next)
		anchorID, originID, err := insertWriteMetaTx(tx, anchor, origin)
		if err != nil {
			return runbook.Proposal{}, err
		}
		at := now()
		r, err := tx.Exec(`INSERT INTO runbook_versions(runbook_id,version,spec,hash,note,person,author_id,created_at,anchor_id,origin_id) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			rid, headVersion+1, string(raw), runbook.Hash(project, machine, name, next),
			fmt.Sprintf("from proposal %d, approved by %s", proposalID, person), proposer, proposerID, at, anchorID, originID)
		if err != nil {
			return runbook.Proposal{}, err
		}
		vid, err := r.LastInsertId()
		if err != nil {
			return runbook.Proposal{}, err
		}
		if _, err := tx.Exec(`INSERT INTO runbook_approvals(version_id,approver_id,person,created_at) VALUES(?,?,?,?)`, vid, approverID, person, at); err != nil {
			return runbook.Proposal{}, err
		}
		resultRow = vid
	}
	if _, err := tx.Exec(`UPDATE runbook_proposals SET state=?, decided_by=?, result_version_id=? WHERE id=?`, newState, person, resultRow, proposalID); err != nil {
		return runbook.Proposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return runbook.Proposal{}, err
	}
	return s.RunbookProposalByID(proposalID)
}
