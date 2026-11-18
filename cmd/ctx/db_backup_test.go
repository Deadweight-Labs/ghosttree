package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func seedBackupDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("alex", "", true); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDBBackupWritesAVerifiedPrivateCopyIntoADirectory(t *testing.T) {
	db := seedBackupDB(t)
	dir := filepath.Join(t.TempDir(), "backups")
	var out bytes.Buffer
	if code := cmdDB([]string{"backup", "--db", db, "--out", dir}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	files, _ := filepath.Glob(filepath.Join(dir, "ghosttree-*.db"))
	if len(files) != 1 {
		t.Fatalf("copies: %v\n%s", files, out.String())
	}
	if fi, _ := os.Stat(files[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("the copy is %v, want 0600", fi.Mode().Perm())
	}
	// The copy is a database of its own with the account in it.
	copyDB, err := store.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	accounts, err := copyDB.ListAccounts()
	if err != nil || len(accounts) != 1 || accounts[0].Name != "alex" {
		t.Fatalf("accounts in the copy: %v %v", accounts, err)
	}
}

func TestDBBackupKeepsOnlyTheNewestCopiesAndNeverOverwrites(t *testing.T) {
	db := seedBackupDB(t)
	dir := t.TempDir()
	for _, old := range []string{"ghosttree-20200101-000000.db", "ghosttree-20200102-000000.db", "ghosttree-20200103-000000.db"} {
		if err := os.WriteFile(filepath.Join(dir, old), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Other files in the directory are not ours to remove.
	if err := os.WriteFile(filepath.Join(dir, "notes.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDB([]string{"backup", "--db", db, "--out", dir, "--keep", "2"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	left, _ := filepath.Glob(filepath.Join(dir, "ghosttree-*.db"))
	joined := strings.Join(left, " ")
	if len(left) != 2 || !strings.Contains(joined, "20200103") || strings.Contains(joined, "20200101") || strings.Contains(joined, "20200102") {
		t.Fatalf("left: %v\n%s", left, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.db")); err != nil {
		t.Error("an unrelated file was removed")
	}

	file := filepath.Join(t.TempDir(), "one.db")
	for i, want := range []int{0, 1} {
		out.Reset()
		if code := cmdDB([]string{"backup", "--db", db, "--out", file}, &out); code != want {
			t.Fatalf("run %d: exit %d, want %d: %s", i, code, want, out.String())
		}
	}
	if !strings.Contains(out.String(), "never overwrites") {
		t.Errorf("second run: %s", out.String())
	}
}

func TestDBBackupRefusesWhatItCannotDo(t *testing.T) {
	db := seedBackupDB(t)
	for name, args := range map[string][]string{
		"no database":      {"backup", "--db", filepath.Join(t.TempDir(), "none.db"), "--out", t.TempDir()},
		"no target":        {"backup", "--db", db},
		"keep with a file": {"backup", "--db", db, "--out", filepath.Join(t.TempDir(), "x.db"), "--keep", "2"},
	} {
		var out bytes.Buffer
		if code := cmdDB(args, &out); code == 0 {
			t.Errorf("%s: accepted: %s", name, out.String())
		}
	}
}

func TestDBRestorePutsABackupBackAndKeepsWhatItReplaced(t *testing.T) {
	db := seedBackupDB(t)
	bak := filepath.Join(t.TempDir(), "good.db")
	var out bytes.Buffer
	if code := cmdDB([]string{"backup", "--db", db, "--out", bak}, &out); code != 0 {
		t.Fatalf("backup: %s", out.String())
	}
	// The database changes after the backup, and leaves a WAL behind.
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAccount("later", "", false); err != nil {
		t.Fatal(err)
	}
	st.Close()
	os.WriteFile(db+"-wal", []byte("stale"), 0o600)

	out.Reset()
	if code := cmdDB([]string{"restore", "--db", db, "--from", bak}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	back, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := back.ListAccounts()
	back.Close()
	if len(accounts) != 1 || accounts[0].Name != "alex" {
		t.Fatalf("accounts after restore: %v", accounts)
	}
	kept, _ := filepath.Glob(db + ".failed-*")
	if len(kept) < 2 { // the database and its stale WAL
		t.Errorf("what was replaced was not kept: %v", kept)
	}
	if _, err := os.Stat(db + "-wal"); err == nil {
		t.Error("the old WAL is still next to the restored database")
	}
}

func TestDBRestoreRefusesABackupThatIsNotOne(t *testing.T) {
	db := seedBackupDB(t)
	before, _ := os.ReadFile(db)
	junk := filepath.Join(t.TempDir(), "junk.db")
	os.WriteFile(junk, []byte("this is not a database at all"), 0o600)
	var out bytes.Buffer
	for name, args := range map[string][]string{
		"junk":    {"restore", "--db", db, "--from", junk},
		"missing": {"restore", "--db", db, "--from", filepath.Join(t.TempDir(), "none.db")},
		"no from": {"restore", "--db", db},
	} {
		out.Reset()
		if code := cmdDB(args, &out); code == 0 {
			t.Errorf("%s: accepted: %s", name, out.String())
		}
	}
	after, _ := os.ReadFile(db)
	if !bytes.Equal(before, after) {
		t.Error("a refused restore changed the database")
	}
}
