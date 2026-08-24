package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// oldSchema is the schema as it shipped before trust tiers, kept verbatim so
// the upgrade is tested against what actually exists on disk.
const oldSchema = `
CREATE TABLE knowledge(
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan')),
  title TEXT NOT NULL, body TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  confidence TEXT NOT NULL DEFAULT 'observation' CHECK(confidence IN ('observation','verified')),
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated')),
  person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE VIRTUAL TABLE knowledge_fts USING fts5(title, body, content='knowledge', content_rowid='id');
CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge BEGIN
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
  INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TABLE sessions(
  id INTEGER PRIMARY KEY, harness TEXT NOT NULL, external_id TEXT NOT NULL,
  project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
  UNIQUE(harness, external_id));
`

func makeOldDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	// Ids 1 and 5, not 1 and 2: a real database has gaps from deleted rows,
	// and a rebuild that renumbers them would point the search at the wrong
	// entry rather than at no entry — which is the failure that hides.
	if _, err := db.Exec(`INSERT INTO knowledge(id, type, title, body, project, confidence, status, created_at, updated_at)
		VALUES(1,'pitfall','ufw drops lan traffic','ssh only via private network','github.com/x/y','observation','active','2026-08-01T00:00:00Z','2026-08-01T00:00:00Z'),
		      (5,'decision','sqlite over postgres','single writer is enough','github.com/x/y','verified','active','2026-08-02T00:00:00Z','2026-08-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return path
}

func openLegacyRequestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF; PRAGMA legacy_alter_table=ON;
		DROP TRIGGER knowledge_ai; DROP TRIGGER knowledge_au;
		ALTER TABLE knowledge RENAME TO knowledge_target;
		CREATE TABLE knowledge(
		 id INTEGER PRIMARY KEY,
		 type TEXT NOT NULL CHECK(type IN ('pitfall','decision','note','plan','instruction','request')),
		 title TEXT NOT NULL, body TEXT NOT NULL,
		 project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
		 confidence TEXT NOT NULL DEFAULT 'trusted' CHECK(confidence IN ('quarantined','staged','trusted','verified')),
		 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','stale','deprecated','superseded','archived')),
		 origin TEXT NOT NULL DEFAULT 'agent' CHECK(origin IN ('agent','distilled','human')),
		 superseded_by INTEGER NOT NULL DEFAULT 0,
		 person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
		 created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		DROP TABLE knowledge_target;
		CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge BEGIN
		 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END;
		CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
		 INSERT INTO knowledge_fts(knowledge_fts,rowid,title,body) VALUES('delete',old.id,old.title,old.body);
		 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func setLegacyRequestState(t *testing.T, s *Store, id int64, state, kind, ref, person string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO request_resolution(knowledge_id,state,evidence_kind,evidence_ref,by_person,at) VALUES(?,?,?,?,?,?)`, id, state, kind, ref, person, now()); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradePreservesDataAndSearch(t *testing.T) {
	path := makeOldDB(t)
	if _, err := UpgradeSchema(path); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	k, err := s.KnowledgeByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if k.Confidence != "trusted" {
		t.Errorf("observation should map to trusted, got %q", k.Confidence)
	}
	if k.Origin != "agent" {
		t.Errorf("origin = %q, want agent", k.Origin)
	}
	if k2, _ := s.KnowledgeByID(5); k2.Confidence != "verified" {
		t.Errorf("verified must survive unchanged, got %q", k2.Confidence)
	}

	// Each term must find its own entry. Checking the id, not just the hit
	// count, is what catches an index whose rowids drifted against the table:
	// that failure returns a confident wrong answer, not an empty one.
	for term, wantID := range map[string]int64{"private network": 1, "postgres": 5} {
		hits, err := s.SearchKnowledge(term, scope.Axes{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ID != wantID {
			t.Errorf("search %q returned %+v, want exactly id %d", term, hits, wantID)
		}
	}
}

func TestUpgradeIsIdempotent(t *testing.T) {
	path := makeOldDB(t)
	if _, err := UpgradeSchema(path); err != nil {
		t.Fatal(err)
	}
	backup, err := UpgradeSchema(path)
	if err != nil {
		t.Fatalf("second run must be a no-op, got %v", err)
	}
	if backup != "" {
		t.Errorf("a no-op run must not write a backup, got %q", backup)
	}
	s, _ := Open(path)
	defer s.Close()
	ks, _ := s.KnowledgeForContext(scope.Axes{Project: "github.com/x/y"})
	if len(ks) != 2 {
		t.Errorf("entries after two upgrade runs = %d, want 2", len(ks))
	}
}

func TestSchemaCurrentDetectsOldDatabase(t *testing.T) {
	path := makeOldDB(t)
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	current, err := SchemaCurrent(db)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Error("an old database must not report a current schema")
	}
}

func TestUpgradeTypesKeepsDataAndSearch(t *testing.T) {
	path := makeOldDB(t)
	if _, err := UpgradeSchema(path); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeTypes(path); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.InsertKnowledge(Knowledge{Type: "instruction", Title: "build", Body: "make test"}); err != nil {
		t.Errorf("instruction must be allowed after upgrade: %v", err)
	}
	for term, wantID := range map[string]int64{"private network": 1, "postgres": 5} {
		hits, err := s.SearchKnowledge(term, scope.Axes{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ID != wantID {
			t.Errorf("search %q returned %+v, want exactly id %d", term, hits, wantID)
		}
	}
}

func TestUpgradeTypesCreatesInstructionActivationTables(t *testing.T) {
	path := makeOldDB(t)
	if _, err := UpgradeSchema(path); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeTypes(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"instruction_activation_path", "instruction_activation_task"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("missing %s after upgrade: %v", table, err)
		}
	}
}

func TestUpgradeRequestDomainMovesLegacyRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.db")
	s := openLegacyRequestStore(t, path)
	openID, err := s.InsertKnowledge(Knowledge{Type: "request", Title: "open feature", Body: "needs a ledger", Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	setLegacyRequestState(t, s, openID, "open", "", "", "robin")
	sessionID, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "legacy-proof"})
	if err := s.AddEvidence(openID, []Evidence{{SessionID: sessionID, ChunkSeq: 7, Quote: "requested in session"}}); err != nil {
		t.Fatal(err)
	}
	doneID, err := s.InsertKnowledge(Knowledge{Type: "request", Title: "done feature", Body: "already delivered", Scope: scope.Axes{Project: "github.com/x/y"}})
	if err != nil {
		t.Fatal(err)
	}
	setLegacyRequestState(t, s, doneID, "done", "commit", "abc123", "robin")
	if _, err := s.InsertKnowledge(Knowledge{Type: "decision", Title: "keep me", Body: "knowledge remains"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	backup, err := UpgradeRequestDomain(path)
	if err != nil {
		t.Fatal(err)
	}
	backupDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	if current, err := SchemaHasNewTypes(backupDB); err != nil || current {
		t.Fatalf("backup is not the legacy pre-upgrade schema: current=%v err=%v", current, err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for id, state := range map[int64]string{openID: "open", doneID: "done"} {
		detail, err := s.RequestByID(id)
		if err != nil {
			t.Fatalf("request %d: %v", id, err)
		}
		if detail.Request.State != state {
			t.Errorf("request %d state = %q, want %q", id, detail.Request.State, state)
		}
	}
	var legacy int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM knowledge WHERE type='request'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 {
		t.Fatalf("legacy request rows = %d", legacy)
	}
	var evidenceRef string
	if err := s.DB().QueryRow(`SELECT ref FROM request_evidence WHERE request_id=? AND kind='session'`, openID).Scan(&evidenceRef); err != nil {
		t.Fatal(err)
	}
	if evidenceRef != "session:1#chunk:7" {
		t.Fatalf("migrated evidence ref = %q", evidenceRef)
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "request", Title: "must use ledger", Body: "body"}); err == nil {
		t.Fatal("knowledge write path still accepts requests after domain upgrade")
	}
	if current, err := SchemaHasNewTypes(s.DB()); err != nil || !current {
		t.Fatalf("separated request domain reported outdated: current=%v err=%v", current, err)
	}
	if hits, err := s.SearchRequests(requestdomain.SearchFilter{Query: "ledger", Limit: 10}); err != nil || len(hits.Results) != 1 {
		t.Fatalf("request search = %+v, %v", hits, err)
	}
	if hits, err := s.SearchKnowledge("remains", scope.Axes{}, 10); err != nil || len(hits) != 1 {
		t.Fatalf("knowledge search after request migration = %+v, %v", hits, err)
	}
}

func TestUpgradeRequestDomainPreservesMigrationEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.db")
	s := openLegacyRequestStore(t, path)
	runID, err := s.BeginMigration("github.com/x/y", map[string]string{"AGENTS.md": "digest"})
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := s.InsertKnowledge(Knowledge{Type: "request", Title: "migrated request", Body: "body", Scope: scope.Axes{Project: "github.com/x/y"}, SessionRef: "AGENTS.md"})
	if err != nil {
		t.Fatal(err)
	}
	setLegacyRequestState(t, s, legacyID, "open", "", "", "")
	if _, err := s.db.Exec(`INSERT INTO migration_evidence(knowledge_id,run_id,source,digest,item_key,quote) VALUES(?,?,?,?,?,?)`, legacyID, runID, "AGENTS.md", "digest", "item-1", ""); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := UpgradeRequestDomain(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var requestID int64
	if err := db.QueryRow(`SELECT request_id FROM migration_evidence WHERE item_key='item-1'`).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if requestID != legacyID {
		t.Fatalf("migration evidence request = %d, want %d", requestID, legacyID)
	}
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CompleteMigration(runID); err != nil {
		t.Fatalf("converted request evidence no longer completes migration: %v", err)
	}
}

func TestUpgradeRequestDomainUpdatesEvidenceSchemaWithoutRequests(t *testing.T) {
	path := makeOldDB(t)
	if _, err := UpgradeSchema(path); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeTypes(path); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeRequestDomain(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA table_info(migration_evidence)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		found = found || name == "request_id"
	}
	if !found {
		t.Fatal("migration_evidence.request_id missing after upgrade")
	}
}

// The production database predates the usage columns, and it is 780 MB: the
// upgrade has to be additive and idempotent, not a table rebuild.
func TestUpgradeUsageTelemetryIsAdditiveAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The knowledge table as it stood before this change, with a row in it.
	if _, err := db.Exec(`CREATE TABLE knowledge(
		id INTEGER PRIMARY KEY, type TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
		project TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', machine TEXT NOT NULL DEFAULT '',
		confidence TEXT NOT NULL DEFAULT 'trusted', status TEXT NOT NULL DEFAULT 'active',
		origin TEXT NOT NULL DEFAULT 'agent', superseded_by INTEGER NOT NULL DEFAULT 0,
		person TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO knowledge(type,title,body,created_at,updated_at)
		  VALUES('note','kept','body','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	backup, err := UpgradeUsageTelemetry(path)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("upgrade reported nothing to do on a legacy table")
	}
	// Running it again must be a no-op, not a second backup.
	again, err := UpgradeUsageTelemetry(path)
	if err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Fatalf("second upgrade wrote backup %q, want a no-op", again)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hits, lastUsed, err := s.KnowledgeUsage(1)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || lastUsed != "" {
		t.Fatalf("existing row got usage %d/%q, want the zero defaults", hits, lastUsed)
	}
	var title string
	if err := s.db.QueryRow(`SELECT title FROM knowledge WHERE id=1`).Scan(&title); err != nil || title != "kept" {
		t.Fatalf("existing row lost: title=%q err=%v", title, err)
	}
}
