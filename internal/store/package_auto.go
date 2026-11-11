package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// Das Paket entsteht von selbst (REQ-601 AC-12): öffnet sich ein PR, dessen Branch
// kein Paket hat, legt der Server eines an und stellt hinein, was sich eindeutig
// herleiten lässt. Niemand muss package_update aufrufen.
//
// Herleiten heißt hier nur zwei Quellen, beide ausdrücklich:
//
//   - ein Request, an dem eine Session des Branches mit start_work als Hauptauftrag
//     arbeitet (oder gearbeitet hat): die Session hat ihn gewählt;
//   - ein Request, den der Titel des PRs mit REQ-n nennt: der Autor hat ihn genannt.
//     Das gilt nur, wenn der Autor ein bewiesenes GitHub-Konto hat, das einer
//     ghosttree-Person mit Schreibrecht am Projekt gehört: der Titel eines PRs kann
//     jeder schreiben, der das Repo auf GitHub sieht, und ein REQ-n darin darf
//     keinen Request in ein Paket ziehen, den der Autor in ghosttree nicht ändern darf.
//
// Was in einem Commit-Text oder im Text des PRs steht, nimmt es nicht auf: ein
// "siehe REQ-12" ist ein Verweis und kein Auftrag, und der Text des PRs kommt nicht
// mit dem Ereignis. Ist der Branchname schon von einem anderen PR benutzt worden,
// gilt auch die Herleitung über Sessions nicht (dieselbe Bezeichnung, eine andere
// Arbeit); das Paket bleibt dann leer und man stellt die Requests von Hand hinein.
const (
	// autoPackagePerson steht als Urheber im Paket und im Verlauf der Requests.
	autoPackagePerson = "github"
	// maxAutoMembers: mehr als das ist keine eindeutige Herleitung mehr.
	maxAutoMembers = 20
)

var titleRequestRef = regexp.MustCompile(`\bREQ-(\d{1,9})\b`)

