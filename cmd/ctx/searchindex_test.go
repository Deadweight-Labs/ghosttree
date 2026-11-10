package main

import (
	"database/sql"
	"testing"
)

// keepChunksFTS gives a test database the first search index over sessions as
// the versions before REQ-622 criterion 4 kept it: the trigger chunks_ai and
// chunks_fts over session_chunks.text, as on a server that has not run ctx db
// slim-search yet.
func keepChunksFTS(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON session_chunks BEGIN
  INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END`,
		`INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// checkSearchIndexes checks the structure of the session indexes and chunks_fts
// against its content.
func checkSearchIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO chunks_fts(chunks_fts, rank) VALUES('integrity-check', 1)`,
		`INSERT INTO sess_fts(sess_fts) VALUES('integrity-check')`,
		`INSERT INTO chunk_fts(chunk_fts) VALUES('integrity-check')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}
