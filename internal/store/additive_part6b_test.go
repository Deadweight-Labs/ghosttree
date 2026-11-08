package store

import (
	"path/filepath"
	"testing"
)

// Die Tabellen und Spalten dieses Teils kommen additiv: eine ältere Datenbank ohne
// sie bekommt sie beim Öffnen, behält ihre Zeilen, und ein zweites Öffnen ändert
// nichts mehr.
func TestPullCommitReactionAndAnswerSchemasAreAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO git_pull_requests(project,number,title,state,head_ref,base_ref,opened_at,updated_at) VALUES('github.com/a/b',3,'alt','open','feat/a','main','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// So sah die Datenbank vorher aus: ohne die neue Spalte und ohne die neuen Tabellen.
	for _, stmt := range []string{
		`ALTER TABLE git_pull_requests DROP COLUMN author`,
		`DROP TABLE git_pr_commits`, `DROP TABLE reactions`, `DROP TABLE answer_options`, `DROP TABLE quick_answers`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		var title, author string
		if err := st.DB().QueryRow(`SELECT title, author FROM git_pull_requests WHERE number=3`).Scan(&title, &author); err != nil || title != "alt" || author != "" {
			t.Fatalf("open %d: the old pull request row: %q %q %v", round, title, author, err)
		}
		for _, table := range []string{"git_pr_commits", "reactions", "answer_options", "quick_answers"} {
			var n int
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
				t.Fatalf("open %d: table %s: %v", round, table, err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
