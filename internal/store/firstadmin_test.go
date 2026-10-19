package store

import (
	"database/sql"
	"testing"
)

func TestFirstPersonIsAdminAtOnceAndRevocationSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/first.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPerson("bob"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"alice": true, "bob": false} {
		acc, err := st.AccountByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if acc.Admin != want {
			t.Errorf("%s admin = %v before any reopen, want %v", name, acc.Admin, want)
		}
	}
	st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE persons SET is_admin=0 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acc, err := st.AccountByName("alice")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Admin {
		t.Error("reopening the store gave the revoked admin back")
	}
}
