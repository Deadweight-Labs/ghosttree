package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("ghosttree_valid_utf8", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) != 1 {
			return int64(0), nil
		}
		value, ok := args[0].(string)
		if !ok || !utf8.ValidString(value) {
			return int64(0), nil
		}
		return int64(1), nil
	})
}

const contextSnapshotInvariantVersion = 2

const contextSnapshotInvariantTableSQL = `CREATE TABLE IF NOT EXISTS context_snapshot_invariants(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  version INTEGER NOT NULL CHECK(version>0))`

const contextSnapshotsTableV1SQL = `CREATE TABLE IF NOT EXISTS context_snapshots(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project TEXT COLLATE BINARY NOT NULL CHECK(typeof(project)='text'),
  name TEXT COLLATE BINARY NOT NULL CHECK(typeof(name)='text'),
  schema_version INTEGER NOT NULL CHECK(schema_version>0),
  state TEXT NOT NULL CHECK(state IN ('building','sealed')),
  content_digest BLOB CHECK(content_digest IS NULL OR (typeof(content_digest)='blob' AND length(content_digest)=32)),
  git_object_format TEXT NOT NULL CHECK(git_object_format IN ('sha1','sha256')),
  git_commit TEXT NOT NULL CHECK(
    git_commit NOT GLOB '*[^0-9a-f]*' AND
    ((git_object_format='sha1' AND length(git_commit)=40) OR
     (git_object_format='sha256' AND length(git_commit)=64))),
  git_ref TEXT,
  git_branch TEXT,
  git_dirty INTEGER NOT NULL CHECK(git_dirty IN (0,1)),
  git_worktree_fingerprint_version INTEGER,
  git_worktree_fingerprint BLOB,
  allow_dirty_used INTEGER NOT NULL CHECK(allow_dirty_used IN (0,1)),
  git_metadata_source TEXT NOT NULL CHECK(git_metadata_source IN ('server-verified','client-reported')),
  message TEXT,
  actor_id TEXT NOT NULL,
  actor_label TEXT,
  session_ref TEXT,
  created_at TEXT NOT NULL,
  entry_count INTEGER NOT NULL DEFAULT 0 CHECK(entry_count>=0),
  payload_bytes_total INTEGER NOT NULL DEFAULT 0 CHECK(payload_bytes_total>=0),
  counts_json BLOB CHECK(counts_json IS NULL OR typeof(counts_json)='blob'),
  UNIQUE(project,name),
  CHECK(length(name) BETWEEN 1 AND 128),
  CHECK(name GLOB '[A-Za-z0-9]*' AND name NOT GLOB '*[^A-Za-z0-9._+-]*'),
  CHECK(
    (git_dirty=0 AND git_worktree_fingerprint_version IS NULL AND git_worktree_fingerprint IS NULL) OR
    (git_dirty=1 AND git_worktree_fingerprint_version=1 AND typeof(git_worktree_fingerprint)='blob' AND length(git_worktree_fingerprint)=32)),
  CHECK(allow_dirty_used=0 OR git_dirty=1))`

const contextSnapshotLogicalSizeColumnSQL = `sealed_logical_bytes INTEGER CHECK(sealed_logical_bytes IS NULL OR (typeof(sealed_logical_bytes)='integer' AND sealed_logical_bytes>=0))`

var contextSnapshotsTableSQL = strings.Replace(contextSnapshotsTableV1SQL, "  UNIQUE(project,name),", "  "+contextSnapshotLogicalSizeColumnSQL+",\n  UNIQUE(project,name),", 1)

const contextSnapshotEntriesTableSQL = `CREATE TABLE IF NOT EXISTS context_snapshot_entries(
  snapshot_id INTEGER NOT NULL REFERENCES context_snapshots(id) ON DELETE RESTRICT,
  domain TEXT COLLATE BINARY NOT NULL CHECK(typeof(domain)='text' AND length(CAST(domain AS BLOB)) BETWEEN 1 AND 32 AND domain GLOB '[A-Za-z0-9]*' AND domain NOT GLOB '*[^A-Za-z0-9._-]*'),
  entry_key TEXT COLLATE BINARY NOT NULL CHECK(typeof(entry_key)='text' AND ghosttree_valid_utf8(entry_key)=1 AND length(CAST(entry_key AS BLOB)) BETWEEN 1 AND 4096),
  payload BLOB NOT NULL CHECK(typeof(payload)='blob'),
  payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
  payload_size INTEGER NOT NULL CHECK(payload_size>=0 AND payload_size=length(payload)),
  PRIMARY KEY(snapshot_id,domain,entry_key))`

