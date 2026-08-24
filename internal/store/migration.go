package store

import (
	"database/sql"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

func (s *Store) BeginMigration(project string, artifacts map[string]string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO migration_runs(project,state,created_at) VALUES(?,'pending',?)`, project, now())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for path, digest := range artifacts {
		if _, err := tx.Exec(`INSERT INTO migration_artifacts(run_id,path,digest) VALUES(?,?,?)`, id, path, digest); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) CompleteMigration(id int64) error {
	var missing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM migration_artifacts a JOIN migration_runs r ON r.id=a.run_id
		WHERE a.run_id=? AND NOT EXISTS (
		 SELECT 1 FROM migration_evidence e
		 LEFT JOIN knowledge k ON k.id=e.knowledge_id
		 LEFT JOIN requests q ON q.id=e.request_id
		 WHERE COALESCE(k.project,q.project)=r.project AND e.source=a.path AND e.digest=a.digest
		)`, id).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("migration run %d has %d artifacts without persisted evidence", id, missing)
	}
	res, err := s.db.Exec(`UPDATE migration_runs SET state='complete', completed_at=? WHERE id=? AND state='pending'`, now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("pending migration run %d not found", id)
	}
	return nil
}

func (s *Store) CompletedMigrationArtifacts(project string) (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT a.path,a.digest FROM migration_artifacts a JOIN migration_runs r ON r.id=a.run_id WHERE r.project=? AND r.state='complete'`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var path, digest string
		if err := rows.Scan(&path, &digest); err != nil {
			return nil, err
		}
		out[path] = append(out[path], digest)
	}
	return out, rows.Err()
}

type MigratedEntry struct {
	Knowledge                               Knowledge
	RunID                                   int64
	Digest, Quote, ItemKey                  string
	RequestState, EvidenceKind, EvidenceRef string
}

type MigratedResult struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

type MigrationEvidence struct {
	RunID   int64  `json:"run_id"`
	Source  string `json:"source"`
	Digest  string `json:"digest"`
	ItemKey string `json:"item_key"`
	Quote   string `json:"quote"`
}

func (s *Store) MigrationEvidenceForKnowledge(id int64) (MigrationEvidence, error) {
	var proof MigrationEvidence
	err := s.db.QueryRow(`SELECT run_id,source,digest,item_key,quote FROM migration_evidence WHERE knowledge_id=?`, id).
		Scan(&proof.RunID, &proof.Source, &proof.Digest, &proof.ItemKey, &proof.Quote)
	return proof, err
}

// InsertMigrated atomically stores an entry, its source proof and its ledger
// state. The stable item key makes retries after a partial run idempotent.
func (s *Store) InsertMigrated(in MigratedEntry) (MigratedResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return MigratedResult{}, err
	}
	defer tx.Rollback()
	var existingKind string
	var existingID int64
	err = tx.QueryRow(`SELECT CASE WHEN request_id IS NOT NULL THEN 'request' ELSE 'knowledge' END,
		COALESCE(request_id,knowledge_id) FROM migration_evidence WHERE item_key=?`, in.ItemKey).Scan(&existingKind, &existingID)
	if err == nil {
		return MigratedResult{Kind: existingKind, ID: existingID}, nil
	}
	if err != sql.ErrNoRows {
		return MigratedResult{}, err
	}
	k := in.Knowledge
	if k.Type == "request" {
		return insertMigratedRequest(tx, in, k)
	}
	if err := activation.ValidateRule(k.Activation); err != nil {
		return MigratedResult{}, err
	}
	if k.Type != "instruction" && (len(k.Activation.Paths) > 0 || len(k.Activation.Tasks) > 0) {
		return MigratedResult{}, fmt.Errorf("activation requires instruction, got %s", k.Type)
	}
	if k.Origin == "" {
		k.Origin = "distilled"
	}
	if k.Confidence == "" {
		if k.Origin == "distilled" {
			k.Confidence = "quarantined"
		} else {
			k.Confidence = "trusted"
		}
	}
	if k.Status == "" {
		k.Status = "active"
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO knowledge(type,title,body,project,branch,machine,confidence,status,origin,superseded_by,person,harness,session_ref,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, k.Type, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine, k.Confidence, k.Status, k.Origin, k.SupersededBy, k.Person, k.Harness, k.SessionRef, ts, ts)
	if err != nil {
		return MigratedResult{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine) VALUES('knowledge',?,?,?,?,?,?)`, id, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine); err != nil {
		return MigratedResult{}, err
	}
	for _, pattern := range k.Activation.Paths {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_path(knowledge_id,pattern) VALUES(?,?)`, id, pattern); err != nil {
			return MigratedResult{}, err
		}
	}
	for _, task := range k.Activation.Tasks {
		if _, err := tx.Exec(`INSERT INTO instruction_activation_task(knowledge_id,task) VALUES(?,?)`, id, task); err != nil {
			return MigratedResult{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO migration_evidence(knowledge_id,run_id,source,digest,item_key,quote) VALUES(?,?,?,?,?,?)`, id, in.RunID, k.SessionRef, in.Digest, in.ItemKey, in.Quote); err != nil {
		return MigratedResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MigratedResult{}, err
	}
	return MigratedResult{Kind: "knowledge", ID: id}, nil
}

func insertMigratedRequest(tx *sql.Tx, in MigratedEntry, k Knowledge) (MigratedResult, error) {
	state := in.RequestState
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "done" && state != "dropped" {
		return MigratedResult{}, fmt.Errorf("invalid request state %q", state)
	}
	if state == "done" && in.EvidenceRef == "" {
		return MigratedResult{}, fmt.Errorf("state done requires evidence_ref")
	}
	ts := now()
	res, err := tx.Exec(`INSERT INTO requests(type,title,description,state,project,branch,machine,origin,person,session_ref,idempotency_key,created_at,updated_at)
		VALUES('feature',?,?,?,?,?,?,'distilled',?,?,?, ?,?)`, k.Title, k.Body, state, k.Scope.Project, k.Scope.Branch, k.Scope.Machine, k.Person, k.SessionRef, in.ItemKey, ts, ts)
	if err != nil {
		return MigratedResult{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MigratedResult{}, err
	}
	if _, err := tx.Exec(`INSERT INTO migration_evidence(request_id,run_id,source,digest,item_key,quote) VALUES(?,?,?,?,?,?)`, id, in.RunID, k.SessionRef, in.Digest, in.ItemKey, in.Quote); err != nil {
		return MigratedResult{}, err
	}
	if state == "done" {
		kind := in.EvidenceKind
		if kind == "" {
			kind = "file"
		}
		if _, err := tx.Exec(`INSERT INTO request_evidence(request_id,kind,ref,person,created_at) VALUES(?,?,?,?,?)`, id, kind, in.EvidenceRef, k.Person, ts); err != nil {
			return MigratedResult{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO request_activity(request_id,kind,person,data,created_at) VALUES(?,'request.migrated',?,?,?)`, id, k.Person, k.SessionRef, ts); err != nil {
		return MigratedResult{}, err
	}
	if _, err := tx.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine) VALUES('request',?,?,?,?,?,?)`, id, k.Title, k.Body, k.Scope.Project, k.Scope.Branch, k.Scope.Machine); err != nil {
		return MigratedResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MigratedResult{}, err
	}
	return MigratedResult{Kind: "request", ID: id}, nil
}
