package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EnsureNotInUse refuses while another process has the database file (or its
// -wal / -shm) open, or holds its write lock: an operator command that changes
// data runs with the server stopped. A process of another user whose open
// files cannot be listed refuses too when it could be the server (it runs as
// the database file's owner, or its command line names ctx or ghosttree);
// where processes cannot be listed at all (no /proc) only the lock is checked.
func EnsureNotInUse(path string) error {
	abs, err := databaseFilePath(path)
	if err != nil {
		return err
	}
	return ensureNotInUse(abs, scanDatabaseHolders(abs))
}

func ensureNotInUse(abs string, scan HolderScan) error {
	if len(scan.Holders) > 0 {
		names := make([]string, len(scan.Holders))
		for i, h := range scan.Holders {
			names[i] = h.String()
		}
		return fmt.Errorf("%w: %s has it open; stop the server first", ErrDatabaseInUse, strings.Join(names, ", "))
	}
	if scan.Suspect > 0 {
		return fmt.Errorf("%w: the open files of %d processes that could be the server cannot be checked; run as the service user or root", ErrDatabaseInUse, scan.Suspect)
	}
	db, err := sql.Open("sqlite", fileURI(abs, "_pragma=busy_timeout(1000)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		if isBusy(err) {
			return fmt.Errorf("%w: another connection holds a lock on the database (waited %s); stop the server first", ErrDatabaseInUse, time.Second)
		}
		return err
	}
	_, err = conn.ExecContext(ctx, "ROLLBACK")
	return err
}

// BackupDatabaseFile writes a verified copy of the database file as it is,
// before any Open runs its migrations on it, over a read-only connection: the
// database is only read (the dry run copies a live server's database this
// way). The backup must not exist.
func BackupDatabaseFile(path, backup string) error {
	abs, err := databaseFilePath(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", fileURI(abs, "mode=ro&_pragma=busy_timeout(3000)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return createVerifiedBackup(db, backup)
}

// mayBeServer: the command line names the ghosttree binary.
func mayBeServer(cmd string) bool {
	first, _, _ := strings.Cut(cmd, " ")
	base := first[strings.LastIndexByte(first, '/')+1:]
	return base == "ctx" || strings.Contains(strings.ToLower(cmd), "ghosttree")
}
