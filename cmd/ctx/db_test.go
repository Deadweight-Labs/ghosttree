package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedDBMaint legt eine Datenbank mit den Snapshot-Tabellen an (an deren
// CHECK-Bedingungen die sqlite3-CLI scheitert) und lässt nach dem Löschen
// vieler Chunks freie Seiten zurück.
func seedDBMaint(t *testing.T) string {
	t.Helper()
	// Aufgelöst, weil der Befehl den Pfad ohne Symlinks meldet (macOS: /var -> /private/var).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ghosttree.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "dbmaint"})
	if err != nil {
		t.Fatal(err)
	}
	// Eine Anweisung, eine Transaktion: 200 einzelne Commits mit fsync dauern.
	if _, err := s.DB().Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<199)
		INSERT INTO session_chunks(session_id, seq, role, text, raw)
		SELECT ?, i, '', 'needle' || i, replace(hex(zeroblob(10000)), '0', 'x') FROM n`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_dirty,allow_dirty_used,git_metadata_source,actor_id,created_at)
		VALUES('p','n',1,'building','sha1',?,0,0,'server-verified','a','2026-01-01T00:00:00Z')`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size)
		VALUES(1,'knowledge','k',zeroblob(100),zeroblob(32),100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM session_chunks WHERE id % 10 != 0`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func dbDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func dbSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// withDBTestHooks setzt Platz, Wartezeit und Terminal für einen Test.
func withDBTestHooks(t *testing.T, free uint64, tty terminal) {
	t.Helper()
	oldFree, oldTimeout, oldTTY := dbFreeBytes, dbLockTimeout, dbTTY
	dbFreeBytes = func(string) (uint64, error) { return free, nil }
	dbLockTimeout = 150 * time.Millisecond
	if tty != nil {
		dbTTY = func() (terminal, error) { return tty, nil }
	}
	t.Cleanup(func() { dbFreeBytes, dbLockTimeout, dbTTY = oldFree, oldTimeout, oldTTY })
}

func TestDBVacuumShrinksAndReports(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<50, nil)
	before := dbSize(t, path)

	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes"}, &out); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, want := range []string{"database   " + path, "temp dir   " + filepath.Dir(path), "needs", "in use", "VACUUM finished in", "->", "WAL        ", "quick_check: ok", "checkpointing the WAL", "quick_check"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "optimized") {
		t.Errorf("optimized without --fts-optimize:\n%s", out.String())
	}
	if after := dbSize(t, path); after >= before/2 {
		t.Fatalf("size %d -> %d: not smaller", before, after)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("%s -> %s", humanSize(before), humanSize(dbSize(t, path)))) {
		t.Errorf("sizes before and after are not reported:\n%s", out.String())
	}
}

func TestDBVacuumFTSOptimizeIsReported(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<50, nil)
	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes", "--fts-optimize"}, &out); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, want := range []string{"fts        optimize chunks_fts", "optimizing chunks_fts", "optimizing knowledge_fts", "optimized  ", "quick_check: ok"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDBVacuumAsksBeforeItStarts(t *testing.T) {
	path := seedDBMaint(t)
	tty := &fakeTTY{answers: []string{"n"}}
	withDBTestHooks(t, 1<<50, tty)
	before := dbDigest(t, path)

	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path}, &out); code != 1 {
		t.Fatalf("a no: code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "nothing was changed") || strings.Contains(out.String(), "VACUUM finished") {
		t.Errorf("output:\n%s", out.String())
	}
	if q := tty.questions(); len(q) != 1 || !strings.Contains(q[0], "server must be stopped") {
		t.Errorf("questions = %q", q)
	}
	if dbDigest(t, path) != before {
		t.Fatal("answering no changed the database")
	}

	tty.answers = []string{"y"}
	out.Reset()
	if code := cmdDB([]string{"vacuum", "--db", path}, &out); code != 0 {
		t.Fatalf("a yes: code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "VACUUM finished") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestDBVacuumWithoutTerminalNeedsYes(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<50, nil)
	before := dbDigest(t, path)
	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path}, &out); code != 1 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Errorf("no hint to --yes:\n%s", out.String())
	}
	if dbDigest(t, path) != before {
		t.Fatal("the database changed")
	}
}

func TestDBVacuumRefusesWhileAnotherConnectionWrites(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<50, nil)
	before := dbDigest(t, path)

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(1, 99999, '', 'held', 'held')`); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes"}, &out); code != 1 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "refused:") || !strings.Contains(out.String(), "stop the server") || strings.Contains(out.String(), "VACUUM finished") {
		t.Errorf("output:\n%s", out.String())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if dbDigest(t, path) != before {
		t.Fatal("a refused vacuum changed the database")
	}
}

