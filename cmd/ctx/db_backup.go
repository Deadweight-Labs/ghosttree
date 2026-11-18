package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const dbBackupUsage = `usage: ctx db backup --db <path> --out <file|directory> [--keep N]

Write a verified copy of the database (VACUUM INTO, then an integrity check of
the copy). It may run beside the server: the copy is one consistent state.
--out names a file (a path ending in .db) or a directory, which is made if it
is missing; the copy is then called ghosttree-<UTC time>.db there. --keep N (directory only) removes all but the
newest N copies of that name afterwards, so a daily timer does not fill the disk.
The copy is readable by its owner only. Restore: stop the server and put the
copy where the database was (with its -wal and -shm files removed).`

// cmdDBBackup is what the server's backup timer runs (ctx server install) and
// what ctx server update takes before it changes anything.
func cmdDBBackup(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db backup", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	out := fs.String("out", "", "file or directory for the copy")
	keep := fs.Int("keep", 0, "with a directory: keep only the newest N copies (0 keeps all)")
	fs.Usage = func() { fmt.Fprintln(stdout, dbBackupUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *out == "" || fs.NArg() != 0 || *keep < 0 {
		fmt.Fprintln(stdout, dbBackupUsage)
		return 2
	}
	if _, err := os.Stat(*dbPath); err != nil {
		fmt.Fprintf(stdout, "database: %v\n", err)
		return 1
	}
	target, dir := *out, ""
	// A path ending in .db is a file; anything else is a directory, made if needed.
	if fi, err := os.Stat(*out); (err == nil && fi.IsDir()) || !strings.HasSuffix(*out, ".db") {
		dir = strings.TrimRight(*out, "/")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(stdout, "backup directory: %v\n", err)
			return 1
		}
		target = filepath.Join(dir, backupName(time.Now().UTC()))
	} else if *keep > 0 {
		fmt.Fprintln(stdout, "--keep needs --out to be a directory")
		return 2
	}
	if _, err := os.Lstat(target); err == nil {
		fmt.Fprintf(stdout, "%s exists already; a backup never overwrites one\n", target)
		return 1
	}
	// A copy of everything the server knows: 0600 from the first byte, not
	// after SQLite has written it.
	prev := syscall.Umask(0o077)
	defer syscall.Umask(prev)

	s, err := store.OpenWithOptions(*dbPath, store.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer s.Close()
	if err := s.Backup(target); err != nil {
		os.Remove(target)
		fmt.Fprintf(stdout, "backup failed: %v\n", err)
		return 1
	}
	fi, _ := os.Stat(target)
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	fmt.Fprintf(stdout, "backup %s (%d bytes, verified)\n", target, size)
	if dir != "" && *keep > 0 {
		for _, gone := range pruneBackups(dir, *keep) {
			fmt.Fprintf(stdout, "removed old backup %s\n", gone)
		}
	}
	return 0
}

func backupName(t time.Time) string { return "ghosttree-" + t.Format("20060102-150405") + ".db" }

// preUpdateRE matches the copy ctx server update makes before it changes
// anything: pre-update-<old>-to-<new>-<UTC time>.db.
var preUpdateRE = regexp.MustCompile(`^pre-update-.+-(\d{8}-\d{6})\.db$`)

// pruneBackups removes all but the newest keep files of each kind from dir and
// returns what it removed: the nightly copies (named like backupName's results,
// which sort by time) and the copies from before an update (pre-update-*,
// ordered by the time in their name). Only regular files with those names are
// touched.
func pruneBackups(dir string, keep int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type file struct{ name, key string }
	var nightly, before []file
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if strings.HasPrefix(n, "ghosttree-") && strings.HasSuffix(n, ".db") && len(n) == len(backupName(time.Time{})) {
			nightly = append(nightly, file{n, n})
		} else if m := preUpdateRE.FindStringSubmatch(n); m != nil {
			before = append(before, file{n, m[1] + n})
		}
	}
	var removed []string
	for _, group := range [][]file{nightly, before} {
		sort.Slice(group, func(i, j int) bool { return group[i].key < group[j].key })
		for i := 0; i < len(group)-keep; i++ {
			if os.Remove(filepath.Join(dir, group[i].name)) == nil {
				removed = append(removed, group[i].name)
			}
		}
	}
	return removed
}

const dbRestoreUsage = `usage: ctx db restore --db <path> --from <backup>

Put a copy made by "ctx db backup" in place of the database. Stop the server
first. The copy is checked before anything changes; the database it replaces
(and its -wal and -shm files) stays next to it as <path>.failed-<UTC time>, so a
restore can be taken back. Run it as the user that owns the database (see
"Running maintenance commands" in deploy/README.md).`

// restoreNow is the clock of a restore, which stamps what it keeps. Tests set it.
var restoreNow = time.Now

// swapIn puts the file repl (written, synced, in the database's directory) in
// place of the database at db, and keeps what it replaces as
// <db>.failed-<stamp>[-wal|-shm]. Everything that can fail is done before the
// first change, so a failure leaves the database exactly as it was:
//
//  1. the kept names are made as hard links of the old files (all or none);
//  2. the old -shm and -wal go: they belong to the database being replaced, and
//     a WAL replayed into another database corrupts it;
//  3. repl is renamed over db, which is atomic: there is no moment without a
//     database.
//
// The directory is synced after each step, so a crash cannot leave the names in
// another order than the one that was made.
func swapIn(db, repl, stamp string) error {
	dir := filepath.Dir(db)
	type kept struct{ old, aside string }
	var all []kept
	undo := func() {
		for _, k := range all {
			os.Remove(k.aside)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		old := db + suffix
		if _, err := os.Lstat(old); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			undo()
			return err
		}
		aside := db + ".failed-" + stamp + suffix
		if err := linkOrCopy(old, aside); err != nil {
			undo()
			return fmt.Errorf("cannot keep %s as %s, nothing was changed: %w", old, aside, err)
		}
		all = append(all, kept{old, aside})
	}
	syncDir(dir)
	for _, suffix := range []string{"-shm", "-wal"} {
		if err := os.Remove(db + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot remove %s%s: %w (the database was not replaced)", filepath.Base(db), suffix, err)
		}
	}
	syncDir(dir)
	if err := os.Rename(repl, db); err != nil {
		// Back to what there was: the WAL goes back next to its database.
		for _, k := range all {
			if k.old != db {
				os.Link(k.aside, k.old)
			}
		}
		return fmt.Errorf("%w (the database was not replaced)", err)
	}
	syncDir(dir)
	return nil
}

// linkOrCopy makes dst a second name of src, or, where the file system does not
// link, a copy. It never replaces a file that exists.
func linkOrCopy(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil || errors.Is(err, os.ErrExist) {
		return err
	}
	in, oerr := os.Open(src)
	if oerr != nil {
		return oerr
	}
	defer in.Close()
	out, oerr := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if oerr != nil {
		return oerr
	}
	if _, cerr := io.Copy(out, in); cerr != nil {
		out.Close()
		os.Remove(dst)
		return cerr
	}
	if cerr := out.Sync(); cerr != nil {
		out.Close()
		os.Remove(dst)
		return cerr
	}
	return out.Close()
}

// syncDir makes the names in dir durable. A file system that cannot sync a
// directory has nothing to offer here, so the error is not one.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// cmdDBRestore is what ctx server update uses to go back when the new release
// does not come up, and what a person runs after a disk failure.
func cmdDBRestore(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("db restore", flag.ContinueOnError)
	fs.SetOutput(stdout)
	dbPath := fs.String("db", "", "path to the ghosttree database")
	from := fs.String("from", "", "the backup to put back")
	fs.Usage = func() { fmt.Fprintln(stdout, dbRestoreUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *from == "" || fs.NArg() != 0 {
		fmt.Fprintln(stdout, dbRestoreUsage)
		return 2
	}
	if fi, err := os.Stat(*from); err != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(stdout, "backup %s: not a readable file\n", *from)
		return 1
	}
	rep, err := store.CheckDatabase(context.Background(), *from, store.CheckOptions{Full: true})
	if err != nil || !rep.OK() {
		fmt.Fprintf(stdout, "the backup %s does not pass the integrity check; nothing was changed (%v %v)\n", *from, err, rep.Messages)
		return 1
	}
	prev := syscall.Umask(0o077)
	defer syscall.Umask(prev)
	dir := filepath.Dir(*dbPath)
	tmp, err := os.CreateTemp(dir, ".restore-*")
	if err != nil {
		fmt.Fprintf(stdout, "restore: %v\n", err)
		return 1
	}
	defer os.Remove(tmp.Name())
	in, err := os.Open(*from)
	if err != nil {
		tmp.Close()
		fmt.Fprintf(stdout, "restore: %v\n", err)
		return 1
	}
	_, cerr := io.Copy(tmp, in)
	in.Close()
	if serr := tmp.Sync(); cerr == nil {
		cerr = serr
	}
	if e := tmp.Close(); cerr == nil {
		cerr = e
	}
	if cerr != nil {
		fmt.Fprintf(stdout, "restore: %v\n", cerr)
		return 1
	}
	stamp := restoreNow().UTC().Format("20060102-150405")
	if err := swapIn(*dbPath, tmp.Name(), stamp); err != nil {
		fmt.Fprintf(stdout, "restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored %s from %s (the database it replaced is %s.failed-%s)\n", *dbPath, *from, *dbPath, stamp)
	return 0
}
