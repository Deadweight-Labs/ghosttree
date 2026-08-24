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
		var duplicate int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE project=? AND lower(title)=lower(?) AND status='active'`, itemScope.Project, item.Title).Scan(&duplicate); err != nil {
			return 0, err
		}
		if duplicate != 0 {
			continue
		}
		res, err := tx.Exec(`INSERT INTO knowledge(type,title,body,project,branch,machine,confidence,status,origin,session_ref,created_at,updated_at)
			VALUES(?,?,?,?,?,?,'quarantined','active','distilled',?,?,?)`, item.Type, item.Title, item.Body, itemScope.Project, itemScope.Branch, itemScope.Machine, "session:"+strconv.FormatInt(sessionID, 10)+"#"+strconv.Itoa(item.ChunkSeq), ts, ts)
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

// archiveEarlierQuarantinedItems retires what an earlier run made of this
// session. It runs before the insert so a new item may reuse a title the old
// run occupied — otherwise the duplicate guard would silently drop the better
// version of the same finding.
//
// The condition is the items themselves, not a distillation row: releasing a
// session for reprocessing deletes its row, so a check against the row would
// find no earlier run in exactly the case reprocessing exists for. Any active
// quarantined item already evidenced by this session is by definition from an
// earlier run, because the current one inserts inside this transaction.
func archiveEarlierQuarantinedItems(tx *sql.Tx, sessionID int64) error {
	_, err := tx.Exec(`UPDATE knowledge SET status='archived', updated_at=?
		WHERE status='active' AND confidence='quarantined' AND origin='distilled'
		  AND id IN (SELECT knowledge_id FROM knowledge_evidence WHERE session_id=?)`, now(), sessionID)
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
