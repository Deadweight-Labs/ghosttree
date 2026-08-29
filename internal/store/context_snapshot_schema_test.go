package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openSnapshotSchemaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA recursive_triggers=ON`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSnapshotSchemaIsAdditiveAndCurrent(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if _, err := db.Exec(`CREATE TABLE legacy_fixture(id INTEGER PRIMARY KEY, body BLOB NOT NULL); INSERT INTO legacy_fixture(body) VALUES (x'000102')`); err != nil {
		t.Fatal(err)
	}
	var beforeCount, beforeBytes int
	if err := db.QueryRow(`SELECT count(*),sum(length(body)) FROM legacy_fixture`).Scan(&beforeCount, &beforeBytes); err != nil {
		t.Fatal(err)
	}

	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	current, err := ContextSnapshotSchemaCurrent(db)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("new snapshot schema is not current")
	}

	var afterCount, afterBytes int
	if err := db.QueryRow(`SELECT count(*),sum(length(body)) FROM legacy_fixture`).Scan(&afterCount, &afterBytes); err != nil {
		t.Fatal(err)
	}
	if beforeCount != afterCount || beforeBytes != afterBytes {
		t.Fatalf("legacy fixture changed: (%d,%d) -> (%d,%d)", beforeCount, beforeBytes, afterCount, afterBytes)
	}

	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='context_snapshots'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !containsNormalizedSQL(tableSQL, "integer primary key autoincrement") {
		t.Fatalf("snapshot id is not AUTOINCREMENT: %s", tableSQL)
	}
	var onDelete string
	rows, err := db.Query(`PRAGMA foreign_key_list(context_snapshot_entries)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, action, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &action, &match); err != nil {
			t.Fatal(err)
		}
		if table == "context_snapshots" {
			onDelete = action
		}
	}
	if onDelete != "RESTRICT" {
		t.Fatalf("entry foreign key ON DELETE = %q, want RESTRICT", onDelete)
	}
}

func TestSnapshotSchemaRejectsEveryMutationPath(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	id := insertBuildingSnapshot(t, db, "v1")
	assertSnapshotStatementFails(t, db, `INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,'{}','12345678901234567890123456789012',2)`, id, "ghost", "file/text-storage")
	assertSnapshotStatementFails(t, db, `INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,'ghost',CAST(x'ff' AS TEXT),x'7b7d',zeroblob(32),2)`, id)
	if _, err := db.Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,zeroblob(32),2)`, id, "ghost", "file/a", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	assertSnapshotStatementFails(t, db, `UPDATE context_snapshot_entries SET payload=x'00' WHERE snapshot_id=?`, id)
	assertSnapshotStatementFails(t, db, `DELETE FROM context_snapshot_entries WHERE snapshot_id=?`, id)
	assertSnapshotStatementFails(t, db, `UPDATE context_snapshots SET message='changed' WHERE id=?`, id)

	if _, err := db.Exec(`UPDATE context_snapshots SET state='sealed',content_digest=zeroblob(32),entry_count=1,payload_bytes_total=2,counts_json=? WHERE id=?`, []byte(`{"ghost":1}`), id); err != nil {
		t.Fatalf("seal: %v", err)
	}
	assertSnapshotStatementFails(t, db, `UPDATE context_snapshots SET message='changed' WHERE id=?`, id)
	assertSnapshotStatementFails(t, db, `DELETE FROM context_snapshots WHERE id=?`, id)
	assertSnapshotStatementFails(t, db, `INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size) VALUES(?,?,?,?,zeroblob(32),2)`, id, "ghost", "file/b", []byte(`{}`))
	assertSnapshotStatementFails(t, db, snapshotHeadInsertSQL+` ON CONFLICT(project,name) DO UPDATE SET message='changed'`, "p", "v1")
	assertSnapshotStatementFails(t, db, `INSERT OR REPLACE`+snapshotHeadInsertSQL[len("INSERT"):], "p", "v1")

	textHead := insertBuildingSnapshot(t, db, "text-head")
	assertSnapshotStatementFails(t, db, `UPDATE context_snapshots SET state='sealed',content_digest='12345678901234567890123456789012',entry_count=0,payload_bytes_total=0,counts_json='{}' WHERE id=?`, textHead)
}

func TestSnapshotSchemaDetectsObsoleteTriggerAndCommittedBuilding(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER context_snapshot_head_update; CREATE TRIGGER context_snapshot_head_update BEFORE UPDATE ON context_snapshots BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("obsolete trigger current=%v err=%v", current, err)
	}
	if err := EnsureContextSnapshotSchema(db); err == nil {
		t.Fatal("EnsureContextSnapshotSchema accepted an obsolete trigger")
	}

	if _, err := db.Exec(`DROP TRIGGER context_snapshot_head_update`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatalf("restore missing trigger: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER unexpected_probe_bypass BEFORE INSERT ON context_snapshots BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("unexpected trigger current=%v err=%v", current, err)
	}
	if _, err := db.Exec(`DROP TRIGGER unexpected_probe_bypass`); err != nil {
		t.Fatal(err)
	}
	insertBuildingSnapshot(t, db, "unfinished")
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("committed building row current=%v err=%v", current, err)
	}
}

func TestSnapshotSchemaCurrentRequiresIndexAndConnectionPragmas(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX context_snapshots_project_id`); err != nil {
		t.Fatal(err)
	}
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("missing index current=%v err=%v", current, err)
	}
	if _, err := db.Exec(`CREATE INDEX context_snapshots_project_id ON context_snapshots(project,id DESC)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("foreign_keys=OFF current=%v err=%v", current, err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA recursive_triggers=OFF`); err != nil {
		t.Fatal(err)
	}
	if current, err := ContextSnapshotSchemaCurrent(db); err != nil || current {
		t.Fatalf("recursive_triggers=OFF current=%v err=%v", current, err)
	}
}

