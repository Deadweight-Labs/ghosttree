package store

import (
	"database/sql"
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// workPackageSchema (REQ-601): Arbeitspakete binden Requests an Projekt und
// Branch. Neue Tabellen statt Spalten an requests, weil ein Request in mehr als
// einem Paket stehen kann (ein Vorhaben über mehrere Branches) und die Rolle
// zur Mitgliedschaft gehört, nicht zum Request.
//
// Je Projekt und Branch gibt es höchstens ein OFFENES Paket: eine Session auf
// dem Branch muss ohne Rückfrage wissen, welches gilt. Geschlossene Pakete
// bleiben als Geschichte stehen und blockieren den Namen nicht; derselbe Branch
// kann danach ein neues Paket bekommen. Ein PR gehört zu höchstens einem Paket
// des Projekts.
const workPackageSchema = `
CREATE TABLE IF NOT EXISTS work_packages(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  branch TEXT NOT NULL CHECK(branch != ''),
  name TEXT NOT NULL CHECK(name != ''),
  state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','closed')),
  pr INTEGER,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS work_packages_open_branch ON work_packages(project, branch) WHERE state='open';
CREATE UNIQUE INDEX IF NOT EXISTS work_packages_pr ON work_packages(project, pr) WHERE pr IS NOT NULL;
CREATE TABLE IF NOT EXISTS work_package_requests(
  package_id INTEGER NOT NULL REFERENCES work_packages(id),
  request_id INTEGER NOT NULL REFERENCES requests(id),
  role TEXT NOT NULL DEFAULT 'pflicht' CHECK(role IN ('pflicht','optional')),
  added_by TEXT NOT NULL DEFAULT '',
  added_at TEXT NOT NULL,
  PRIMARY KEY(package_id, request_id));
CREATE INDEX IF NOT EXISTS work_package_requests_request ON work_package_requests(request_id);
`

// ensureWorkPackages legt die Tabellen additiv an und darf beliebig oft laufen.
//
// Dazu eine nullbare Spalte an request_relations: die Übernahme einer alten
// branch-Relation markiert sie, statt sie zu löschen (siehe ImportBranchRelations).
func ensureWorkPackages(db *sql.DB) error {
	if _, err := db.Exec(workPackageSchema); err != nil {
		return err
	}
	return ensureColumn(db, "request_relations", "package_id", `INTEGER REFERENCES work_packages(id)`)
}

func packageNotFound(id int64) error {
	return requestdomain.NewRuleError("package_not_found", fmt.Sprintf("no work package PKG-%d", id), "list the packages of the branch to find its id", nil)
}

const packageCols = `id,project,branch,name,state,COALESCE(pr,0),created_by,created_at,updated_at`

func scanPackage(row interface{ Scan(...any) error }, p *requestdomain.Package) error {
	return row.Scan(&p.ID, &p.Project, &p.Branch, &p.Name, &p.State, &p.PR, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
}

// PackageRef nennt das Projekt eines Pakets für die Zugriffsprüfung, ohne die
// Mitglieder zu laden. sql.ErrNoRows, wenn es das Paket nicht gibt.
func (s *Store) PackageRef(id int64) (string, error) {
	if s.reader != nil {
		return s.reader.PackageRef(id)
	}
	var project string
	err := s.db.QueryRow(`SELECT project FROM work_packages WHERE id=?`, id).Scan(&project)
	return project, err
}

// PackageByID liefert das Paket mit seinen Mitgliedern in der Reihenfolge des
// Hinzufügens. Die Mitglieder tragen nur Id, Rolle und Herkunft; Titel und
// Kriterien ergänzt ProjectAccess.PackageView für den Betrachter.
func (s *Store) PackageByID(id int64) (requestdomain.PackageDetail, error) {
	if s.reader != nil {
		return s.reader.PackageByID(id)
	}
	return loadPackage(s.db, id)
}

func loadPackage(q queryer, id int64) (requestdomain.PackageDetail, error) {
	var d requestdomain.PackageDetail
	err := scanPackage(q.QueryRow(`SELECT `+packageCols+` FROM work_packages WHERE id=?`, id), &d.Package)
	if err == sql.ErrNoRows {
		return d, packageNotFound(id)
	}
	if err != nil {
		return d, err
	}
	rows, err := q.Query(`SELECT request_id,role,added_by,added_at FROM work_package_requests WHERE package_id=? ORDER BY added_at, request_id`, id)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.Members = []requestdomain.PackageMemberView{}
	for rows.Next() {
		var m requestdomain.PackageMemberView
		if err := rows.Scan(&m.RequestID, &m.Role, &m.AddedBy, &m.AddedAt); err != nil {
			return d, err
		}
		d.Members = append(d.Members, m)
	}
	return d, rows.Err()
}

// PackageForBranch findet das offene Paket eines Branches. Hat der Branch kein
// eigenes, gilt das des nächsten Vorfahren, der eines hat (#713: ein
// abgezweigter Branch trägt, was der Ursprung hatte). lineage ist die Kette aus
// scope.Axes, nächster Vorfahr zuletzt; gesucht wird von hinten. InheritedFrom
// nennt den Vorfahren, wenn das Paket geerbt ist. sql.ErrNoRows ohne Treffer.
//
// Geschlossene Pakete erben nicht: ein gemergter Branch bleibt für immer in der
// Kette aller später abgezweigten, und sein altes Lastenheft gehört nicht in
// deren Briefing.
func (s *Store) PackageForBranch(project, branch string, lineage []string) (requestdomain.PackageDetail, error) {
	if s.reader != nil {
		return s.reader.PackageForBranch(project, branch, lineage)
	}
	project = scope.NormalizeRemote(project)
	candidates := []string{}
	if branch = strings.TrimSpace(branch); branch != "" {
		candidates = append(candidates, branch)
	}
	for i := len(lineage) - 1; i >= 0; i-- {
		if name := strings.TrimSpace(lineage[i]); name != "" && name != branch {
			candidates = append(candidates, name)
		}
	}
	for _, name := range candidates {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM work_packages WHERE project=? AND branch=? AND state='open'`, project, name).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return requestdomain.PackageDetail{}, err
		}
		d, err := loadPackage(s.db, id)
		if err == nil && name != branch {
			// Der gespeicherte Branch des Pakets, nicht der Name aus der Anfrage:
			// beide sind gleich, aber nur der gespeicherte ist geprüfter Inhalt.
			d.InheritedFrom = d.Branch
		}
		return d, err
	}
	return requestdomain.PackageDetail{}, sql.ErrNoRows
}

// PackageForPR findet das Paket, das an einen PR gebunden ist.
func (s *Store) PackageForPR(project string, pr int64) (requestdomain.PackageDetail, error) {
	if s.reader != nil {
		return s.reader.PackageForPR(project, pr)
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM work_packages WHERE project=? AND pr=?`, scope.NormalizeRemote(project), pr).Scan(&id)
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	return loadPackage(s.db, id)
}

// CreatePackage legt ein Paket an und stellt die genannten Requests hinein.
// Die Requests müssen im selben Projekt liegen.
func (s *Store) CreatePackage(in requestdomain.PackageInput) (requestdomain.PackageDetail, error) {
	in.Project = scope.NormalizeRemote(in.Project)
	in.Branch, in.Name = strings.TrimSpace(in.Branch), strings.TrimSpace(in.Name)
	if in.Project == "" {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("project_required", "a work package belongs to a project", "name the project", nil)
	}
	if in.Branch == "" {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("branch_required", "a work package is bound to a branch", "name the branch", nil)
	}
	if in.Name == "" {
		in.Name = in.Branch
	}
	if in.PR < 0 {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("invalid_pr", "pr must be a pull request number", "use a positive number or leave it out", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{in}, func(d *Store, p []any) (requestdomain.PackageDetail, error) {
			return d.CreatePackage(p[0].(requestdomain.PackageInput))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	defer tx.Rollback()
	pkg, err := createPackageTx(tx, in)
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	for _, m := range in.Members {
		if _, err := addMemberTx(tx, pkg, m, "", in.Person); err != nil {
			return requestdomain.PackageDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	return s.PackageByID(pkg.ID)
}

func createPackageTx(tx *sql.Tx, in requestdomain.PackageInput) (requestdomain.Package, error) {
	var existing int64
	err := tx.QueryRow(`SELECT id FROM work_packages WHERE project=? AND branch=? AND state='open'`, in.Project, in.Branch).Scan(&existing)
	if err == nil {
		return requestdomain.Package{}, requestdomain.NewRuleError("package_exists",
			fmt.Sprintf("branch %s already has the open work package PKG-%d", in.Branch, existing),
			"add the requests to that package instead", []string{fmt.Sprintf("PKG-%d", existing)})
	}
	if err != sql.ErrNoRows {
		return requestdomain.Package{}, err
	}
	var pr any
	if in.PR > 0 {
		pr = in.PR
		var other int64
		if err := tx.QueryRow(`SELECT id FROM work_packages WHERE project=? AND pr=?`, in.Project, in.PR).Scan(&other); err == nil {
			return requestdomain.Package{}, prTaken(in.PR, other)
		}
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO work_packages(project,branch,name,state,pr,created_by,created_at,updated_at) VALUES(?,?,?,'open',?,?,?,?)`,
		in.Project, in.Branch, in.Name, pr, in.Person, ts, ts)
	if err != nil {
		return requestdomain.Package{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return requestdomain.Package{}, err
	}
	p := requestdomain.Package{ID: id, Project: in.Project, Branch: in.Branch, Name: in.Name, State: requestdomain.PackageOpen,
		PR: in.PR, CreatedBy: in.Person, CreatedAt: ts, UpdatedAt: ts}
	return p, nil
}

func prTaken(pr, pkg int64) error {
	return requestdomain.NewRuleError("pr_taken", fmt.Sprintf("PR #%d already belongs to PKG-%d", pr, pkg),
		"a pull request carries one work package", []string{fmt.Sprintf("PKG-%d", pkg)})
}

// Ergebnisse von addMemberTx.
const (
	memberAdded       = "added"
	memberAlready     = "already_member"
	memberRoleChanged = "role_changed"
)

func requestNotInProject(id int64) error {
	return requestdomain.NewRuleError("request_not_found", fmt.Sprintf("no request REQ-%d in this project", id), "a package holds requests of its own project; check the request number", nil)
}

// addMemberTx stellt einen Request in ein Paket, idempotent: dieselbe Rolle
// noch einmal ist ein No-op ohne Eintrag im Verlauf, eine andere Rolle wechselt
// sie. Jede Änderung steht im Activity-Log des Requests, mit Grund, soweit
// einer genannt ist.
func addMemberTx(tx *sql.Tx, pkg requestdomain.Package, m requestdomain.PackageMember, reason, person string) (string, error) {
	role, err := requestdomain.ValidateRole(m.Role)
	if err != nil {
		return "", err
	}
	// Ein Request, den es nicht gibt, und einer in einem anderen Projekt
	// antworten gleich, bis auf das letzte Zeichen: die Abfrage unterscheidet
	// sie nicht einmal, damit die Antwort nichts über fremde Projekte verrät.
	var found int
	err = tx.QueryRow(`SELECT 1 FROM requests WHERE id=? AND project=?`, m.RequestID, pkg.Project).Scan(&found)
	if err == sql.ErrNoRows {
		return "", requestNotInProject(m.RequestID)
	}
	if err != nil {
		return "", err
	}
	var have string
	err = tx.QueryRow(`SELECT role FROM work_package_requests WHERE package_id=? AND request_id=?`, pkg.ID, m.RequestID).Scan(&have)
	ts := now()
	kind, result := "package.added", memberAdded
	switch {
	case err == nil && have == role:
		return memberAlready, nil
	case err == nil:
		if _, err := tx.Exec(`UPDATE work_package_requests SET role=? WHERE package_id=? AND request_id=?`, role, pkg.ID, m.RequestID); err != nil {
			return "", err
		}
		kind, result = "package.role", memberRoleChanged
	case err == sql.ErrNoRows:
		if _, err := tx.Exec(`INSERT INTO work_package_requests(package_id,request_id,role,added_by,added_at) VALUES(?,?,?,?,?)`,
			pkg.ID, m.RequestID, role, person, ts); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	data := fmt.Sprintf("PKG-%d branch:%s role:%s", pkg.ID, pkg.Branch, role)
	if strings.TrimSpace(reason) != "" {
		data += " — " + strings.TrimSpace(reason)
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,?,?,?,?)`, m.RequestID, kind, person, data, ts); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE work_packages SET updated_at=? WHERE id=?`, ts, pkg.ID); err != nil {
		return "", err
	}
	return result, nil
}

// AddPackageRequests stellt Requests in ein bestehendes Paket. Ein
// geschlossenes Paket nimmt nichts mehr auf.
func (s *Store) AddPackageRequests(id int64, members []requestdomain.PackageMember, reason, person string) (requestdomain.PackageDetail, error) {
	if len(members) == 0 {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("requests_required", "name at least one request", "pass request ids with a role", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{id, members, reason, person}, func(d *Store, p []any) (requestdomain.PackageDetail, error) {
			return d.AddPackageRequests(p[0].(int64), p[1].([]requestdomain.PackageMember), p[2].(string), p[3].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	defer tx.Rollback()
	var pkg requestdomain.Package
	err = scanPackage(tx.QueryRow(`SELECT `+packageCols+` FROM work_packages WHERE id=?`, id), &pkg)
	if err == sql.ErrNoRows {
		return requestdomain.PackageDetail{}, packageNotFound(id)
	}
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if pkg.State != requestdomain.PackageOpen {
		return requestdomain.PackageDetail{}, packageClosed(id)
	}
	for _, m := range members {
		if _, err := addMemberTx(tx, pkg, m, reason, person); err != nil {
			return requestdomain.PackageDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	return s.PackageByID(id)
}

func packageClosed(id int64) error {
	return requestdomain.NewRuleError("package_closed", fmt.Sprintf("PKG-%d is closed", id), "open a new package for the branch, or reopen this one with state=open", nil)
}

// RemovePackageRequest nimmt einen Request aus dem Paket. Der Grund ist Pflicht
// und steht im Activity-Log des Requests: ein Lastenheft, aus dem etwas ohne
// Spur verschwindet, ist keines.
func (s *Store) RemovePackageRequest(id, requestID int64, reason, person string) (requestdomain.PackageDetail, error) {
	if strings.TrimSpace(reason) == "" {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("removal_reason_required", "removing a request from a package has to say why", "pass the reason it no longer belongs", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{id, requestID, reason, person}, func(d *Store, p []any) (requestdomain.PackageDetail, error) {
			return d.RemovePackageRequest(p[0].(int64), p[1].(int64), p[2].(string), p[3].(string))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	defer tx.Rollback()
	var pkg requestdomain.Package
	err = scanPackage(tx.QueryRow(`SELECT `+packageCols+` FROM work_packages WHERE id=?`, id), &pkg)
	if err == sql.ErrNoRows {
		return requestdomain.PackageDetail{}, packageNotFound(id)
	}
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	var role string
	err = tx.QueryRow(`SELECT role FROM work_package_requests WHERE package_id=? AND request_id=?`, id, requestID).Scan(&role)
	if err == sql.ErrNoRows {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("not_a_member", fmt.Sprintf("REQ-%d is not in PKG-%d", requestID, id), "read the package to see its requests", nil)
	}
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if _, err := tx.Exec(`DELETE FROM work_package_requests WHERE package_id=? AND request_id=?`, id, requestID); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	ts := now()
	data := fmt.Sprintf("PKG-%d branch:%s role:%s — %s", id, pkg.Branch, role, strings.TrimSpace(reason))
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'package.removed',?,?,?)`, requestID, person, data, ts); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if _, err := tx.Exec(`UPDATE work_packages SET updated_at=? WHERE id=?`, ts, id); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	return s.PackageByID(id)
}

// UpdatePackage ändert Name, PR-Nummer oder Zustand. Ein zweites offenes Paket
// auf demselben Branch entsteht dabei nicht: das Wiederöffnen scheitert, wenn
// der Branch schon ein offenes hat.
func (s *Store) UpdatePackage(id int64, patch requestdomain.PackagePatch) (requestdomain.PackageDetail, error) {
	if patch.Name == nil && patch.PR == nil && patch.State == nil {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("update_empty", "nothing to update", "pass name, pr or state", nil)
	}
	if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("name_required", "a package needs a name", "pass a non-empty name", nil)
	}
	if patch.PR != nil && *patch.PR < 0 {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("invalid_pr", "pr must be a pull request number", "use a positive number, or 0 to unlink", nil)
	}
	if patch.State != nil && *patch.State != requestdomain.PackageOpen && *patch.State != requestdomain.PackageClosed {
		return requestdomain.PackageDetail{}, requestdomain.NewRuleError("invalid_package_state", "package state must be open or closed", "use open or closed", nil)
	}
	if s.writer != nil {
		return queueValue(s, []any{id, patch}, func(d *Store, p []any) (requestdomain.PackageDetail, error) {
			return d.UpdatePackage(p[0].(int64), p[1].(requestdomain.PackagePatch))
		})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	defer tx.Rollback()
	var pkg requestdomain.Package
	err = scanPackage(tx.QueryRow(`SELECT `+packageCols+` FROM work_packages WHERE id=?`, id), &pkg)
	if err == sql.ErrNoRows {
		return requestdomain.PackageDetail{}, packageNotFound(id)
	}
	if err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if patch.Name != nil {
		pkg.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.State != nil {
		if *patch.State == requestdomain.PackageOpen && pkg.State != requestdomain.PackageOpen {
			var other int64
			if err := tx.QueryRow(`SELECT id FROM work_packages WHERE project=? AND branch=? AND state='open' AND id<>?`, pkg.Project, pkg.Branch, id).Scan(&other); err == nil {
				return requestdomain.PackageDetail{}, requestdomain.NewRuleError("package_exists",
					fmt.Sprintf("branch %s already has the open work package PKG-%d", pkg.Branch, other), "close that one first", []string{fmt.Sprintf("PKG-%d", other)})
			}
		}
		pkg.State = *patch.State
	}
	var pr any
	if patch.PR != nil {
		pkg.PR = *patch.PR
	}
	if pkg.PR > 0 {
		pr = pkg.PR
		var other int64
		if err := tx.QueryRow(`SELECT id FROM work_packages WHERE project=? AND pr=? AND id<>?`, pkg.Project, pkg.PR, id).Scan(&other); err == nil {
			return requestdomain.PackageDetail{}, prTaken(pkg.PR, other)
		}
	}
	if _, err := tx.Exec(`UPDATE work_packages SET name=?,state=?,pr=?,updated_at=? WHERE id=?`, pkg.Name, pkg.State, pr, now(), id); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return requestdomain.PackageDetail{}, err
	}
	return s.PackageByID(id)
}