const contextSnapshotsProjectIndexSQL = `CREATE INDEX IF NOT EXISTS context_snapshots_project_id ON context_snapshots(project,id DESC)`

var contextSnapshotTriggerV1SQL = map[string]string{
	"context_snapshot_head_insert": `CREATE TRIGGER IF NOT EXISTS context_snapshot_head_insert
BEFORE INSERT ON context_snapshots
WHEN NEW.state!='building' OR NEW.content_digest IS NOT NULL OR NEW.counts_json IS NOT NULL OR NEW.entry_count!=0 OR NEW.payload_bytes_total!=0
BEGIN SELECT RAISE(ABORT,'context snapshot must start as an empty building head'); END`,
	"context_snapshot_head_update": `CREATE TRIGGER IF NOT EXISTS context_snapshot_head_update
BEFORE UPDATE ON context_snapshots
WHEN NOT (
  OLD.state='building' AND NEW.state='sealed' AND
  NEW.id IS OLD.id AND NEW.project IS OLD.project AND NEW.name IS OLD.name AND
  NEW.schema_version IS OLD.schema_version AND
  NEW.git_object_format IS OLD.git_object_format AND NEW.git_commit IS OLD.git_commit AND
  NEW.git_ref IS OLD.git_ref AND NEW.git_branch IS OLD.git_branch AND NEW.git_dirty IS OLD.git_dirty AND
  NEW.git_worktree_fingerprint_version IS OLD.git_worktree_fingerprint_version AND
  NEW.git_worktree_fingerprint IS OLD.git_worktree_fingerprint AND
  NEW.allow_dirty_used IS OLD.allow_dirty_used AND NEW.git_metadata_source IS OLD.git_metadata_source AND
  NEW.message IS OLD.message AND NEW.actor_id IS OLD.actor_id AND NEW.actor_label IS OLD.actor_label AND
  NEW.session_ref IS OLD.session_ref AND NEW.created_at IS OLD.created_at AND
  NEW.content_digest IS NOT NULL AND length(NEW.content_digest)=32 AND NEW.counts_json IS NOT NULL AND
  NEW.entry_count>=0 AND NEW.payload_bytes_total>=0)
BEGIN SELECT RAISE(ABORT,'context snapshot head is immutable'); END`,
	"context_snapshot_head_delete": `CREATE TRIGGER IF NOT EXISTS context_snapshot_head_delete
BEFORE DELETE ON context_snapshots
BEGIN SELECT RAISE(ABORT,'context snapshot head is immutable'); END`,
	"context_snapshot_entry_insert": `CREATE TRIGGER IF NOT EXISTS context_snapshot_entry_insert
BEFORE INSERT ON context_snapshot_entries
WHEN (SELECT state FROM context_snapshots WHERE id=NEW.snapshot_id) IS NOT 'building'
BEGIN SELECT RAISE(ABORT,'context snapshot entries require a building head'); END`,
	"context_snapshot_entry_update": `CREATE TRIGGER IF NOT EXISTS context_snapshot_entry_update
BEFORE UPDATE ON context_snapshot_entries
BEGIN SELECT RAISE(ABORT,'context snapshot entries are immutable'); END`,
	"context_snapshot_entry_delete": `CREATE TRIGGER IF NOT EXISTS context_snapshot_entry_delete
BEFORE DELETE ON context_snapshot_entries
BEGIN SELECT RAISE(ABORT,'context snapshot entries are immutable'); END`,
}