// autoPackageTx legt das Paket für einen offenen PR an. Es tut nichts, wenn der PR
// oder der Branch schon ein Paket hat (der Aufrufer hat ein offenes ohne PR vorher
// gebunden). created sagt, ob eines entstand, members, wie viele Requests darin sind.
func autoPackageTx(tx *sql.Tx, project string, number int64, head, title, ts string) (created bool, members int, err error) {
	var one int
	switch err := tx.QueryRow(`SELECT 1 FROM work_packages WHERE project=? AND pr=?`, project, number).Scan(&one); {
	case err == nil:
		return false, 0, nil
	case err != sql.ErrNoRows:
		return false, 0, err
	}
	if _, err := closeFinishedPackagesTx(tx, project, head, ts); err != nil {
		return false, 0, err
	}
	switch err := tx.QueryRow(`SELECT 1 FROM work_packages WHERE project=? AND branch=? AND state='open'`, project, head).Scan(&one); {
	case err == nil:
		return false, 0, nil
	case err != sql.ErrNoRows:
		return false, 0, err
	}
	name := title
	if name == "" {
		name = fmt.Sprintf("PR #%d", number)
	}
	if err := checkPackageLabel(name); err != nil {
		name = fmt.Sprintf("PR #%d", number)
	}
	if err := checkPackageLabel(head); err != nil {
		return false, 0, nil
	}
	pkg, err := createPackageTx(tx, requestdomain.PackageInput{Project: project, Branch: head, Name: name, PR: number, Person: autoPackagePerson})
	if err != nil {
		return false, 0, err
	}

	type derived struct {
		id     int64
		reason string
	}
	var found []derived
	seen := map[int64]bool{}
	add := func(id int64, reason string) {
		if !seen[id] && len(found) < maxAutoMembers+1 {
			seen[id] = true
			found = append(found, derived{id, reason})
		}
	}
	// Der Branchname war nie ein anderer PR: sonst wäre der Weg über Sessions nicht
	// eindeutig.
	var reused int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM git_pull_requests WHERE project=? AND head_ref=? AND number<>?`, project, head, number).Scan(&reused); err != nil {
		return false, 0, err
	}
	if reused == 0 {
		rows, err := tx.Query(`SELECT DISTINCT w.request_id FROM request_work w
			JOIN sessions s ON s.id=w.session_id JOIN requests r ON r.id=w.request_id
			WHERE w.role='primary' AND w.state IN ('active','paused','completed')
			AND s.project=? AND s.branch=? AND r.project=? AND r.state<>'dropped' ORDER BY w.request_id LIMIT ?`, project, head, project, maxAutoMembers+1)
		if err != nil {
			return false, 0, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return false, 0, err
			}
			add(id, fmt.Sprintf("added when PR #%d opened: a session on this branch works on it with start_work", number))
		}
		if err := rows.Close(); err != nil {
			return false, 0, err
		}
	}
	titleAllowed, err := authorMayWriteTx(tx, project, number)
	if err != nil {
		return false, 0, err
	}
	for _, m := range titleRequestRef.FindAllStringSubmatch(title, maxAutoMembers+1) {
		if !titleAllowed {
			break
		}
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		var ok int
		if err := tx.QueryRow(`SELECT 1 FROM requests WHERE id=? AND project=? AND state<>'dropped'`, id, project).Scan(&ok); err == sql.ErrNoRows {
			continue
		} else if err != nil {
			return false, 0, err
		}
		add(id, fmt.Sprintf("added when PR #%d opened: the title of the PR names it", number))
	}
	// Mehr Mitglieder, als eine Herleitung vernünftig trägt, sind ein Zeichen für
	// einen Branch, auf dem allerhand lief: dann bleibt das Paket leer.
	if len(found) > maxAutoMembers {
		return true, 0, nil
	}
	for _, d := range found {
		if _, err := addMemberTx(tx, pkg, requestdomain.PackageMember{RequestID: d.id, Role: requestdomain.RolePflicht}, d.reason, autoPackagePerson); err != nil {
			return false, 0, err
		}
		members++
	}
	return true, members, nil
}

// authorMayWriteTx sagt, ob der Autor des PRs (git_pull_requests.author, ein
// GitHub-Login) ein bewiesenes GitHub-Konto einer ghosttree-Person ist, die im
// Projekt mindestens member ist. Dieselbe Abbildung wie bei der Zuordnung von
// Commits (account_github mit github_id>0, sessiongit_match.go), dieselbe Rolle wie
// bei jedem Schreiben (projectRoleTx). Ein unbekannter Autor, ein nur eingetragener
// (nicht bewiesener) Login und ein Gast ergeben false.
func authorMayWriteTx(tx *sql.Tx, project string, number int64) (bool, error) {
	var author string
	switch err := tx.QueryRow(`SELECT author FROM git_pull_requests WHERE project=? AND number=?`, project, number).Scan(&author); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	if author == "" {
		return false, nil
	}
	var account int64
	switch err := tx.QueryRow(`SELECT account_id FROM account_github WHERE github_id>0 AND lower(login)=lower(?)`, author).Scan(&account); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	return RoleRank(projectRoleTx(tx, project, account).Role) >= RoleRank(RoleMember), nil
}

// closeFinishedPackagesTx schließt die offenen Pakete des Branches, deren PR
// gemergt oder geschlossen ist, und gibt zurück, wie viele es waren. Sie blockieren
// sonst den Branchnamen: der nächste PR mit demselben Namen bekäme kein eigenes
// Paket, und package create verwiese auf das alte. Ein Paket ohne PR oder mit
// offenem PR bleibt, wie es ist.
func closeFinishedPackagesTx(tx *sql.Tx, project, branch, ts string) (int64, error) {
	r, err := tx.Exec(`UPDATE work_packages SET state='closed', updated_at=?
		WHERE project=? AND branch=? AND state='open' AND pr IS NOT NULL
		AND EXISTS(SELECT 1 FROM git_pull_requests g WHERE g.project=work_packages.project AND g.number=work_packages.pr AND g.state IN ('merged','closed'))`,
		ts, project, branch)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
