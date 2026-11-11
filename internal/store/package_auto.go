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
	for _, m := range titleRequestRef.FindAllStringSubmatch(title, maxAutoMembers+1) {
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
