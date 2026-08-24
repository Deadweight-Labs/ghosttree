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

func (s *Store) SessionDistillationExists(sessionID int64, digest string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_distillations WHERE session_id=? AND digest=?`, sessionID, digest).Scan(&exists)
	return exists != 0, err
}

// ApplySessionDistillation persists one model result atomically. A repeated
// transcript digest is a no-op, and every quote is rechecked against its chunk.
func (s *Store) ApplySessionDistillation(sessionID int64, digest string, ax scope.Axes, items []SessionDistilledItem) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM session_distillations WHERE session_id=? AND digest=?`, sessionID, digest).Scan(&exists); err != nil {
		return 0, err
	}
	if exists != 0 {
		return 0, nil
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
		if _, err := tx.Exec(`INSERT INTO knowledge_evidence(knowledge_id,session_id,chunk_seq,quote) VALUES(?,?,?,?)`, id, sessionID, item.ChunkSeq, item.Quote); err != nil {
			return 0, err
		}
		inserted++
	}
	if _, err := tx.Exec(`INSERT INTO session_distillations(session_id,digest,item_count,created_at) VALUES(?,?,?,?)`, sessionID, digest, inserted, ts); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}
