package store

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

type SessionDistilledItem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Quote    string `json:"quote"`
	ChunkSeq int    `json:"chunk_seq"`
	// SameAs names an entry this finding already belongs to. The model is given
	// the project's entries with their ids and answers "this is #42 again"
	// instead of inventing a second title for one defect. That is what makes
	// recurrence a real number: the quote becomes further evidence for #42
	// rather than a new entry nobody can tell apart from it.
	SameAs int64 `json:"same_as,omitempty"`
}

func (s *Store) SessionDistillationExists(sessionID int64, digest, promptVersion string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_distillations
		WHERE session_id=? AND digest=? AND prompt_version=?`, sessionID, digest, promptVersion).Scan(&exists)
	return exists != 0, err
}

// ApplySessionDistillation persists one model result atomically. A repeated
// transcript digest under the same prompt version is a no-op, and every quote
// is rechecked against its chunk.
func (s *Store) ApplySessionDistillation(sessionID int64, digest, promptVersion string, ax scope.Axes, items []SessionDistilledItem) (int, error) {
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
	// A newer prompt supersedes the unreviewed output of an older one. Only
	// quarantined items are retired: they were never trusted, so nothing is
	// lost, while an item somebody approved stays — a prompt change is no
	// reason to discard a human decision.
	if err := archiveEarlierQuarantinedItems(tx, sessionID); err != nil {
		return 0, err
	}
	ts := now()
	inserted := 0
	for _, item := range items {
		itemScope := scope.DefaultAxes(item.Type, ax)
		var chunkText string
		if err := tx.QueryRow(`SELECT text FROM session_chunks WHERE session_id=? AND seq=?`, sessionID, item.ChunkSeq).Scan(&chunkText); err != nil {
			return 0, err
		}
		if item.Quote == "" || !strings.Contains(chunkText, item.Quote) {
			return 0, sql.ErrNoRows
		}
		// A finding that already exists is corroboration, not noise. Dropping it
		// silently was what kept every entry at recurrence one, and recurrence is
		// what the trust model and the bootstrap ranking are both built on.
		if existing, err := existingEntryFor(tx, itemScope.Project, item); err != nil {
			return 0, err
		} else if existing != 0 {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote)
				VALUES(?,?,?,?)`, existing, sessionID, item.ChunkSeq, item.Quote); err != nil {
				return 0, err
			}
			if err := refreshObservedAt(tx, existing); err != nil {
				return 0, err
			}
			continue
		}
		// observed_at starts at the run time and is corrected to the session's
		// own start once the evidence row exists, a few lines down. No entry is
		// left with an empty one even if a future path forgets the evidence.
		res, err := tx.Exec(`INSERT INTO knowledge(type,title,body,project,branch,machine,confidence,status,origin,session_ref,observed_at,created_at,updated_at)
			VALUES(?,?,?,?,?,?,'quarantined','active','distilled',?,?,?,?)`, item.Type, item.Title, item.Body, itemScope.Project, itemScope.Branch, itemScope.Machine, "session:"+strconv.FormatInt(sessionID, 10)+"#"+strconv.Itoa(item.ChunkSeq), ts, ts, ts)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine) VALUES('knowledge',?,?,?,?,?,?)`, id, item.Title, item.Body, itemScope.Project, itemScope.Branch, itemScope.Machine); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,?)`, id, sessionID, item.ChunkSeq, item.Quote); err != nil {
			return 0, err
		}
		if err := refreshObservedAt(tx, id); err != nil {
			return 0, err
		}
		inserted++
	}
	if _, err := tx.Exec(`INSERT INTO session_distillations(session_id,digest,prompt_version,item_count,created_at) VALUES(?,?,?,?,?)`,
		sessionID, digest, promptVersion, inserted, ts); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// existingEntryFor finds the entry a finding belongs to, or 0 if it is new.
//
// Two ways in, because they catch different halves. An exact title is
// mechanical and certain but only recognises the same words. The model's own
// judgement, passed as same_as, is what recognises the same defect under a
// different name — "Redirect validation occurs after the request" and
// "Redirected fetches permit SSRF" were two entries for one bug.
//
// same_as is checked against the project rather than trusted outright: a model
// that names an id from another project, or one that no longer exists, gets a
// new entry instead of writing evidence somewhere nobody expects it.
func existingEntryFor(tx *sql.Tx, project string, item SessionDistilledItem) (int64, error) {
	if item.SameAs != 0 {
		var id int64
		err := tx.QueryRow(`SELECT id FROM knowledge WHERE id=? AND project=? AND status='active'`,
			item.SameAs, project).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM knowledge WHERE project=? AND lower(title)=lower(?) AND status='active'
		ORDER BY id LIMIT 1`, project, item.Title).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// archiveEarlierQuarantinedItems retires what an earlier run made of this
// session. It runs before the insert so a new item may reuse a title the old
// run occupied — otherwise the duplicate guard would silently drop the better
// version of the same finding.
//
// The condition is the items themselves, not a distillation row: releasing a
// session for reprocessing deletes its row, so a check against the row would
// find no earlier run in exactly the case reprocessing exists for.
//
// Authorship, not evidence, decides what may be retired. A session that merely
// corroborated somebody else's finding also has an evidence row against it, and
// keying on evidence would let one session's rerun archive another session's
// entry. session_ref records who wrote it.
//
// The session's evidence on entries it did not write is dropped instead, so a
// rerun re-states its corroboration rather than counting it twice.
func archiveEarlierQuarantinedItems(tx *sql.Tx, sessionID int64) error {
	authored := "session:" + strconv.FormatInt(sessionID, 10) + "#%"
	if _, err := tx.Exec(`UPDATE knowledge SET status='archived', updated_at=?
		WHERE status='active' AND confidence='quarantined' AND origin='distilled'
		  AND session_ref LIKE ?`, now(), authored); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM knowledge_evidence
		WHERE session_id=? AND knowledge_id IN (
			SELECT id FROM knowledge WHERE session_ref NOT LIKE ?)`, sessionID, authored)
	return err
}

// ReleaseDistillations puts the sessions processed under one prompt version
// back in the queue. It is deliberately a separate, scoped, countable act:
// bumping a version must not silently re-submit the whole archive, because
// every re-submission is paid for.
func (s *Store) ReleaseDistillations(promptVersion string, filter scope.Axes, dryRun bool) (int, error) {
	where, args := filter.FilterWhere()
	args = append([]any{promptVersion}, args...)
	query := `SELECT COUNT(*) FROM session_distillations d
		WHERE d.prompt_version = ? AND d.session_id IN (SELECT id FROM sessions WHERE ` + where + `)`
	var affected int
	if err := s.db.QueryRow(query, args...).Scan(&affected); err != nil {
		return 0, err
	}
	if dryRun || affected == 0 {
		return affected, nil
	}
	if _, err := s.db.Exec(`DELETE FROM session_distillations
		WHERE prompt_version = ? AND session_id IN (SELECT id FROM sessions WHERE `+where+`)`, args...); err != nil {
		return 0, err
	}
	return affected, nil
}
