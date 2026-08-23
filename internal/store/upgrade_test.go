package store

import (
	"database/sql"
	"path/filepath"
	"testing"

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