func TestSnapshotSchemaAPIsRequireSingleConnectionPool(t *testing.T) {
	unbounded, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer unbounded.Close()
	if err := EnsureContextSnapshotSchema(unbounded); err == nil {
		t.Fatal("EnsureContextSnapshotSchema accepted an unbounded connection pool")
	}

	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	if _, err := ContextSnapshotSchemaCurrent(db); err == nil {
		t.Fatal("ContextSnapshotSchemaCurrent accepted MaxOpenConns=2")
	}
	if err := ProbeContextSnapshotSchema(context.Background(), db); err == nil {
		t.Fatal("ProbeContextSnapshotSchema accepted MaxOpenConns=2")
	}
}

func TestSnapshotProbeAlwaysRollsBackOnSuccessAndFailure(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	before := allTableCounts(t, db)
	if err := ProbeContextSnapshotSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if after := allTableCounts(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("successful probe changed counts: %v -> %v", before, after)
	}

	if _, err := db.Exec(`DROP TRIGGER context_snapshot_entry_delete`); err != nil {
		t.Fatal(err)
	}
	before = allTableCounts(t, db)
	if err := ProbeContextSnapshotSchema(context.Background(), db); err == nil {
		t.Fatal("probe accepted stale schema")
	}
	if after := allTableCounts(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("failing probe changed counts: %v -> %v", before, after)
	}
}

func TestSnapshotProbeRollsBackErrorAfterInsert(t *testing.T) {
	db := openSnapshotSchemaTestDB(t)
	if err := EnsureContextSnapshotSchema(db); err != nil {
		t.Fatal(err)
	}
	before := allTableCounts(t, db)
	probeFailure := errors.New("injected failure after probe insert")
	err := probeContextSnapshotSchema(context.Background(), db, func(ctx context.Context, tx snapshotProbeExecer) error {
		if _, err := tx.ExecContext(ctx, snapshotProbeHeadInsertSQL, "__ghosttree_snapshot_probe__", "probe-injected-failure"); err != nil {
			return err
		}
		return probeFailure
	})
	if !errors.Is(err, probeFailure) {
		t.Fatalf("probe error = %v, want %v", err, probeFailure)
	}
	if after := allTableCounts(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed post-insert probe changed counts: %v -> %v", before, after)
	}
}

const snapshotHeadInsertSQL = `INSERT INTO context_snapshots(project,name,schema_version,state,content_digest,git_object_format,git_commit,git_ref,git_branch,git_dirty,git_worktree_fingerprint_version,git_worktree_fingerprint,allow_dirty_used,git_metadata_source,message,actor_id,actor_label,session_ref,created_at,entry_count,payload_bytes_total,counts_json) VALUES(?,?,1,'building',NULL,'sha1','0000000000000000000000000000000000000000',NULL,'dev',0,NULL,NULL,0,'server-verified',NULL,'actor',NULL,NULL,'2026-08-29T00:00:00Z',0,0,NULL)`

func insertBuildingSnapshot(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	result, err := db.Exec(snapshotHeadInsertSQL, "p", name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertSnapshotStatementFails(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	before := allTableCounts(t, db)
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("statement succeeded: %s", query)
	}
	if after := allTableCounts(t, db); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed statement changed table counts: %v -> %v; statement: %s", before, after, query)
	}
}

func allTableCounts(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND (name NOT LIKE 'sqlite_%' OR name='sqlite_sequence') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(tables)
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		if strings.ContainsRune(table, '"') {
			t.Fatalf("unexpected quoted table name %q", table)
		}
		var count int64
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM "%s"`, table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}
