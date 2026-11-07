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

// ImportBranchRelations übernimmt die alten Branch-Relationen in echte
// Arbeitspakete: je (Projekt, Branch) ein offenes Paket, jeder markierte
// Request als Pflicht-Mitglied, danach entfällt die Relation (mit Eintrag im
// Activity-Log des Requests).
//
// Idempotent: nach einem Lauf gibt es keine passenden Relationen mehr, ein
// zweiter Lauf findet nichts. Bricht ein Lauf mittendrin ab, ist nichts
// geschrieben (eine Transaktion); ein Request, der schon Mitglied ist, verliert
// nur seine Relation. Ein Request, dessen Projekt nicht zum Verweis passt,
// bleibt mit seiner Relation stehen und steht im Bericht als übersprungen.
//
// project schränkt auf ein Projekt ein, leer nimmt alle. Bei dryRun läuft alles
// in einer Transaktion, die zurückgerollt wird: der Bericht ist genau der des
// echten Laufs.
func (s *Store) ImportBranchRelations(project string, dryRun bool, person string) (requestdomain.ImportReport, error) {
	project = scope.NormalizeRemote(project)
	if s.writer != nil {
		return queueValue(s, []any{project, dryRun, person}, func(d *Store, p []any) (requestdomain.ImportReport, error) {
			return d.ImportBranchRelations(p[0].(string), p[1].(bool), p[2].(string))
		})
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
		WHERE x.kind='external' AND x.external_ref LIKE 'branch:%' ORDER BY x.id`)
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
		if _, err := tx.Exec(`DELETE FROM request_relations WHERE id=?`, rel.id); err != nil {
			return report, err
		}
		data := fmt.Sprintf("external %s — übernommen in PKG-%d", rel.ref, pkg.ID)
		if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'relation.removed',?,?,?)`, rel.request, person, data, now()); err != nil {
			return report, err
		}
		report.Add(item)
	}
	if dryRun {
		return report, nil
	}
	return report, tx.Commit()
}
