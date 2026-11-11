package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func knowledgeTriggerSQL(t *testing.T, s *Store) string {
	t.Helper()
	var sql string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='knowledge_au'`).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	return sql
}

// Counting a use must not rewrite the full-text row (PAIN-76), and an edit of the
// words must still reach it.
func TestKnowledgeIndexIsRewrittenOnlyWhenItsColumnsChange(t *testing.T) {
	s := openTest(t)
	if sql := knowledgeTriggerSQL(t, s); !strings.Contains(sql, "UPDATE OF title, body") {
		t.Fatalf("trigger = %s", sql)
	}
	id := addEntry(t, s, Knowledge{Title: "Quarkspeise ordentlich", Body: "Rezept"})
	if _, err := s.db.Exec(`UPDATE knowledge SET hit_count = hit_count + 5, last_used_at = 'x' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	found := func(word string) int {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM knowledge_fts WHERE knowledge_fts MATCH ?`, `"`+word+`"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if found("quarkspeise") != 1 {
		t.Fatal("the entry left the index when only its use was counted")
	}
	if _, err := s.db.Exec(`UPDATE knowledge SET title = 'Pudding ordentlich' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if found("quarkspeise") != 0 || found("pudding") != 1 {
		t.Fatal("an edit of the title did not reach the index")
	}
}

// A database made with the old trigger gets the narrow one once, and keeps its
// index.
func TestOldKnowledgeTriggerIsReplacedOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := addEntry(t, s, Knowledge{Title: "Quarkspeise ordentlich", Body: "Rezept"})
	if _, err := s.db.Exec(`DROP TRIGGER knowledge_au; CREATE TRIGGER knowledge_au AFTER UPDATE ON knowledge BEGIN
		INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES('delete', old.id, old.title, old.body);
		INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
	END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if sql := knowledgeTriggerSQL(t, s); !strings.Contains(sql, "UPDATE OF title, body") {
		t.Fatalf("trigger after open = %s", sql)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM knowledge_fts WHERE knowledge_fts MATCH '"quarkspeise"' AND rowid = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the index changed under the replacement: %d, %v", n, err)
	}
}
