package store

import "strings"

// GhostReview records that a path was looked at and deliberately left without a
// description. It is bound to the git blob: the claim "there is nothing here
// worth recording that is not already in the code" is about one version of a
// file, not about the path forever.
//
// Without this state the tree cannot tell "never looked at" from "looked at,
// nothing to say", so every later inventory run reads the same rejected files
// again — which would make the promise that a run is resumable a false one.
type GhostReview struct {
	Project string `json:"project"`
	Path    string `json:"path"`
	GitBlob string `json:"git_blob"`
	Person  string `json:"person,omitempty"`
	At      string `json:"at"`
}

func (s *Store) PutGhostReview(r GhostReview) error {
	at := r.At
	if at == "" {
		at = now()
	}
	_, err := s.db.Exec(`INSERT INTO ghost_reviews(project,path,git_blob,person,at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(project,path) DO UPDATE SET git_blob=excluded.git_blob,
			person=excluded.person, at=excluded.at`,
		r.Project, r.Path, r.GitBlob, r.Person, at)
	return err
}

// GhostReviewsUnder returns the reviews of one branch of the tree. The empty
// prefix is the repository root and yields the whole project, which is how the
// materializer fetches them in one go.
func (s *Store) GhostReviewsUnder(project, prefix string) ([]GhostReview, error) {
	query := `SELECT project,path,git_blob,person,at FROM ghost_reviews WHERE project=?`
	args := []any{project}
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		// A prefix is a directory, not a string. A plain LIKE 'internal/store%'
		// would drag in internal/storage, and a foreign branch would show up as
		// already reviewed.
		query += ` AND (path=? OR path LIKE ?)`
		args = append(args, prefix, prefix+"/%")
	}
	query += ` ORDER BY path`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GhostReview
	for rows.Next() {
		var r GhostReview
		if err := rows.Scan(&r.Project, &r.Path, &r.GitBlob, &r.Person, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
