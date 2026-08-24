package store

import (
	"database/sql"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// CanonicalizeReport records what a backfill touched, so a run can be
// verified rather than trusted.
type CanonicalizeReport struct {
	KnowledgeRescoped int      `json:"knowledge_rescoped"`
	RequestsRescoped  int      `json:"requests_rescoped"`
	SessionsRescoped  int      `json:"sessions_rescoped"`
	DocumentsRescoped int      `json:"documents_rescoped"`
	RequestsWidened   int      `json:"requests_widened"`
	RequestsMerged    int      `json:"requests_merged"`
	MergeSkipped      []int64  `json:"merge_skipped"`
	Projects          []string `json:"projects"`
}

// CanonicalizeScopes rewrites every stored project name to its canonical form
// and merges the duplicates that non-canonical names produced.
//
// Canonicalisation was introduced at the server boundary, which fixed new
// writes but left existing rows on their old spelling, so one repository was
// split across several project values and every project-scoped read saw only
// a fraction of it. Aliases cover what normalisation cannot know on its own: a
// repository that changed owner keeps no trace of where it came from.
func (s *Store) CanonicalizeScopes(aliases map[string]string) (CanonicalizeReport, error) {
	return s.canonicalizeScopes(aliases, false)
}

// PreviewCanonicalizeScopes reports what CanonicalizeScopes would change and
// then rolls back, so the run can be inspected before it touches production.
func (s *Store) PreviewCanonicalizeScopes(aliases map[string]string) (CanonicalizeReport, error) {
	return s.canonicalizeScopes(aliases, true)
}

// Backup writes a verified copy of the database to path.
func (s *Store) Backup(path string) error {
	return createVerifiedBackup(s.db, path)
}

func (s *Store) canonicalizeScopes(aliases map[string]string, preview bool) (CanonicalizeReport, error) {
	canonical := func(project string) string {
		if project == "" {
			return ""
		}
		normalized := scope.NormalizeRemote(project)
		if target, ok := aliases[normalized]; ok {
			return scope.NormalizeRemote(target)
		}
		return normalized
	}

	tx, err := s.db.Begin()
	if err != nil {
		return CanonicalizeReport{}, err
	}
	defer tx.Rollback()

	var report CanonicalizeReport
	seen := map[string]bool{}
	for _, target := range []struct {
		table string
		count *int
	}{
		{"knowledge", &report.KnowledgeRescoped},
		{"requests", &report.RequestsRescoped},
		{"sessions", &report.SessionsRescoped},
		{"search_documents", &report.DocumentsRescoped},
	} {
		rows, err := tx.Query(`SELECT DISTINCT project FROM ` + target.table + ` WHERE project != ''`)
		if err != nil {
			return CanonicalizeReport{}, err
		}
		var stored []string
		for rows.Next() {
			var project string
			if err := rows.Scan(&project); err != nil {
				rows.Close()
				return CanonicalizeReport{}, err
			}
			stored = append(stored, project)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return CanonicalizeReport{}, err
		}
		for _, project := range stored {
			want := canonical(project)
			if !seen[want] {
				seen[want] = true
				report.Projects = append(report.Projects, want)
			}
			if want == project {
				continue
			}
			res, err := tx.Exec(`UPDATE `+target.table+` SET project=? WHERE project=?`, want, project)
			if err != nil {
				return CanonicalizeReport{}, err
			}
			affected, _ := res.RowsAffected()
			*target.count += int(affected)
		}
	}

	// Requests filed before the write path was fixed inherited the branch and
	// machine of the session that created them. A backlog entry belongs to the
	// repository, and an exact-axis read elsewhere would never see it.
	widened, err := tx.Exec(`UPDATE requests SET branch='', machine='' WHERE branch!='' OR machine!=''`)
	if err != nil {
		return CanonicalizeReport{}, err
	}
	widenedRows, _ := widened.RowsAffected()
	report.RequestsWidened = int(widenedRows)
	if _, err := tx.Exec(`UPDATE search_documents SET branch='', machine='' WHERE kind='request' AND (branch!='' OR machine!='')`); err != nil {
		return CanonicalizeReport{}, err
	}

	merged, skipped, err := mergeDuplicateRequests(tx)
	if err != nil {
		return CanonicalizeReport{}, err
	}
	report.RequestsMerged, report.MergeSkipped = merged, skipped

	if preview {
		// The deferred Rollback discards everything; the counts stay.
		return report, nil
	}
	if err := tx.Commit(); err != nil {
		return CanonicalizeReport{}, err
	}
	return report, nil
}

// mergeDuplicateRequests removes copies of a request that differ in nothing
// but their id. The oldest survives because any work or evidence recorded
// against the group most likely points at it. A duplicate that carries work or
// evidence of its own is left alone and reported: merging it would discard a
// record no one can reconstruct.
func mergeDuplicateRequests(tx *sql.Tx) (int, []int64, error) {
	rows, err := tx.Query(`SELECT id, project, title, description FROM requests ORDER BY id`)
	if err != nil {
		return 0, nil, err
	}
	type request struct {
		id  int64
		key string
	}
	var all []request
	for rows.Next() {
		var r request
		var project, title, description string
		if err := rows.Scan(&r.id, &project, &title, &description); err != nil {
			rows.Close()
			return 0, nil, err
		}
		r.key = project + "\x00" + title + "\x00" + description
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	keep := map[string]int64{}
	merged := 0
	var skipped []int64
	for _, r := range all {
		if _, ok := keep[r.key]; !ok {
			keep[r.key] = r.id
			continue
		}
		var attached int
		if err := tx.QueryRow(`SELECT
			(SELECT COUNT(*) FROM request_work WHERE request_id=?) +
			(SELECT COUNT(*) FROM request_evidence WHERE request_id=?) +
			(SELECT COUNT(*) FROM request_relations WHERE request_id=? OR other_request_id=?)`,
			r.id, r.id, r.id, r.id).Scan(&attached); err != nil {
			return 0, nil, err
		}
		if attached != 0 {
			skipped = append(skipped, r.id)
			continue
		}
		for _, stmt := range []string{
			`DELETE FROM request_activity WHERE request_id=?`,
			`DELETE FROM request_criteria WHERE request_id=?`,
			`DELETE FROM search_documents WHERE kind='request' AND domain_id=?`,
			`DELETE FROM requests WHERE id=?`,
		} {
			if _, err := tx.Exec(stmt, r.id); err != nil {
				return 0, nil, err
			}
		}
		merged++
	}
	return merged, skipped, nil
}
