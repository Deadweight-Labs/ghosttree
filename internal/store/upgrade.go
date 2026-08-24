package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
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
	if err := createVerifiedBackup(db, backup); err != nil {
		return "", err
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

// SchemaHasNewTypes reports whether instructions and the separated request
// domain are available and the knowledge write path rejects requests.
func SchemaHasNewTypes(db *sql.DB) (bool, error) {
	var legacyRequests int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE type='request'`).Scan(&legacyRequests); err != nil {
		return false, err
	}
	if legacyRequests != 0 {
		return false, nil
	}
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
	if err == nil {
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO requests(type,title,description,created_at,updated_at) VALUES('feature','probe','probe','x','x')`)
	return err == nil, nil
}

func schemaAllowsInstruction(db *sql.DB) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO knowledge(type,title,body,confidence,status,origin,superseded_by,created_at,updated_at)
		VALUES('instruction','probe','probe','trusted','active','agent',0,'x','x')`)
	return err == nil, nil
}

const migrationTables = `
CREATE TABLE IF NOT EXISTS request_resolution(knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id), state TEXT NOT NULL CHECK(state IN ('open','done','dropped')), evidence_kind TEXT NOT NULL DEFAULT '', evidence_ref TEXT NOT NULL DEFAULT '', by_person TEXT NOT NULL DEFAULT '', at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS migration_runs(id INTEGER PRIMARY KEY, project TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','complete')), created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS migration_artifacts(run_id INTEGER NOT NULL REFERENCES migration_runs(id), path TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(run_id,path));
CREATE TABLE IF NOT EXISTS migration_evidence(knowledge_id INTEGER PRIMARY KEY REFERENCES knowledge(id), run_id INTEGER NOT NULL REFERENCES migration_runs(id), source TEXT NOT NULL, digest TEXT NOT NULL, item_key TEXT NOT NULL UNIQUE, quote TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS instruction_activation_path(knowledge_id INTEGER NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE, pattern TEXT NOT NULL, PRIMARY KEY(knowledge_id,pattern));`

// UpgradeTypes extends the knowledge type and status constraints. It writes a
// backup before rebuilding the table and preserves ids used by the FTS index.
func UpgradeTypes(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	current, err := schemaAllowsInstruction(db)
	if err != nil {
		return "", err
	}
	if current {
		_, err = db.Exec(migrationTables)
		return "", err
	}

	backup := fmt.Sprintf("%s.backup-types-%s", path, time.Now().UTC().Format("20060102-150405"))
	if err := createVerifiedBackup(db, backup); err != nil {
		return "", err
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

// UpgradeRequestDomain moves legacy knowledge(type=request) rows and their
// evidence into the dedicated request ledger, then removes request from the
// knowledge table constraint.
func UpgradeRequestDomain(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return "", err
	}
	if err := verifyIntegrity(db); err != nil {
		return "", err
	}
	current, err := SchemaHasNewTypes(db)
	if err != nil {
		return "", err
	}
	if current {
		if _, err := db.Exec(schema); err != nil {
			return "", err
		}
		return "", nil
	}
	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge WHERE type='request'`).Scan(&legacy); err != nil {
		return "", err
	}
	backup := fmt.Sprintf("%s.backup-requests-%s", path, time.Now().UTC().Format("20060102-150405.000000000"))
	if err := createVerifiedBackup(db, backup); err != nil {
		return backup, err
	}
	// Only mutate the source after a consistent, readable backup exists.
	if _, err := db.Exec(schema); err != nil {
		return backup, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return backup, err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)
	tx, err := db.Begin()
	if err != nil {
		return backup, err
	}
	defer tx.Rollback()
	steps := []string{
		`INSERT INTO requests(id,type,title,description,state,project,branch,machine,origin,person,session_ref,created_at,updated_at)
		 SELECT k.id,'change',k.title,k.body,COALESCE(r.state,'open'),k.project,k.branch,k.machine,k.origin,k.person,k.session_ref,k.created_at,k.updated_at
		 FROM knowledge k LEFT JOIN request_resolution r ON r.knowledge_id=k.id WHERE k.type='request'`,
		`INSERT INTO request_evidence(request_id,kind,ref,person,created_at)
		 SELECT k.id,r.evidence_kind,r.evidence_ref,r.by_person,r.at
		 FROM knowledge k JOIN request_resolution r ON r.knowledge_id=k.id
		 WHERE k.type='request' AND r.state='done' AND r.evidence_ref!=''`,
		`INSERT INTO request_evidence(request_id,kind,ref,person,created_at)
		 SELECT k.id,'session',printf('session:%d#chunk:%d',e.session_id,e.chunk_seq),k.person,k.updated_at
		 FROM knowledge k JOIN knowledge_evidence e ON e.knowledge_id=k.id WHERE k.type='request'`,
		`INSERT INTO request_activity(request_id,kind,person,data,created_at)
		 SELECT k.id,'evidence.migrated',k.person,e.quote,k.updated_at
		 FROM knowledge k JOIN knowledge_evidence e ON e.knowledge_id=k.id WHERE k.type='request'`,
		`INSERT INTO request_activity(request_id,kind,person,data,created_at)
		 SELECT k.id,'request.migrated',k.person,k.session_ref,k.updated_at FROM knowledge k WHERE k.type='request'`,
		`DELETE FROM search_documents`,
		`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine)
		 SELECT 'knowledge',id,title,body,project,branch,machine FROM knowledge WHERE type!='request'`,
		`INSERT INTO search_documents(kind,domain_id,title,body,project,branch,machine)
		 SELECT 'request',id,title,body,project,branch,machine FROM knowledge WHERE type='request'`,
		`CREATE TABLE migration_evidence_new(
		 id INTEGER PRIMARY KEY,
		 knowledge_id INTEGER REFERENCES knowledge(id), request_id INTEGER REFERENCES requests(id),
		 run_id INTEGER NOT NULL REFERENCES migration_runs(id), source TEXT NOT NULL, digest TEXT NOT NULL,
		 item_key TEXT NOT NULL UNIQUE, quote TEXT NOT NULL DEFAULT '',
		 CHECK((knowledge_id IS NOT NULL) != (request_id IS NOT NULL)),
		 UNIQUE(knowledge_id), UNIQUE(request_id))`,
		`INSERT INTO migration_evidence_new(id,knowledge_id,request_id,run_id,source,digest,item_key,quote)
		 SELECT e.rowid,CASE WHEN k.type='request' THEN NULL ELSE e.knowledge_id END,
		 CASE WHEN k.type='request' THEN e.knowledge_id ELSE NULL END,e.run_id,e.source,e.digest,e.item_key,e.quote
		 FROM migration_evidence e JOIN knowledge k ON k.id=e.knowledge_id`,
		`DROP TABLE migration_evidence`,
		`ALTER TABLE migration_evidence_new RENAME TO migration_evidence`,
		`DELETE FROM request_resolution WHERE knowledge_id IN (SELECT id FROM knowledge WHERE type='request')`,
		`DELETE FROM knowledge_evidence WHERE knowledge_id IN (SELECT id FROM knowledge WHERE type='request')`,
		`DELETE FROM instruction_activation_path WHERE knowledge_id IN (SELECT id FROM knowledge WHERE type='request')`,
		`DROP TRIGGER IF EXISTS knowledge_ai`,
		`DROP TRIGGER IF EXISTS knowledge_au`,
		`CREATE TABLE knowledge_new(
		 id INTEGER PRIMARY KEY,
		 type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan','instruction')),
		 title TEXT NOT NULL, body TEXT NOT NULL,
		 project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
		 confidence TEXT NOT NULL DEFAULT 'trusted' CHECK(confidence IN ('quarantined','staged','trusted','verified')),
		 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated','superseded','archived')),
		 origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
		 superseded_by INTEGER NOT NULL DEFAULT 0,
		 person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
		 created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO knowledge_new(id,type,title,body,project,branch,machine,confidence,status,origin,superseded_by,person,harness,session_ref,created_at,updated_at)
		 SELECT id,type,title,body,project,branch,machine,confidence,status,origin,superseded_by,person,harness,session_ref,created_at,updated_at
		 FROM knowledge WHERE type!='request'`,
		`DROP TABLE knowledge`,
		`ALTER TABLE knowledge_new RENAME TO knowledge`,
		`CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge BEGIN
		 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END`,
		`CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
		 INSERT INTO knowledge_fts(knowledge_fts,rowid,title,body) VALUES('delete',old.id,old.title,old.body);
		 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END`,
		`INSERT INTO knowledge_fts(knowledge_fts) VALUES('rebuild')`,
	}
	for _, step := range steps {
		if _, err := tx.Exec(step); err != nil {
			return backup, fmt.Errorf("request upgrade step failed (%.60s...): %w", step, err)
		}
	}
	var mapped int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM requests r WHERE EXISTS (SELECT 1 FROM search_documents d WHERE d.kind='request' AND d.domain_id=r.id)`).Scan(&mapped); err != nil {
		return backup, err
	}
	if mapped != legacy {
		return backup, fmt.Errorf("request mappings after upgrade are %d, expected exactly %d", mapped, legacy)
	}
	var missingKnowledge int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM knowledge k WHERE NOT EXISTS (SELECT 1 FROM search_documents d WHERE d.kind='knowledge' AND d.domain_id=k.id)`).Scan(&missingKnowledge); err != nil {
		return backup, err
	}
	if missingKnowledge != 0 {
		return backup, fmt.Errorf("%d knowledge rows are absent from search projection", missingKnowledge)
	}
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return backup, err
	}
	if rows.Next() {
		rows.Close()
		return backup, fmt.Errorf("foreign key check failed before commit")
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return backup, err
	}
	return backup, verifyIntegrity(db)
}

