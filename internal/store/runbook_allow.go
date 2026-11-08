package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
)

// runbookExecution ist der Schalter für die Ausführung (REQ-604, Stufe 2). Er
// steht standardmäßig AUS: ohne ihn legt kein Aufruf eine Freigabe an, und der
// Server meldet jede Fassung als nicht freigegeben. Betreiber schalten ihn mit
// GHOSTTREE_RUNBOOK_EXEC=1 beim Start von ctx serve ein. Er gilt für den Prozess.
var runbookExecution atomic.Bool

// SetRunbookExecution schaltet die Ausführung von Abläufen frei oder sperrt sie.
func SetRunbookExecution(on bool) { runbookExecution.Store(on) }

// RunbookExecutionEnabled sagt, ob die Ausführung freigeschaltet ist.
func RunbookExecutionEnabled() bool { return runbookExecution.Load() }

// ErrRunbookExecutionOff: die Ausführung von Abläufen ist auf diesem Server nicht
// freigeschaltet.
var ErrRunbookExecutionOff = fmt.Errorf("running runbooks is switched off on this server (GHOSTTREE_RUNBOOK_EXEC=1 turns it on)")

// RunbookAllowRequest ist die Freigabe einer Fassung für eine Maschine. Sie
// entsteht nur durch einen Menschen: in der Weboberfläche (via "web") oder durch
// den Betreiber mit Datenbankzugriff (via "cli-db"), nie über ein Token der API.
type RunbookAllowRequest struct {
	RunbookID int64
	Version   int
	// Hash muss der Inhalts-Hash der Fassung sein, die der Mensch gesehen hat.
	Hash      string
	Machine   string
	Person    string
	AccountID int64
	Via       string
}

// RunbookAllowState ist die Antwort auf "darf diese Fassung auf dieser Maschine
// laufen".
type RunbookAllowState struct {
	Allowed   bool   `json:"allowed"`
	Reason    string `json:"reason,omitempty"`
	Version   int    `json:"version"`
	Hash      string `json:"hash,omitempty"`
	Machine   string `json:"machine"`
	AllowedBy string `json:"allowed_by,omitempty"`
	At        string `json:"at,omitempty"`
}

