package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// What a restore replaces is kept, and it is kept before anything is changed:
// if a name it needs is taken, the database, its WAL and its shared memory file
// are exactly where they were, and nothing half-made is left.
func TestDBRestoreThatCannotKeepWhatItReplacesChangesNothing(t *testing.T) {
	db := seedBackupDB(t)
	bak := filepath.Join(t.TempDir(), "good.db")
	var out bytes.Buffer
	if code := cmdDB([]string{"backup", "--db", db, "--out", bak}, &out); code != 0 {
		t.Fatalf("backup: %s", out.String())
	}
	before := map[string][]byte{}
	for suffix, content := range map[string]string{"-wal": "wal of the running database", "-shm": "shared memory"} {
		os.WriteFile(db+suffix, []byte(content), 0o600)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		before[suffix], _ = os.ReadFile(db + suffix)
	}

	old := restoreNow
	restoreNow = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { restoreNow = old })
	// The name the shared memory file would be kept as is taken (by a directory,
	// so nothing can be written there).
	if err := os.Mkdir(db+".failed-20260102-030405-shm", 0o700); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := cmdDB([]string{"restore", "--db", db, "--from", bak}, &out); code == 0 {
		t.Fatalf("restore went on although it could not keep the old files:\n%s", out.String())
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		got, err := os.ReadFile(db + suffix)
		if err != nil || !bytes.Equal(got, before[suffix]) {
			t.Errorf("%s%s changed or is gone after the refused restore: %v", filepath.Base(db), suffix, err)
		}
	}
	if left, _ := filepath.Glob(db + ".failed-*"); len(left) != 1 || !strings.HasSuffix(left[0], "-shm") {
		t.Errorf("a refused restore left files behind: %v", left)
	}
	if tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(db), ".restore-*")); len(tmps) != 0 {
		t.Errorf("temporary files left: %v", tmps)
	}
	if !strings.Contains(out.String(), "nothing was changed") {
		t.Errorf("the message does not say the database is untouched:\n%s", out.String())
	}

	// The same restore with the name free works, and keeps all three files.
	os.Remove(db + ".failed-20260102-030405-shm")
	out.Reset()
	if code := cmdDB([]string{"restore", "--db", db, "--from", bak}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	for suffix, want := range before {
		kept, err := os.ReadFile(db + ".failed-20260102-030405" + suffix)
		if err != nil || !bytes.Equal(kept, want) {
			t.Errorf("the old %q file was not kept: %v", suffix, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); err == nil {
			t.Errorf("the old %s is still next to the restored database", suffix)
		}
	}
}

// The copy before an update is as large as the database, and the update makes
// a new one each time: they are kept like the nightly copies, the newest N.
func TestDBBackupKeepsOnlyTheNewestCopiesFromBeforeUpdatesToo(t *testing.T) {
	db := seedBackupDB(t)
	dir := t.TempDir()
	names := []string{
		"pre-update-0.1.0-to-0.1.1-20200101-000000.db",
		"pre-update-0.1.1-to-0.2.0-20200102-000000.db",
		"pre-update-0.2.0-to-0.2.1-20200103-000000.db",
		"pre-update-0.1.0-rc.3-to-0.1.0-20190101-000000.db", // older, with a longer name
		"pre-update-notes.db",                               // not one of ours: no time in the name
		"ghosttree-20200101-000000.db", "ghosttree-20200102-000000.db",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if code := cmdDB([]string{"backup", "--db", db, "--out", dir, "--keep", "2"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	pre, _ := filepath.Glob(filepath.Join(dir, "pre-update-*-2*.db"))
	if len(pre) != 2 || !strings.Contains(strings.Join(pre, " "), "20200103") || !strings.Contains(strings.Join(pre, " "), "20200102") {
		t.Errorf("copies from before updates left: %v\n%s", pre, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pre-update-notes.db")); err != nil {
		t.Error("a file that is no copy of ours was removed")
	}
	// The nightly copies are counted apart: the new one and the newer old one.
	nightly, _ := filepath.Glob(filepath.Join(dir, "ghosttree-*.db"))
	if len(nightly) != 2 {
		t.Errorf("nightly copies: %v", nightly)
	}
}
