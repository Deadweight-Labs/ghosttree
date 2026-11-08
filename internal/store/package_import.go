package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// branchRefPrefix leitet den Übergangsmechanismus ein, mit dem vor REQ-601 der
// Pflichtumfang eines Branches markiert wurde (Entscheidung #4298): eine
// Relation kind=external mit external_ref "branch:<projekt>@<branch>".
const branchRefPrefix = "branch:"

// parseBranchRef zerlegt "branch:<projekt>@<branch>". Das Projekt ist ein
// normalisierter Remote ohne "@", deshalb trennt das erste "@"; der Branch darf
// weitere enthalten.
func parseBranchRef(ref string) (project, branch string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(ref), branchRefPrefix)
	if !found {
		return "", "", false
	}
	project, branch, found = strings.Cut(rest, "@")
	project, branch = scope.NormalizeRemote(project), strings.TrimSpace(branch)
	if !found || project == "" || branch == "" {
		return "", "", false
	}
	return project, branch, true
}

// BranchRelationRequests nennt die Requests, an denen noch nicht übernommene
// branch-Relationen hängen (im Projekt, leer = alle), mit ihrem Projekt. Das
// ist die Lesehälfte der Übernahme: wer sie ausführt, prüft je Request, ob der
// Aufrufer ihn sehen und bearbeiten darf, bevor er die Ids zurückgibt.
func (s *Store) BranchRelationRequests(project string) ([]ObjectRef, error) {
	if s.reader != nil {
		return s.reader.BranchRelationRequests(project)
	}
	project = scope.NormalizeRemote(project)
	rows, err := s.db.Query(`SELECT DISTINCT x.request_id, x.external_ref FROM request_relations x
		WHERE x.kind='external' AND x.package_id IS NULL AND x.external_ref LIKE 'branch:%'`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	seen := map[int64]bool{}
	for rows.Next() {
		var id int64
		var ref string
		if err := rows.Scan(&id, &ref); err != nil {
			rows.Close()
			return nil, err
		}
		if p, _, ok := parseBranchRef(ref); (project == "" || (ok && p == project)) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ObjectRef
	for _, id := range ids {
		ref, err := s.RequestRef(RefRequest, id)
		if err != nil {
			continue
		}
		out = append(out, ref)
	}
	return out, nil
}

// ImportBranchRelations übernimmt die alten Branch-Relationen in echte
// Arbeitspakete, so weit der Aufrufer die Requests sehen und bearbeiten darf: je
// (Projekt, Branch) ein offenes Paket, jeder markierte Request als Pflicht-
// Mitglied. Die Relation selbst bleibt stehen und trägt danach die Nummer des
// Pakets (request_relations.package_id) und einen Eintrag im Activity-Log.
// Gelöscht wird sie hier nie: Wissen wird versioniert, nicht gelöscht (#1444),
// und ob die alten Markierungen verschwinden, entscheidet ein Mensch.
//
// allowed sind die Request-Ids, die der Aufrufer bearbeiten darf
// (ProjectAccess.ImportBranchRelations ermittelt sie); jede andere Relation wird
// nicht angefasst und kommt nicht im Bericht vor. nil heißt: keine.
//
// Idempotent: eine übernommene Relation zählt beim nächsten Lauf nicht mehr, ein
// zweiter Lauf findet nichts. Bricht ein Lauf mittendrin ab, ist nichts
// geschrieben (eine Transaktion). Ein Request, dessen Projekt nicht zum Verweis
// passt, bleibt unübernommen und steht als übersprungen im Bericht. Ein Request,
// der schon Mitglied ist, behält seine Mitgliedschaft; war er optional, wird er
// Pflicht, denn die Markierung war der Pflichtumfang.
//
// project schränkt auf ein Projekt ein, leer nimmt alle. Bei dryRun läuft alles
// in einer Transaktion, die zurückgerollt wird: der Bericht ist genau der des
// echten Laufs.
func (s *Store) ImportBranchRelations(project string, dryRun bool, person string, allowed []int64) (requestdomain.ImportReport, error) {
	project = scope.NormalizeRemote(project)
	if s.writer != nil {
		return queueValue(s, []any{project, dryRun, person, allowed}, func(d *Store, p []any) (requestdomain.ImportReport, error) {
			return d.ImportBranchRelations(p[0].(string), p[1].(bool), p[2].(string), p[3].([]int64))
		})
	}
	may := map[int64]bool{}
	for _, id := range allowed {
		may[id] = true
	}
	report := requestdomain.ImportReport{DryRun: dryRun, Items: []requestdomain.ImportItem{}}
	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	type relation struct {
		id, request int64
		ref, owner  string
	}
	rows, err := tx.Query(`SELECT x.id,x.request_id,x.external_ref,r.project FROM request_relations x JOIN requests r ON r.id=x.request_id
		WHERE x.kind='external' AND x.package_id IS NULL AND x.external_ref LIKE 'branch:%' ORDER BY x.id`)
	if err != nil {
		return report, err
	}
	var relations []relation
	for rows.Next() {
		var r relation
		if err := rows.Scan(&r.id, &r.request, &r.ref, &r.owner); err != nil {
			rows.Close()
			return report, err
		}
		relations = append(relations, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return report, err
	}
	packages := map[string]requestdomain.Package{}
	for _, rel := range relations {
		if !may[rel.request] {
			continue
		}
		refProject, branch, ok := parseBranchRef(rel.ref)
		if !ok {
			if project == "" {
				report.Add(requestdomain.ImportItem{RelationID: rel.id, RequestID: rel.request, Result: "skipped", Reason: "reference is not branch:<project>@<branch>"})
			}
			continue
		}
		if project != "" && refProject != project {
			continue
		}
		item := requestdomain.ImportItem{RelationID: rel.id, RequestID: rel.request, Project: refProject, Branch: branch}
		if rel.owner != refProject {
			item.Result, item.Reason = "skipped", "the request belongs to another project than the reference names"
			report.Add(item)
			continue
		}
		key := refProject + "@" + branch
		pkg, have := packages[key]
		if !have {
			err := tx.QueryRow(`SELECT `+packageCols+` FROM work_packages WHERE project=? AND branch=? AND state='open'`, refProject, branch).Scan(
				&pkg.ID, &pkg.Project, &pkg.Branch, &pkg.Name, &pkg.State, &pkg.PR, &pkg.CreatedBy, &pkg.CreatedAt, &pkg.UpdatedAt)
			switch {
			case err == nil:
			case errors.Is(err, sql.ErrNoRows):
				if pkg, err = createPackageTx(tx, requestdomain.PackageInput{Project: refProject, Branch: branch, Name: branch, Person: person}); err != nil {
					return report, err
				}
				report.PackagesCreated = append(report.PackagesCreated, pkg.ID)
			default:
				return report, err
			}
			packages[key] = pkg
		}
		item.PackageID = pkg.ID
		reason := "Übernahme der Relation " + rel.ref
		result, err := addMemberTx(tx, pkg, requestdomain.PackageMember{RequestID: rel.request, Role: requestdomain.RolePflicht}, reason, person)
		var rule *requestdomain.RuleError
		if errors.As(err, &rule) {
			item.Result, item.Reason = "skipped", rule.Message
			report.Add(item)
			continue
		}
		if err != nil {
			return report, err
		}
		item.Result = "added"
		if result != memberAdded {
			item.Result = "already_member"
		}
		if result == memberRoleChanged {
			// Die Markierung war der Pflichtumfang: sie hebt auch ein optionales
			// Mitglied an. Der Wechsel steht im Activity-Log des Requests.
			item.Reason = "role raised to pflicht"
		}
		if _, err := tx.Exec(`UPDATE request_relations SET package_id=? WHERE id=?`, pkg.ID, rel.id); err != nil {
			return report, err
		}
		data := fmt.Sprintf("external %s — übernommen in PKG-%d, Relation bleibt stehen", rel.ref, pkg.ID)
		if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'relation.taken_over',?,?,?)`, rel.request, person, data, now()); err != nil {
			return report, err
		}
		report.Add(item)
	}
	if dryRun {
		return report, nil
	}
	return report, tx.Commit()
}

// ImportBranchRelations ist die Übernahme für einen Betrachter: Pakete und
// Mitgliedschaften entstehen nur für Requests, die er lesen und bearbeiten darf.
// Alle anderen bleiben unberührt und unerwähnt. Die Prüfung läuft vor der
// Transaktion, denn sie fragt die Datenbank selbst, und innerhalb einer
// Transaktion auf einer einzigen Verbindung hinge das.
func (a *ProjectAccess) ImportBranchRelations(project string, dryRun bool, person string) (requestdomain.ImportReport, error) {
	refs, err := a.st.BranchRelationRequests(project)
	if err != nil {
		return requestdomain.ImportReport{}, err
	}
	var allowed []int64
	for _, ref := range refs {
		obj, visible := a.RequestObject(ref)
		if visible && a.Check(ref.Project, ResRequest, ActWork, obj) == nil {
			allowed = append(allowed, ref.ID)
		}
	}
	return a.st.ImportBranchRelations(project, dryRun, person, allowed)
}