// AllowRunbook trägt die Freigabe einer Fassung für eine Maschine ein. Verlangt
// sind: die Ausführung ist eingeschaltet, die Fassung ist von Owner oder Lead
// freigegeben (approverOf) und der Hash ist der der Fassung. Der Aufrufer hat
// geprüft, dass ein Mensch dahinter steht.
func (s *Store) AllowRunbook(in RunbookAllowRequest) error {
	in.Machine = canonicalMachine(in.Machine)
	if in.Machine == "" {
		return fmt.Errorf("name the machine")
	}
	if in.Via != "web" && in.Via != "cli-db" {
		return fmt.Errorf("an allowance comes from the web interface or from the operator, not from %q", in.Via)
	}
	if s.writer != nil {
		return queueWrite(s, []any{in}, func(d *Store, p []any) error {
			return d.AllowRunbook(p[0].(RunbookAllowRequest))
		})
	}
	if !RunbookExecutionEnabled() {
		return ErrRunbookExecutionOff
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ref RunbookRef
	var versionRow int64
	var hash string
	err = tx.QueryRow(`SELECT r.id, r.project, r.machine, r.name, r.author_id, r.person, v.id, v.hash
		FROM runbooks r JOIN runbook_versions v ON v.runbook_id=r.id WHERE r.id=? AND v.version=?`, in.RunbookID, in.Version).
		Scan(&ref.ID, &ref.Project, &ref.Machine, &ref.Name, &ref.AuthorID, &ref.Person, &versionRow, &hash)
	if err == sql.ErrNoRows {
		return ErrRunbookNotFound
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(in.Hash, hash) {
		return &ErrRunbookConflict{Msg: "the hash does not match this version; read it again before allowing it"}
	}
	if _, ok := approverOf(tx, ref, versionRow); !ok {
		return &ErrRunbookConflict{Msg: "this version is not approved by an owner or lead, so it cannot be allowed on a machine"}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO runbook_allowances(version_id,machine,hash,person,account_id,via,created_at) VALUES(?,?,?,?,?,?,?)`,
		versionRow, in.Machine, hash, in.Person, in.AccountID, in.Via, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeRunbookAllowance nimmt die Freigabe einer Fassung für eine Maschine
// zurück. Die Zeile bleibt als Geschichte stehen.
func (s *Store) RevokeRunbookAllowance(id int64, version int, machine string) error {
	machine = canonicalMachine(machine)
	if s.writer != nil {
		return queueWrite(s, []any{id, version, machine}, func(d *Store, p []any) error {
			return d.RevokeRunbookAllowance(p[0].(int64), p[1].(int), p[2].(string))
		})
	}
	res, err := s.db.Exec(`UPDATE runbook_allowances SET revoked_at=? WHERE revoked_at IS NULL AND machine=? AND version_id IN
		(SELECT id FROM runbook_versions WHERE runbook_id=? AND version=?)`, now(), machine, id, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRunbookNotFound
	}
	return nil
}

// RunbookAllowed sagt, ob genau diese Fassung auf dieser Maschine laufen darf.
// Das ist die einzige Quelle dafür: eine Datei auf der Maschine reicht nie. Die
// Antwort verlangt, dass die Ausführung eingeschaltet ist, die Fassung noch von
// jemandem freigegeben ist, der Owner oder Lead ist, und eine Freigabe für
// dieselbe Maschine und denselben Hash vorliegt.
func (s *Store) RunbookAllowed(id int64, version int, machine string) (RunbookAllowState, error) {
	if s.reader != nil {
		return s.reader.RunbookAllowed(id, version, machine)
	}
	machine = canonicalMachine(machine)
	st := RunbookAllowState{Machine: machine, Version: version}
	if !RunbookExecutionEnabled() {
		st.Reason = ErrRunbookExecutionOff.Error()
		return st, nil
	}
	ref, err := runbookRefScan(s.db.QueryRow(`SELECT `+runbookRefCols+` FROM runbooks WHERE id=?`, id))
	if err != nil {
		return st, err
	}
	var versionRow int64
	var hash string
	err = s.db.QueryRow(`SELECT id, hash FROM runbook_versions WHERE runbook_id=? AND version=?`, id, version).Scan(&versionRow, &hash)
	if err == sql.ErrNoRows {
		return st, ErrRunbookNotFound
	}
	if err != nil {
		return st, err
	}
	st.Hash = hash
	if _, ok := approverOf(s.db, ref, versionRow); !ok {
		st.Reason = "this version is not approved by an owner or lead"
		return st, nil
	}
	if machine == "" {
		st.Reason = "no machine named"
		return st, nil
	}
	var by, at string
	err = s.db.QueryRow(`SELECT person, created_at FROM runbook_allowances WHERE version_id=? AND machine=? AND hash=? AND revoked_at IS NULL`,
		versionRow, machine, hash).Scan(&by, &at)
	if err == sql.ErrNoRows {
		st.Reason = fmt.Sprintf("a person has not allowed version %d (hash %s) on machine %s yet", version, hash[:12], machine)
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Allowed, st.AllowedBy, st.At = true, by, at
	return st, nil
}

// RunbookAllowedVersions nennt die Fassungen, die auf dieser Maschine freigegeben
// sind, neueste zuerst, für die Anzeige beim Freigeben ("was war freigegeben").
func (s *Store) RunbookAllowedVersions(id int64, machine string) ([]int, error) {
	if s.reader != nil {
		return s.reader.RunbookAllowedVersions(id, machine)
	}
	rows, err := s.db.Query(`SELECT v.version FROM runbook_allowances a JOIN runbook_versions v ON v.id=a.version_id
		WHERE v.runbook_id=? AND a.machine=? AND a.revoked_at IS NULL ORDER BY v.version DESC`, id, canonicalMachine(machine))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
