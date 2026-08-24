package store

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// distilledRequestTypes mirrors the schema's CHECK constraint.
var distilledRequestTypes = map[string]bool{
	"feature": true, "change": true, "bug": true, "investigation": true,
}

// DistilledRequest is a wish read out of what someone actually typed.
type DistilledRequest struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Quote    string `json:"quote"`
	ChunkSeq int    `json:"chunk_seq"`
	// SameAs marks this as the same wish being voiced again, by REQ number.
	// Saying a thing twice is the strongest evidence there is that it was meant.
	SameAs int64 `json:"same_as,omitempty"`
}

// RequestTitlesForPrompt lists the ledger entries a distillation prompt should
// treat as already known, each with its number so the model can point at one.
// Dropped requests are included: somebody decided against them, and the model
// re-proposing what was rejected is worse than it staying quiet.
func (s *Store) RequestTitlesForPrompt(project string) ([]string, error) {
	rows, err := s.db.Query(`SELECT '#' || id || ' [' || state || '] ' || title
		FROM requests WHERE project = ? ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// ApplyRequestDistillation writes wishes into the ledger.
//
// Distilled requests arrive open and without acceptance criteria on purpose. A
// criterion is a commitment about what "done" means, and a model reading a
// side remark is in no position to make one — inventing them would fill the
// ledger with conditions nobody agreed to. What the model can do is record that
// something was asked for, and quote where.
func (s *Store) ApplyRequestDistillation(sessionID int64, digest, promptVersion string, ax scope.Axes, items []DistilledRequest) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM session_distillations
		WHERE session_id=? AND digest=? AND prompt_version=?`, sessionID, digest, promptVersion).Scan(&exists); err != nil {
		return 0, err
	}
	if exists != 0 {
		return 0, nil
	}
	ts := now()
	ref := "session:" + strconv.FormatInt(sessionID, 10)
	created := 0
	for _, item := range items {
		var chunkText string
		if err := tx.QueryRow(`SELECT text FROM session_chunks WHERE session_id=? AND seq=?`,
			sessionID, item.ChunkSeq).Scan(&chunkText); err != nil {
			return 0, err
		}
		// The same grounding rule as for knowledge, and it matters more here: a
		// wish nobody voiced is a task nobody asked for, and it would be worked on.
		if item.Quote == "" || !strings.Contains(chunkText, item.Quote) {
			return 0, sql.ErrNoRows
		}
		id, err := existingRequestFor(tx, ax.Project, item)
		if err != nil {
			return 0, err
		}
		if id == 0 {
			// A wish that does not name a recognised kind is still a wish. The
			// schema would reject it and cost the whole session; "feature" is
			// the honest default for "somebody asked for this".
			kind := item.Type
			if !distilledRequestTypes[kind] {
				kind = "feature"
			}
			res, err := tx.Exec(`INSERT INTO requests(type,title,description,state,project,branch,machine,origin,person,session_ref,created_at,updated_at)
				VALUES(?,?,?,'open',?,'','','distilled','',?,?,?)`,
				kind, item.Title, item.Body, ax.Project, ref, ts, ts)
			if err != nil {
				return 0, err
			}
			id, _ = res.LastInsertId()
			if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine)
				VALUES('request',?,?,?,?,'','')`, id, item.Title, item.Body, ax.Project); err != nil {
				return 0, err
			}
			created++
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO request_sightings(request_id,session_id,chunk_seq,quote)
			VALUES(?,?,?,?)`, id, sessionID, item.ChunkSeq, item.Quote); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO session_distillations(session_id,digest,prompt_version,item_count,created_at)
		VALUES(?,?,?,?,?)`, sessionID, digest, promptVersion, created, ts); err != nil {
		return 0, err
	}
	return created, tx.Commit()
}

func existingRequestFor(tx *sql.Tx, project string, item DistilledRequest) (int64, error) {
	if item.SameAs != 0 {
		var id int64
		err := tx.QueryRow(`SELECT id FROM requests WHERE id=? AND project=?`, item.SameAs, project).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM requests WHERE project=? AND lower(title)=lower(?) ORDER BY id LIMIT 1`,
		project, item.Title).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// RequestSightings counts how many separate sessions voiced a request. A wish
// mentioned once may have been thinking aloud; one mentioned in four sessions
// is a requirement that keeps not getting built.
func (s *Store) RequestSightings(requestID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM request_sightings WHERE request_id=?`,
		requestID).Scan(&n)
	return n, err
}

// RequestQuotes returns what was actually said, so a person judging a distilled
// entry reads the words rather than the model's summary of them.
func (s *Store) RequestQuotes(requestID int64) ([]Evidence, error) {
	rows, err := s.db.Query(`SELECT session_id, chunk_seq, quote FROM request_sightings
		WHERE request_id=? ORDER BY session_id, chunk_seq`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.SessionID, &e.ChunkSeq, &e.Quote); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
