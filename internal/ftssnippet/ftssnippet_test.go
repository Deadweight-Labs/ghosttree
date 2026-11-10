package ftssnippet

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// The snippets are the ones an FTS5 table that stores the text gives, and a
// call sees nothing of the one before.
func TestSnippetsMatchAStoredIndex(t *testing.T) {
	docs := map[int64][]string{
		7: {"the rollout of the server failed twice", "thinking about rollout"},
		9: {"nothing to see here", "quiet"},
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE ref USING fts5(a, b)`); err != nil {
		t.Fatal(err)
	}
	for id, d := range docs {
		if _, err := db.Exec(`INSERT INTO ref(rowid, a, b) VALUES(?,?,?)`, id, d[0], d[1]); err != nil {
			t.Fatal(err)
		}
	}
	var want [2]string
	if err := db.QueryRow(`SELECT snippet(ref, 0, '[', ']', '…', 4), snippet(ref, 1, '[', ']', '…', 4) FROM ref WHERE ref MATCH 'rollout'`).Scan(&want[0], &want[1]); err != nil {
		t.Fatal(err)
	}
	x := New("a", "b")
	got, err := x.Snippets(context.Background(), docs, "rollout", "[", "]", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[7][0] != want[0] || got[7][1] != want[1] {
		t.Fatalf("got %v, want %q", got, want)
	}
	again, err := x.Snippets(context.Background(), map[int64][]string{9: docs[9]}, "rollout", "[", "]", 4)
	if err != nil || len(again) != 0 {
		t.Fatalf("second call sees %v, %v", again, err)
	}
	if _, err := x.Snippets(context.Background(), map[int64][]string{1: {"one"}}, "one", "[", "]", 4); err == nil {
		t.Fatal("a document with the wrong number of values was taken")
	}
}