// FileSHA256 returns the stable digest used to identify an upgrade backup.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func verifyIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity check: %s", result)
	}
	return nil
}

func createVerifiedBackup(db *sql.DB, backup string) error {
	if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return fmt.Errorf("backup to %s: %w", backup, err)
	}
	before, err := FileSHA256(backup)
	if err != nil {
		return fmt.Errorf("checksum backup %s: %w", backup, err)
	}
	copyDB, err := sql.Open("sqlite", backup)
	if err != nil {
		return fmt.Errorf("open backup %s: %w", backup, err)
	}
	verifyErr := verifyIntegrity(copyDB)
	closeErr := copyDB.Close()
	if verifyErr != nil {
		return fmt.Errorf("verify backup %s: %w", backup, verifyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	after, err := FileSHA256(backup)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("backup checksum changed during verification")
	}
	return nil
}

// UpgradeUsageTelemetry adds the two usage columns to an existing knowledge
// table. Unlike the earlier upgrades this needs no table rebuild — SQLite adds
// a column with a constant default as metadata only — but it still takes a
// verified backup first, because a 780 MB production database is not the place
// to discover that an assumption about ALTER TABLE was wrong.
func UpgradeUsageTelemetry(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return "", err
	}
	if err := verifyIntegrity(db); err != nil {
		return "", err
	}
	missing, err := missingUsageColumns(db)
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return "", nil
	}
	backup := fmt.Sprintf("%s.backup-usage-%s", path, time.Now().UTC().Format("20060102-150405.000000000"))
	if err := createVerifiedBackup(db, backup); err != nil {
		return backup, err
	}
	for _, column := range missing {
		if _, err := db.Exec(`ALTER TABLE knowledge ADD COLUMN ` + column); err != nil {
			return backup, err
		}
	}
	return backup, nil
}