var contextSnapshotTriggerSQL = map[string]string{
	"context_snapshot_head_insert": `CREATE TRIGGER IF NOT EXISTS context_snapshot_head_insert
BEFORE INSERT ON context_snapshots
WHEN NEW.state!='building' OR NEW.content_digest IS NOT NULL OR NEW.counts_json IS NOT NULL OR NEW.entry_count!=0 OR NEW.payload_bytes_total!=0 OR NEW.sealed_logical_bytes IS NOT NULL
BEGIN SELECT RAISE(ABORT,'context snapshot must start as an empty building head'); END`,
	"context_snapshot_head_update": `CREATE TRIGGER IF NOT EXISTS context_snapshot_head_update
BEFORE UPDATE ON context_snapshots
WHEN NOT (
  OLD.state='building' AND NEW.state='sealed' AND
  NEW.id IS OLD.id AND NEW.project IS OLD.project AND NEW.name IS OLD.name AND
  NEW.schema_version IS OLD.schema_version AND
  NEW.git_object_format IS OLD.git_object_format AND NEW.git_commit IS OLD.git_commit AND
  NEW.git_ref IS OLD.git_ref AND NEW.git_branch IS OLD.git_branch AND NEW.git_dirty IS OLD.git_dirty AND
  NEW.git_worktree_fingerprint_version IS OLD.git_worktree_fingerprint_version AND
  NEW.git_worktree_fingerprint IS OLD.git_worktree_fingerprint AND
  NEW.allow_dirty_used IS OLD.allow_dirty_used AND NEW.git_metadata_source IS OLD.git_metadata_source AND
  NEW.message IS OLD.message AND NEW.actor_id IS OLD.actor_id AND NEW.actor_label IS OLD.actor_label AND
  NEW.session_ref IS OLD.session_ref AND NEW.created_at IS OLD.created_at AND
  NEW.content_digest IS NOT NULL AND length(NEW.content_digest)=32 AND NEW.counts_json IS NOT NULL AND
  NEW.entry_count>=0 AND NEW.payload_bytes_total>=0 AND
  typeof(NEW.sealed_logical_bytes)='integer' AND NEW.sealed_logical_bytes>=0)
BEGIN SELECT RAISE(ABORT,'context snapshot head is immutable'); END`,
	"context_snapshot_head_delete":  contextSnapshotTriggerV1SQL["context_snapshot_head_delete"],
	"context_snapshot_entry_insert": contextSnapshotTriggerV1SQL["context_snapshot_entry_insert"],
	"context_snapshot_entry_update": contextSnapshotTriggerV1SQL["context_snapshot_entry_update"],
	"context_snapshot_entry_delete": contextSnapshotTriggerV1SQL["context_snapshot_entry_delete"],
}

func EnsureContextSnapshotSchema(db *sql.DB) error {
	if err := requireSnapshotSingleConnection(db); err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA recursive_triggers=ON`); err != nil {
		return err
	}
	if _, err := db.Exec(contextSnapshotInvariantTableSQL); err != nil {
		return err
	}
	if _, err := db.Exec(contextSnapshotsTableV1SQL); err != nil {
		return err
	}
	if _, err := db.Exec(contextSnapshotEntriesTableSQL); err != nil {
		return err
	}
	if _, err := db.Exec(contextSnapshotsProjectIndexSQL); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO context_snapshot_invariants(singleton,version) VALUES(1,1)`); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM context_snapshot_invariants WHERE singleton=1`).Scan(&version); err != nil {
		return err
	}
	if version == 1 {
		for _, name := range snapshotTriggerNames() {
			if _, err := db.Exec(contextSnapshotTriggerV1SQL[name]); err != nil {
				return err
			}
		}
		if err := migrateContextSnapshotSchemaV1(db); err != nil {
			return err
		}
	} else if version == contextSnapshotInvariantVersion {
		for _, name := range snapshotTriggerNames() {
			if _, err := db.Exec(contextSnapshotTriggerSQL[name]); err != nil {
				return err
			}
		}
	}
	current, err := ContextSnapshotSchemaCurrent(db)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("context snapshot schema invariant mismatch")
	}
	return nil
}

func migrateContextSnapshotSchemaV1(db *sql.DB) (err error) {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	current, err := contextSnapshotSchemaDefinitionsMatch(context.Background(), conn, 1, contextSnapshotsTableV1SQL, contextSnapshotTriggerV1SQL)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("context snapshot v1 schema invariant mismatch")
	}
	if _, err := conn.ExecContext(context.Background(), `DROP TRIGGER context_snapshot_head_update`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.Background(), `ALTER TABLE context_snapshots ADD COLUMN `+contextSnapshotLogicalSizeColumnSQL); err != nil {
		return err
	}

	rows, err := conn.QueryContext(context.Background(), `SELECT project,name FROM context_snapshots WHERE state='sealed' ORDER BY id`)
	if err != nil {
		return err
	}
	type identity struct{ project, name string }
	var identities []identity
	for rows.Next() {
		var v identity
		if err := rows.Scan(&v.project, &v.name); err != nil {
			rows.Close()
			return err
		}
		identities = append(identities, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, v := range identities {
		h, found, err := loadContextSnapshotHead(context.Background(), conn, v.project, v.name)
		if err != nil {
			return err
		}
		if !found {
			return &snapshot.RuleError{Code: "snapshot_integrity_error"}
		}
		entries, counts, payloadTotal, err := readStoredSnapshotEntries(context.Background(), conn, h.ID, h.SchemaVersion)
		if err != nil {
			return err
		}
		if snapshot.ContentDigest(h.SchemaVersion, entries) != h.ContentDigest || payloadTotal != h.PayloadBytesTotal || !equalCounts(counts, h.Counts) {
			return &snapshot.RuleError{Code: "snapshot_integrity_error"}
		}
		logical, err := contextSnapshotLogicalSize(h, entries)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(context.Background(), `UPDATE context_snapshots SET sealed_logical_bytes=? WHERE id=? AND state='sealed' AND sealed_logical_bytes IS NULL`, logical, h.ID); err != nil {
			return err
		}
	}
	for _, name := range snapshotTriggerNames() {
		if _, err := conn.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS `+name); err != nil {
			return err
		}
		if _, err := conn.ExecContext(context.Background(), contextSnapshotTriggerSQL[name]); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE context_snapshot_invariants SET version=? WHERE singleton=1 AND version=1`, contextSnapshotInvariantVersion); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.Background(), `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

type snapshotSchemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func contextSnapshotSchemaDefinitionsMatch(ctx context.Context, q snapshotSchemaQueryer, version int, headSQL string, triggers map[string]string) (bool, error) {
	var gotVersion, rows int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0),count(*) FROM context_snapshot_invariants WHERE singleton=1`).Scan(&gotVersion, &rows); err != nil {
		return false, err
	}
	if rows != 1 || gotVersion != version {
		return false, nil
	}
	expectedTables := map[string]string{
		"context_snapshot_invariants": contextSnapshotInvariantTableSQL,
		"context_snapshots":           headSQL,
		"context_snapshot_entries":    contextSnapshotEntriesTableSQL,
	}
	for name, expected := range expectedTables {
		var actual string
		if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if normalizeSnapshotSQL(actual) != normalizeSnapshotSQL(expected) {
			return false, nil
		}
	}
	var actualIndex string
	if err := q.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='context_snapshots_project_id'`).Scan(&actualIndex); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if normalizeSnapshotSQL(actualIndex) != normalizeSnapshotSQL(contextSnapshotsProjectIndexSQL) {
		return false, nil
	}
	actualTriggers := make(map[string]string)
	triggerRows, err := q.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('context_snapshots','context_snapshot_entries')`)
	if err != nil {
		return false, err
	}
	for triggerRows.Next() {
		var name, definition string
		if err := triggerRows.Scan(&name, &definition); err != nil {
			triggerRows.Close()
			return false, err
		}
		actualTriggers[name] = definition
	}
	if err := triggerRows.Err(); err != nil {
		triggerRows.Close()
		return false, err
	}
	if err := triggerRows.Close(); err != nil {
		return false, err
	}
	if len(actualTriggers) != len(triggers) {
		return false, nil
	}
	for name, expected := range triggers {
		if normalizeSnapshotSQL(actualTriggers[name]) != normalizeSnapshotSQL(expected) {
			return false, nil
		}
	}
	var building int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM context_snapshots WHERE state='building'`).Scan(&building); err != nil {
		return false, err
	}
	return building == 0, nil
}

func ContextSnapshotSchemaCurrent(db *sql.DB) (bool, error) {
	if err := requireSnapshotSingleConnection(db); err != nil {
		return false, err
	}
	var foreignKeys, recursiveTriggers int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return false, err
	}
	if err := db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursiveTriggers); err != nil {
		return false, err
	}
	if foreignKeys != 1 || recursiveTriggers != 1 {
		return false, nil
	}

	current, err := contextSnapshotSchemaDefinitionsMatch(context.Background(), db, contextSnapshotInvariantVersion, contextSnapshotsTableSQL, contextSnapshotTriggerSQL)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return false, nil
		}
		return false, err
	}
	if !current {
		return false, nil
	}
	var missingLogicalSize int
	if err := db.QueryRow(`SELECT count(*) FROM context_snapshots WHERE state='sealed' AND sealed_logical_bytes IS NULL`).Scan(&missingLogicalSize); err != nil {
		return false, err
	}
	return missingLogicalSize == 0, nil
}

func ProbeContextSnapshotSchema(ctx context.Context, db *sql.DB) error {
	if err := requireSnapshotSingleConnection(db); err != nil {
		return err
	}
	current, err := ContextSnapshotSchemaCurrent(db)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("context snapshot schema invariant mismatch")
	}
	return probeContextSnapshotSchema(ctx, db, exerciseContextSnapshotSchema)
}

