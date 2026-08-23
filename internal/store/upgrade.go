package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SchemaCurrent reports whether the trust-tier schema is already in place.
// The origin column is the marker: it only exists after the upgrade.
func SchemaCurrent(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "origin" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// UpgradeSchema rebuilds the knowledge table in place. SQLite cannot alter a
// CHECK constraint, so the table has to be recreated and the external-content
// FTS index rebuilt to match. Returns the path of the backup it wrote, or an
// empty string when the schema was already current.
//
// This is a one-off upgrade rather than a migration framework: ghosttree has
// one operator and one database, so a version ledger would be apparatus for a
// single step.
func UpgradeSchema(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	current, err := SchemaCurrent(db)
	if err != nil {
		return "", err
	}
	if current {
		return "", nil
	}

	backup := fmt.Sprintf("%s.backup-%s", path, time.Now().UTC().Format("20060102-150405"))
	if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return "", fmt.Errorf("backup to %s: %w", backup, err)
	}

	// foreign_keys can only be switched outside a transaction; inside it is a
	// silent no-op, which would let the DROP below cascade unpredictably.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return backup, err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&before); err != nil {
		return backup, err
	}

	tx, err := db.Begin()
	if err != nil {
		return backup, err
	}
	defer tx.Rollback()

	steps := []string{
		`DROP TRIGGER IF EXISTS knowledge_ai`,
		`DROP TRIGGER IF EXISTS knowledge_au`,
		`CREATE TABLE knowledge_new(
		  id INTEGER PRIMARY KEY,
		  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan')),
		  title TEXT NOT NULL, body TEXT NOT NULL,
		  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
		  confidence TEXT NOT NULL DEFAULT 'trusted' CHECK(confidence IN ('quarantined','staged','trusted','verified')),
		  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated','superseded')),
		  origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
		  superseded_by INTEGER NOT NULL DEFAULT 0,
		  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// id is carried over explicitly: the FTS index addresses rows by rowid.
		`INSERT INTO knowledge_new(id, type, title, body, project, branch, machine,
		   confidence, status, origin, superseded_by, person, harness, session_ref, created_at, updated_at)
		 SELECT id, type, title, body, project, branch, machine,
		   CASE confidence WHEN 'observation' THEN 'trusted' ELSE confidence END,
		   status, 'agent', 0, person, harness, session_ref, created_at, updated_at
		 FROM knowledge`,
		`DROP TABLE knowledge`,
		`ALTER TABLE knowledge_new RENAME TO knowledge`,
		`CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge BEGIN
		   INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		 END`,
		`CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
		   INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
		   INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		 END`,
		// Without this the index still describes the dropped table.
		// Cheap insurance: the index survives the DROP as its own tables, so it
		// only stays correct as long as the ids above were carried over
		// verbatim. Rebuilding removes that dependency.
		`INSERT INTO knowledge_fts(knowledge_fts) VALUES('rebuild')`,
		`CREATE TABLE IF NOT EXISTS knowledge_evidence(
		  id INTEGER PRIMARY KEY,
		  knowledge_id INTEGER NOT NULL REFERENCES knowledge(id),
		  session_id INTEGER NOT NULL REFERENCES sessions(id),
		  chunk_seq INTEGER NOT NULL,
		  quote TEXT NOT NULL DEFAULT '',
		  UNIQUE(knowledge_id, session_id, chunk_seq))`,
	}
	for _, s := range steps {
		if _, err := tx.Exec(s); err != nil {
			return backup, fmt.Errorf("upgrade step failed (%.60s...): %w", s, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return backup, err
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&after); err != nil {
		return backup, err
	}
	if before != after {
		return backup, fmt.Errorf("row count changed during upgrade: %d before, %d after (restore %s)", before, after, backup)
	}
	return backup, nil
}