func missingUsageColumns(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`PRAGMA table_info(knowledge)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []string
	if !present["last_used_at"] {
		missing = append(missing, `last_used_at TEXT NOT NULL DEFAULT ''`)
	}
	if !present["hit_count"] {
		missing = append(missing, `hit_count INTEGER NOT NULL DEFAULT 0`)
	}
	// search_hits is separate from hit_count on purpose. hit_count answers
	// "was this ever put in front of an agent" and counts delivery; ranking by
	// it would be circular, because delivery is what the ranking decides.
	if !present["search_hits"] {
		missing = append(missing, `search_hits INTEGER NOT NULL DEFAULT 0`)
	}
	return missing, nil
}

// UpgradeDistillationVersion widens the session_distillations key to include
// the prompt version. Existing rows are labelled v1, which is what they are:
// every distillation that exists predates versioning and was produced by the
// original prompt. Leaving them empty would have been more literal and less
// useful — an empty version cannot be named on a command line without
// colliding with "flag not given".
//
// The primary key changes, so this is a table rebuild rather than an added
// column. The table is small (one row per processed session), so the rebuild is
// cheap even on the production database.
func UpgradeDistillationVersion(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return "", err
	}
	if err := verifyIntegrity(db); err != nil {
		return "", err
	}
	has, err := hasDistillationVersionColumn(db)
	if err != nil {
		return "", err
	}
	if has {
		// A database upgraded by an earlier build of this command carries
		// empty versions. Naming them is idempotent and needs no backup.
		if _, err := db.Exec(`UPDATE session_distillations SET prompt_version='v1' WHERE prompt_version=''`); err != nil {
			return "", err
		}
		return "", nil
	}
	backup := fmt.Sprintf("%s.backup-distillversion-%s", path, time.Now().UTC().Format("20060102-150405.000000000"))
	if err := createVerifiedBackup(db, backup); err != nil {
		return backup, err
	}
	// Only mutate the source after a consistent, readable backup exists.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return backup, err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)
	tx, err := db.Begin()
	if err != nil {
		return backup, err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`CREATE TABLE session_distillations_new(
			session_id INTEGER NOT NULL REFERENCES sessions(id),
			digest TEXT NOT NULL,
			prompt_version TEXT NOT NULL DEFAULT '',
			item_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			PRIMARY KEY(session_id,digest,prompt_version))`,
		`INSERT INTO session_distillations_new(session_id,digest,prompt_version,item_count,created_at)
			SELECT session_id,digest,'v1',item_count,created_at FROM session_distillations`,
		`DROP TABLE session_distillations`,
		`ALTER TABLE session_distillations_new RENAME TO session_distillations`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return backup, err
		}
	}
	if err := tx.Commit(); err != nil {
		return backup, err
	}
	return backup, nil
}

func hasDistillationVersionColumn(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(session_distillations)`)
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
		if name == "prompt_version" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// AddedColumns applies the schema's purely additive column changes and names
