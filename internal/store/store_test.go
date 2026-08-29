package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAddsSnapshotSchemaWithoutChangingLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO knowledge(type,title,body,project,confidence,status,origin,created_at,updated_at) VALUES('note','legacy knowledge','byte-exact body','p','trusted','active','human','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
INSERT INTO ghost_files(project,path,kind,description,content_sha,described_at,updated_at) VALUES('p','README.md','file','legacy ghost','abc','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	beforeCount, beforeDigest := legacyDomainFingerprint(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	afterCount, afterDigest := legacyDomainFingerprint(t, st.DB())
	if afterCount != beforeCount || afterDigest != beforeDigest {
		t.Fatalf("legacy domains changed: count %d -> %d, digest %s -> %s", beforeCount, afterCount, beforeDigest, afterDigest)
	}
	if current, err := ContextSnapshotSchemaCurrent(st.DB()); err != nil || !current {
		t.Fatalf("snapshot schema current=%v err=%v", current, err)
	}
}

func legacyDomainFingerprint(t *testing.T, db *sql.DB) (int, string) {
	t.Helper()
	var knowledgeTitle, knowledgeBody, ghostPath, ghostDescription string
	if err := db.QueryRow(`SELECT title,body FROM knowledge ORDER BY id LIMIT 1`).Scan(&knowledgeTitle, &knowledgeBody); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT path,description FROM ghost_files ORDER BY id LIMIT 1`).Scan(&ghostPath, &ghostDescription); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM knowledge)+(SELECT count(*) FROM ghost_files)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(knowledgeTitle + "\x00" + knowledgeBody + "\x00" + ghostPath + "\x00" + ghostDescription))
	return count, hex.EncodeToString(sum[:])
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesPrivateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	assertPrivateDatabase(t, path)
}

func TestOpenTightensExistingDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	if err := os.WriteFile(path, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	assertPrivateDatabase(t, path)
}

func TestPrepareDatabaseFilesTightensExistingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	assertPrivateDatabase(t, path)
}

func TestOpenRejectsDirectoryWithoutChangingItsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a directory as a database")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("directory mode = %04o, want 0755", got)
	}
}

func TestOpenRejectsDatabaseSymlinkWithoutChangingTargetMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ghosttree.db")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a symlink as a database")
	}
	assertMode(t, target, 0o644)
}

func TestPrepareDatabaseFilesRejectsSidecarSymlinkWithoutChangingTargetMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghosttree.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.wal")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+"-wal"); err != nil {
		t.Fatal(err)
	}
	if err := prepareDatabaseFiles(path); err == nil {
		t.Fatal("prepareDatabaseFiles accepted a symlink as a sidecar")
	}
	assertMode(t, target, 0o644)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
	}
}

func assertPrivateDatabase(t *testing.T, path string) {
	t.Helper()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(file), got)
		}
	}
}

func TestPersonRoundtrip(t *testing.T) {
	s := openTest(t)
	tok, err := s.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}
	name, ok := s.Authenticate(tok)
	if !ok || name != "alice" {
		t.Errorf("Authenticate = %q, %v", name, ok)
	}
	if _, ok := s.Authenticate("deadbeef"); ok {
		t.Error("bogus token must not authenticate")
	}
	if _, err := s.AddPerson("alice"); err == nil {
		t.Error("duplicate person must error")
	}
}

func TestTouchMachine(t *testing.T) {
	s := openTest(t)
	s.TouchMachine("workstation-a")
	s.TouchMachine("workstation-a") // idempotent, updates last_seen
}
