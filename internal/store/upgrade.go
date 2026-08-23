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

// SchemaHasNewTypes reports whether the instruction/request types are allowed.
// SQLite does not expose CHECK constraints structurally, so this probes inside
// a transaction that is always rolled back.
func SchemaHasNewTypes(db *sql.DB) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO knowledge(type, title, body, confidence, status, origin,
		superseded_by, created_at, updated_at)
		VALUES('instruction','probe','probe','trusted','active','agent',0,'x','x')`)
	if err != nil {
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO knowledge(type, title, body, confidence, status, origin,
		superseded_by, created_at, updated_at)
		VALUES('request','probe','probe','trusted','archived','agent',0,'x','x')`)
	return err == nil, nil
}

const migrationTables = `
CREATE TABLE IF NOT EXISTS request_resolution(knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id), state TEXT NOT NULL CHECK(state IN ('open','done','dropped')), evidence_kind TEXT NOT NULL DEFAULT '', evidence_ref TEXT NOT NULL DEFAULT '', by_person TEXT NOT NULL DEFAULT '', at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS migration_runs(id INTEGER PRIMARY KEY, project TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','complete')), created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS migration_artifacts(run_id INTEGER NOT NULL REFERENCES migration_runs(id), path TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(run_id,path));
CREATE TABLE IF NOT EXISTS migration_evidence(knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id), run_id INTEGER NOT NULL REFERENCES migration_runs(id), source TEXT NOT NULL, digest TEXT NOT NULL, item_key TEXT NOT NULL UNIQUE, quote TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS instruction_activation_path(knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE, pattern TEXT NOT NULL, PRIMARY KEY(knowledge_id,pattern));
CREATE TABLE IF NOT EXISTS instruction_activation_task(knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE, task TEXT NOT NULL CHECK(task IN ('code','review','test','deploy','security','docs')), PRIMARY KEY(knowledge_id,task));`

// UpgradeTypes extends the knowledge type and status constraints. It writes a
// backup before rebuilding the table and preserves ids used by the FTS index.
func UpgradeTypes(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	current, err := SchemaHasNewTypes(db)
	if err != nil {
		return "", err
	}
	if current {
		_, err = db.Exec(migrationTables)
		return "", err
	}

	backup := fmt.Sprintf("%s.backup-types-%s", path, time.Now().UTC().Format("20060102-150405"))
	if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return "", fmt.Errorf("backup to %s: %w", backup, err)
	}
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
		  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan','instruction','request')),
		  title TEXT NOT NULL, body TEXT NOT NULL,
		  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
		  confidence TEXT NOT NULL DEFAULT 'trusted' CHECK(confidence IN ('quarantined','staged','trusted','verified')),
		  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated','superseded','archived')),
		  origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
		  superseded_by INTEGER NOT NULL DEFAULT 0,
		  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
		  created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO knowledge_new(id, type, title, body, project, branch, machine,
		   confidence, status, origin, superseded_by, person, harness, session_ref, created_at, updated_at)
		 SELECT id, type, title, body, project, branch, machine,
		   confidence, status, origin, superseded_by, person, harness, session_ref, created_at, updated_at
		 FROM knowledge`,
		`DROP TABLE knowledge`,
		`ALTER TABLE knowledge_new RENAME TO knowledge`,
		`CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge BEGIN
		   INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END`,
		`CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
		   INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
		   INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END`,
		`INSERT INTO knowledge_fts(knowledge_fts) VALUES('rebuild')`,
		migrationTables,
	}
	for _, step := range steps {
		if _, err := tx.Exec(step); err != nil {
			return backup, fmt.Errorf("upgrade step failed (%.60s...): %w", step, err)
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
