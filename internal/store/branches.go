package store

import (
	"sort"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// ProjectBranches nennt die Branches, die ein Projekt in seinen Daten kennt:
// die Achse des Wissens, die Branches der Arbeitspakete und die der Git-Anker
// an Beschreibungen und Wissen. Sortiert, ohne leere Namen.
//
// Sessions tragen absichtlich nichts bei: sie sind je Konto privat, und der
// Branch einer fremden privaten Session soll in keiner Liste auftauchen.
//
// Der Server hat die Git-Repositorien nicht; die Liste ist deshalb das, was
// Agenten und Pakete ihm gesagt haben, nicht die Branch-Liste von GitHub.
// Wer die Liste anzeigt, darf sie nicht für vollständig ausgeben. Die Auswahl
// der Betrachter (wer welche Namen sehen darf) trifft der Aufrufer; hier wird
// nichts gefiltert.
func (s *Store) ProjectBranches(project string) ([]string, error) {
	if s.reader != nil {
		return s.reader.ProjectBranches(project)
	}
	project = scope.NormalizeRemote(project)
	rows, err := s.db.Query(`
		SELECT branch FROM knowledge WHERE project=? AND branch<>''
		UNION SELECT branch FROM work_packages WHERE project=?
		UNION SELECT a.branch FROM git_anchors a JOIN ghost_files g ON g.anchor_id=a.id
			WHERE g.project=? AND a.branch<>''
		UNION SELECT a.branch FROM git_anchors a JOIN knowledge k ON k.anchor_id=a.id
			WHERE k.project=? AND a.branch<>''`,
		project, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Strings(out)
	return out, rows.Err()
}