func requireSnapshotSingleConnection(db *sql.DB) error {
	if maximum := db.Stats().MaxOpenConnections; maximum != 1 {
		return fmt.Errorf("context snapshot schema requires MaxOpenConns=1, got %d", maximum)
	}
	return nil
}

type snapshotProbeExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func probeContextSnapshotSchema(ctx context.Context, db *sql.DB, exercise func(context.Context, snapshotProbeExecer) error) error {
	before, err := contextSnapshotCounts(ctx, db)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	probeErr := exercise(ctx, tx)
	background := context.Background()
	rollbackErr := tx.Rollback()
	after, countErr := contextSnapshotCounts(background, db)
	var cleanup []error
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		cleanup = append(cleanup, fmt.Errorf("rollback snapshot probe: %w", rollbackErr))
	}
	if countErr != nil {
		cleanup = append(cleanup, fmt.Errorf("count after snapshot probe rollback: %w", countErr))
	} else if after != before {
		cleanup = append(cleanup, fmt.Errorf("snapshot probe changed table counts: %v -> %v", before, after))
	}
	cleanup = append(cleanup, probeErr)
	return errors.Join(cleanup...)
}

func exerciseContextSnapshotSchema(ctx context.Context, tx snapshotProbeExecer) error {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := "probe-" + hex.EncodeToString(random[:])
	result, err := tx.ExecContext(ctx, snapshotProbeHeadInsertSQL, "__ghosttree_snapshot_probe__", name)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,zeroblob(32),2)`, id, "ghost", "file/probe", []byte(`{}`)); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE context_snapshot_entries SET payload=x'00' WHERE snapshot_id=?`,
		`DELETE FROM context_snapshot_entries WHERE snapshot_id=?`,
	} {
		if err := expectSnapshotProbeFailure(ctx, tx, statement, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE context_snapshots SET state='sealed',content_digest=zeroblob(32),entry_count=1,payload_bytes_total=2,counts_json=x'7b7d',sealed_logical_bytes=2 WHERE id=?`, id); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE context_snapshots SET message='changed' WHERE id=?`,
		`UPDATE context_snapshots SET sealed_logical_bytes=3 WHERE id=?`,
		`DELETE FROM context_snapshots WHERE id=?`,
		`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,'ghost','file/late',x'7b7d',zeroblob(32),2)`,
	} {
		if err := expectSnapshotProbeFailure(ctx, tx, statement, id); err != nil {
			return err
		}
	}
	return nil
}

const snapshotProbeHeadInsertSQL = `INSERT INTO context_snapshots(project,name,schema_version,state,content_digest,git_object_format,git_commit,git_ref,git_branch,git_dirty,git_worktree_fingerprint_version,git_worktree_fingerprint,allow_dirty_used,git_metadata_source,message,actor_id,actor_label,session_ref,created_at,entry_count,payload_bytes_total,counts_json) VALUES(?,?,1,'building',NULL,'sha1','0000000000000000000000000000000000000000',NULL,NULL,0,NULL,NULL,0,'server-verified',NULL,'probe',NULL,NULL,'1970-01-01T00:00:00Z',0,0,NULL)`

func expectSnapshotProbeFailure(ctx context.Context, tx snapshotProbeExecer, statement string, args ...any) error {
	if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
		return fmt.Errorf("snapshot invariant probe unexpectedly succeeded: %s", statement)
	}
	return nil
}

func contextSnapshotCounts(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) ([2]int64, error) {
	var counts [2]int64
	if err := query.QueryRowContext(ctx, `SELECT count(*) FROM context_snapshots`).Scan(&counts[0]); err != nil {
		return counts, err
	}
	if err := query.QueryRowContext(ctx, `SELECT count(*) FROM context_snapshot_entries`).Scan(&counts[1]); err != nil {
		return counts, err
	}
	return counts, nil
}

func snapshotTriggerNames() []string {
	return []string{
		"context_snapshot_head_insert",
		"context_snapshot_head_update",
		"context_snapshot_head_delete",
		"context_snapshot_entry_insert",
		"context_snapshot_entry_update",
		"context_snapshot_entry_delete",
	}
}

func normalizeSnapshotSQL(statement string) string {
	normalized := strings.Join(strings.Fields(statement), " ")
	normalized = strings.ReplaceAll(normalized, " IF NOT EXISTS ", " ")
	return strings.TrimSuffix(normalized, ";")
}

func containsNormalizedSQL(statement, fragment string) bool {
	return strings.Contains(strings.ToLower(normalizeSnapshotSQL(statement)), strings.ToLower(normalizeSnapshotSQL(fragment)))
}