// the ones it actually added. ALTER TABLE ADD COLUMN with a constant default is
// metadata only, so this needs no backup — but it does need to be reported. An
// upgrade that says "nothing to do" while it changed the schema is how someone
// later debugging an empty column concludes the column was never added.
func AddedColumns(path string) ([]string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return nil, err
	}
	var added []string
	for _, c := range []struct{ table, column, definition string }{
		{"distill_batches", "model", `TEXT NOT NULL DEFAULT ''`},
		{"distill_batch_items", "prompt_version", `TEXT NOT NULL DEFAULT ''`},
		{"knowledge", "observed_at", `TEXT NOT NULL DEFAULT ''`},
	} {
		// A table that does not exist yet needs no column added: Open creates it
		// from the current schema, with the column already in it.
		exists, err := hasTable(db, c.table)
		if err != nil {
			return added, err
		}
		if !exists {
			continue
		}
		before, err := hasColumn(db, c.table, c.column)
		if err != nil {
			return added, err
		}
		if before {
			continue
		}
		if err := addColumnIfMissing(db, c.table, c.column, c.definition); err != nil {
			return added, err
		}
		added = append(added, c.table+"."+c.column)
	}
	return added, nil
}

func hasTable(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// addColumnIfMissing is idempotent by inspection rather than by catching an
// error, so a genuine failure is not mistaken for "already there".
func addColumnIfMissing(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
