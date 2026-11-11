package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func schemaOf(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT type||' '||name, COALESCE(sql,'') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSeededStoreMatchesFreshMigration holds the promise of testtemplate.go:
// a store opened from the template has exactly the schema of one migrated from
// an empty file.
func TestSeededStoreMatchesFreshMigration(t *testing.T) {
	dir := t.TempDir()
	seeded := filepath.Join(dir, "seeded.db")
	st, err := Open(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.db")
	st, err = openWithOptions(fresh, OpenOptions{MaxOpenConns: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if want, got := schemaOf(t, fresh), schemaOf(t, seeded); !reflect.DeepEqual(want, got) {
		t.Fatalf("seeded schema differs from a fresh migration: %d objects against %d", len(got), len(want))
	}
}

// A path that exists already is a database of the test's own making and is
// never overwritten by the template.
func TestTestTemplateLeavesExistingFilesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mine.db")
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedFromTestTemplate(path)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "not a database" {
		t.Fatalf("existing file changed: %q, %v", got, err)
	}
}

// The in-memory variant must also end up with the schema of a fresh migration,
// and two memory stores must not share anything.
func TestSeededMemoryStoreMatchesFreshMigration(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.db")
	st, err := openWithOptions(fresh, OpenOptions{MaxOpenConns: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	want := schemaOf(t, fresh)

	a, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.AddAccount("only-in-a", "", true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.db.QueryRow(`SELECT count(*) FROM persons WHERE name='only-in-a'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("first memory store lost its own account: n=%d err=%v", n, err)
	}
	if err := b.db.QueryRow(`SELECT count(*) FROM persons WHERE name='only-in-a'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("second memory store sees the first one's account: n=%d err=%v", n, err)
	}
	rows, err := a.db.Query(`SELECT type||' '||name, COALESCE(sql,'') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		got[k] = v
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("seeded memory schema differs from a fresh migration: %d objects against %d", len(got), len(want))
	}
}