func TestDBVacuumRefusesWithoutEnoughSpace(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<20, nil)
	before := dbDigest(t, path)
	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes"}, &out); code != 1 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "refused: not enough free disk space") || !strings.Contains(out.String(), "--tmpdir") {
		t.Errorf("output:\n%s", out.String())
	}
	if dbDigest(t, path) != before {
		t.Fatal("a refused vacuum changed the database")
	}
}

func TestDBVacuumTempDirIsUsedAndValidated(t *testing.T) {
	path := seedDBMaint(t)
	withDBTestHooks(t, 1<<50, nil)
	tmp := t.TempDir()
	var out bytes.Buffer
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes", "--tmpdir", tmp}, &out); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "temp dir   "+tmp) {
		t.Errorf("temp dir not reported:\n%s", out.String())
	}
	out.Reset()
	if code := cmdDB([]string{"vacuum", "--db", path, "--yes", "--tmpdir", filepath.Join(tmp, "missing")}, &out); code != 1 {
		t.Fatalf("missing temp dir: code %d:\n%s", code, out.String())
	}
}

func TestDBUsage(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"nope"}, 2},
		{[]string{"help"}, 0},
		{[]string{"vacuum"}, 2},
		{[]string{"vacuum", "--db", "x", "extra"}, 2},
		{[]string{"vacuum", "--bogus"}, 2},
		{[]string{"check"}, 2},
		{[]string{"check", "--db", "x", "extra"}, 2},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if code := cmdDB(c.args, &out); code != c.code {
			t.Errorf("ctx db %v: code %d, want %d:\n%s", c.args, code, c.code, out.String())
		}
		if !strings.Contains(out.String(), "usage: ctx db ") {
			t.Errorf("ctx db %v prints no usage:\n%s", c.args, out.String())
		}
	}
	var out bytes.Buffer
	if code := run([]string{"db"}, &out); code != 2 || !strings.Contains(out.String(), "ctx db check") {
		t.Errorf("ctx db: code %d:\n%s", code, out.String())
	}
	out.Reset()
	if run([]string{"help"}, &out); !strings.Contains(out.String(), "  db ") {
		t.Errorf("the top-level usage does not list db:\n%s", out.String())
	}
}

func TestDBCheckOKFindingsAndDamage(t *testing.T) {
	path := seedDBMaint(t)
	for _, args := range [][]string{{"check", "--db", path}, {"check", "--db", path, "--full"}} {
		var out bytes.Buffer
		if code := cmdDB(args, &out); code != 0 || strings.TrimSpace(out.String()) != "ok" {
			t.Fatalf("ctx db %v: code %d:\n%s", args, code, out.String())
		}
	}

	// Eine Zeile gegen die CHECK-Bedingung mit ghosttree_valid_utf8.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size)
		VALUES(1,'knowledge',CAST(x'ff80' AS TEXT),zeroblob(10),zeroblob(32),10)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"check", "--db", path}, {"check", "--db", path, "--full"}} {
		var out bytes.Buffer
		if code := cmdDB(args, &out); code != 1 {
			t.Fatalf("ctx db %v: code %d, want 1:\n%s", args, code, out.String())
		}
		if !strings.Contains(out.String(), "CHECK constraint failed in context_snapshot_entries") || strings.HasPrefix(out.String(), "ok") {
			t.Errorf("ctx db %v:\n%s", args, out.String())
		}
	}

	// Eine zerstörte Seite: die erste Tabellenwurzel.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xff}, 4096), 4096); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var out bytes.Buffer
	if code := cmdDB([]string{"check", "--db", path}, &out); code != 1 || strings.HasPrefix(out.String(), "ok") {
		t.Fatalf("damaged file: code %d:\n%s", code, out.String())
	}

	out.Reset()
	if code := cmdDB([]string{"check", "--db", filepath.Join(t.TempDir(), "missing.db")}, &out); code != 1 || !strings.Contains(out.String(), "failed") {
		t.Fatalf("missing file: code %d:\n%s", code, out.String())
	}
}

func TestDBCheckRunsBesideAWriter(t *testing.T) {
	path := seedDBMaint(t)
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(1, 99999, '', 'held', 'held')`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdDB([]string{"check", "--db", path}, &out); code != 0 || strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
}

func TestStripImagesHintPointsToCtxDBVacuum(t *testing.T) {
	if strings.Contains(stripImagesUsage, "sqlite3 <db> VACUUM") || !strings.Contains(stripImagesUsage, "ctx db vacuum") {
		t.Errorf("strip-images help:\n%s", stripImagesUsage)
	}
	path, _ := seedStripImagesDB(t)
	var out bytes.Buffer
	if code := cmdStripImages([]string{"--db", path, "--sleep", "0s"}, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), "sqlite3") || !strings.Contains(out.String(), "ctx db vacuum --db") {
		t.Errorf("strip-images output:\n%s", out.String())
	}
}
