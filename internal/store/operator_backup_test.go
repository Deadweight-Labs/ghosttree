package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Security review 13: the operator's backup is the file as it was (no schema
// step of Open ran on it), and a database whose write lock another connection
// holds is in use.
func TestOperatorBackupIsTakenBeforeOpenAndInUseIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE only_this(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureNotInUse(path); err != nil {
		t.Fatalf("idle database in use: %v", err)
	}
	backup := filepath.Join(dir, "before.db")
	if err := BackupDatabaseFile(path, backup); err != nil {
		t.Fatal(err)
	}
	copyDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var n int
	if err := copyDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='orgs'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the backup ran the store's migrations (%d, %v)", n, err)
	}
	if err := BackupDatabaseFile(path, backup); err == nil {
		t.Fatal("an existing backup was overwritten")
	}
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO only_this VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureNotInUse(path); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("a held write lock: %v", err)
	}
}

// Processes of other users whose open files cannot be listed could be the
// server: refused, with the hint to run as the service user or root.
func TestUnreadableProcessesCountAsInUse(t *testing.T) {
	err := ensureNotInUse(filepath.Join(t.TempDir(), "x.db"), HolderScan{Supported: true, Unreadable: 2, Suspect: 1})
	if !errors.Is(err, ErrDatabaseInUse) || !strings.Contains(err.Error(), "service user or root") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnlyProcessesThatCouldBeTheServerCount(t *testing.T) {
	if err := ensureNotInUse(filepath.Join(t.TempDir(), "x.db"), HolderScan{Supported: true, Unreadable: 40}); err != nil {
		t.Fatalf("unrelated processes of other users refused: %v", err)
	}
	for cmd, want := range map[string]bool{"/usr/local/bin/ctx serve --db x": true, "ctx": true, "/opt/ghosttree/bin/server": true, "/usr/bin/sshd -D": false, "ctxd": false} {
		if got := mayBeServer(cmd); got != want {
			t.Errorf("mayBeServer(%q) = %v", cmd, got)
		}
	}
}
