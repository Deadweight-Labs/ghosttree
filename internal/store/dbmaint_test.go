package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const plentyOfSpace = uint64(1) << 50

func plenty(string) (uint64, error) { return plentyOfSpace, nil }

// seedMaintDB legt eine Datenbank mit den Snapshot-Tabellen an (deren
// CHECK-Bedingungen ghosttree_valid_utf8 aufrufen, woran die sqlite3-CLI
// scheitert), füllt session_chunks mit chunks Zeilen zu je chunkBytes Byte und
// löscht danach alle bis auf jede keepEvery-te. Die Datei bleibt danach mit
// freien Seiten zurück.
func seedMaintDB(t *testing.T, chunks, chunkBytes, keepEvery int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "maint"})
	if err != nil {
		t.Fatal(err)
	}
	// Eine Anweisung, eine Transaktion: einzelne Commits mit fsync dauern.
	if _, err := s.DB().Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO session_chunks(session_id, seq, role, text, raw)
		SELECT ?, i, '', 'needle' || i || ' plain text', replace(hex(zeroblob(?)), '0', 'x') FROM n`, chunks-1, id, chunkBytes/2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO context_snapshots(project,name,schema_version,state,git_object_format,git_commit,git_dirty,allow_dirty_used,git_metadata_source,actor_id,created_at)
		VALUES('p','n',1,'building','sha1',?,0,0,'server-verified','a','2026-01-01T00:00:00Z')`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := s.DB().Exec(`INSERT INTO context_snapshot_entries(snapshot_id,domain,entry_key,payload,payload_digest,payload_size)
			VALUES(1,'knowledge',?,zeroblob(2000),zeroblob(32),2000)`, fmt.Sprintf("key-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if keepEvery > 0 {
		if _, err := s.DB().Exec(`DELETE FROM session_chunks WHERE id % ? != 0`, keepEvery); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", fileURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVacuumShrinksDatabaseWithSnapshotTables(t *testing.T) {
	path := seedMaintDB(t, 400, 20_000, 10)
	before := fileSizeOf(t, path)
	chunks := countRows(t, path, "session_chunks")
	entries := countRows(t, path, "context_snapshot_entries")

	var steps []string
	rep, err := VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: plenty,
		Progress:  func(s string) { steps = append(steps, s) },
	})
	if err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	if !rep.QuickCheckOK() {
		t.Fatalf("quick_check = %v", rep.QuickCheck)
	}
	if !rep.WALTruncated || rep.WALAfter != 0 {
		t.Fatalf("WAL not truncated: truncated=%v wal=%d", rep.WALTruncated, rep.WALAfter)
	}
	if rep.Plan.FileBytes != before {
		t.Fatalf("plan size = %d, file was %d", rep.Plan.FileBytes, before)
	}
	after := fileSizeOf(t, path)
	if rep.SizeAfter != after || after >= before/2 {
		t.Fatalf("size %d -> %d (reported %d): the freed pages were not given back", before, after, rep.SizeAfter)
	}
	if got := countRows(t, path, "session_chunks"); got != chunks {
		t.Fatalf("session_chunks %d -> %d", chunks, got)
	}
	if got := countRows(t, path, "context_snapshot_entries"); got != entries || got != 20 {
		t.Fatalf("snapshot entries %d -> %d", entries, got)
	}
	want := []string{"checkpointing the WAL", "VACUUM", "checkpointing the WAL", "quick_check"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v", steps)
	}
	for i, w := range want {
		if !strings.HasPrefix(steps[i], w) {
			t.Fatalf("step %d = %q, want %q...", i, steps[i], w)
		}
	}
	if rep.Duration <= 0 {
		t.Fatal("no duration reported")
	}
	// Keine Migration: das Schema ist nach dem Lauf, wie es war.
	if _, err := os.Stat(path + "-wal"); err == nil {
		if fileSizeOf(t, path+"-wal") != 0 {
			t.Fatal("WAL left behind")
		}
	}
}

func fileSizeOf(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestVacuumRefusesWhileAnotherConnectionWrites(t *testing.T) {
	path := seedMaintDB(t, 100, 5_000, 4)
	before := fileDigest(t, path)

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

	called := false
	start := time.Now()
	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes:   plenty,
		LockTimeout: 150 * time.Millisecond,
		Confirm:     func(VacuumPlan) error { called = true; return nil },
	})
	if !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("err = %v, want ErrDatabaseInUse", err)
	}
	if called {
		t.Fatal("Confirm ran although the database is in use")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("refusal took %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "stop the server") {
		t.Fatalf("message gives no hint: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	other.Close()
	// Die Datei selbst ist unberührt: weder VACUUM noch ein Checkpoint haben sie angefasst.
	if after := fileDigest(t, path); after != before {
		t.Fatal("the database file changed although vacuum refused")
	}
}

func TestVacuumRefusesWhileAReaderBlocksTheCheckpoint(t *testing.T) {
	path := seedMaintDB(t, 100, 5_000, 4)

	// Ein Leser mit offener Transaktion hält einen Lesemarkstein im WAL; damit
	// kommt kein TRUNCATE-Checkpoint durch.
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	rtx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer rtx.Rollback()
	var n int
	if err := rtx.QueryRow(`SELECT count(*) FROM session_chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// Ein Schreiber legt Rahmen hinter den Lesemarkstein.
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO session_chunks(session_id, seq, role, text, raw) VALUES(1, 99999, '', 'later', 'later')`); err != nil {
		t.Fatal(err)
	}

	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty, LockTimeout: 150 * time.Millisecond})
	if !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("err = %v, want ErrDatabaseInUse", err)
	}
	rtx.Rollback()
	reader.Close()
	writer.Close()
	// Der Schreiber hat die Datei absichtlich geändert; VACUUM hat nichts davon verloren.
	if got := countRows(t, path, "session_chunks"); got != 26 {
		t.Fatalf("session_chunks = %d, want the 25 kept plus the one inserted", got)
	}
}

func TestVacuumRefusesWhenAnotherProcessHoldsTheFile(t *testing.T) {
	path := seedMaintDB(t, 50, 2_000, 2)
	before := fileDigest(t, path)
	called := false
	_, err := VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: plenty,
		Holders: func(string) HolderScan {
			return HolderScan{Supported: true, Holders: []DBHolder{{PID: 4242, Command: "ctx serve --db x"}}}
		},
		Confirm: func(VacuumPlan) error { called = true; return nil },
	})
	if !errors.Is(err, ErrDatabaseInUse) || !strings.Contains(err.Error(), "pid 4242 (ctx serve --db x)") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("Confirm ran")
	}
	if fileDigest(t, path) != before {
		t.Fatal("the database file changed")
	}
}

func TestVacuumRefusesWithoutEnoughSpace(t *testing.T) {
	path := seedMaintDB(t, 200, 10_000, 5)
	before := fileDigest(t, path)

	// Erst den Plan holen: Confirm lehnt ab, nach den Prüfungen.
	var plan VacuumPlan
	_, err := VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: plenty,
		Confirm:   func(p VacuumPlan) error { plan = p; return ErrVacuumDeclined },
	})
	if !errors.Is(err, ErrVacuumDeclined) {
		t.Fatalf("err = %v", err)
	}
	if !plan.SameFS || plan.NeedTemp <= plan.LiveBytes || plan.NeedDir <= plan.LiveBytes {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.LiveBytes != (plan.PageCount-plan.FreePages)*plan.PageSize || plan.FreePages == 0 {
		t.Fatalf("live size / free pages wrong: %+v", plan)
	}
	if !strings.EqualFold(plan.JournalMode, "wal") {
		t.Fatalf("journal mode = %q", plan.JournalMode)
	}
	if fileDigest(t, path) != before {
		t.Fatal("a declined vacuum changed the file")
	}

	// Beide Verzeichnisse auf einem Dateisystem: es zählt die Summe.
	need := uint64(plan.NeedTemp + plan.NeedDir)
	tooLittle := func(string) (uint64, error) { return need - 1, nil }
	called := false
	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: tooLittle,
		Confirm:   func(VacuumPlan) error { called = true; return nil },
	})
	if !errors.Is(err, ErrNotEnoughSpace) {
		t.Fatalf("err = %v, want ErrNotEnoughSpace", err)
	}
	if called {
		t.Fatal("Confirm ran although the space check failed")
	}
	if !strings.Contains(err.Error(), "--tmpdir") {
		t.Fatalf("message names no way out: %v", err)
	}
	if fileDigest(t, path) != before {
		t.Fatal("a refused vacuum changed the file")
	}

	// Genau die Summe reicht.
	exact := func(string) (uint64, error) { return need, nil }
	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: exact,
		Confirm:   func(VacuumPlan) error { return ErrVacuumDeclined },
	})
	if !errors.Is(err, ErrVacuumDeclined) {
		t.Fatalf("with exactly enough space: %v", err)
	}

	// Kann der Platz nicht gelesen werden, verweigert der Lauf.
	broken := func(string) (uint64, error) { return 0, errors.New("statfs failed") }
	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: broken})
	if err == nil || !strings.Contains(err.Error(), "free space") {
		t.Fatalf("err = %v", err)
	}
	if fileDigest(t, path) != before {
		t.Fatal("the file changed")
	}
}

func TestVacuumDeclinedAndCanceledChangeNothing(t *testing.T) {
	path := seedMaintDB(t, 100, 5_000, 4)
	before := fileDigest(t, path)

	_, err := VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: plenty,
		Confirm:   func(VacuumPlan) error { return ErrVacuumDeclined },
	})
	if !errors.Is(err, ErrVacuumDeclined) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VacuumDatabase(ctx, path, VacuumOptions{FreeBytes: plenty}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v", err)
	}
	if fileDigest(t, path) != before {
		t.Fatal("the file changed")
	}
	if _, err := VacuumDatabase(context.Background(), filepath.Join(t.TempDir(), "missing.db"), VacuumOptions{FreeBytes: plenty}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: err = %v", err)
	}
	if _, err := VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty, TempDir: filepath.Join(t.TempDir(), "nope")}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing temp dir: err = %v", err)
	}
}

// SQLite legt die Zwischenkopie von VACUUM in einer unlinked Datei etilqs_* im
// Temp-Verzeichnis an. Ohne --tmpdir wäre das /var/tmp (oder $TMPDIR).
func TestVacuumWritesItsTemporaryCopyToTheTempDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/self/fd")
	}
	path := seedMaintDB(t, 160, 500_000, 2)
	tempDir := t.TempDir()

	var (
		mu       sync.Mutex
		seen     []string
		stop     = make(chan struct{})
		finished = make(chan struct{})
	)
	scan := func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
			}
			fds, _ := os.ReadDir("/proc/self/fd")
			for _, fd := range fds {
				if link, err := os.Readlink("/proc/self/fd/" + fd.Name()); err == nil && strings.Contains(link, "etilqs_") {
					mu.Lock()
					seen = append(seen, link)
					mu.Unlock()
				}
			}
			time.Sleep(time.Millisecond)
		}
	}
	started := false
	_, err := VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes: plenty,
		TempDir:   tempDir,
		Progress: func(step string) {
			if strings.HasPrefix(step, "VACUUM") && !started {
				started = true
				go scan()
			}
		},
	})
	close(stop)
	if started {
		<-finished
	}
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("never saw the temporary file of VACUUM")
	}
	for _, link := range seen {
		if !strings.HasPrefix(link, tempDir+"/") {
			t.Fatalf("temporary file %q is outside the temp dir %s", link, tempDir)
		}
	}
	// Die Einstellung ist nach dem Lauf zurückgenommen: sie gilt sonst für den ganzen Prozess.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var dir string
	if err := db.QueryRow(`PRAGMA temp_store_directory`).Scan(&dir); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if dir != "" {
		t.Fatalf("temp_store_directory stayed set to %q", dir)
	}
}

func TestVacuumFTSOptimizeCoversEveryFTSTable(t *testing.T) {
	path := seedMaintDB(t, 120, 3_000, 3)

	rep, err := VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.FTSOptimized) != 0 {
		t.Fatalf("optimized %v without the option", rep.FTSOptimized)
	}
	want := rep.Plan.FTSTables
	for _, name := range []string{"chunks_fts", "sess_fts", "knowledge_fts"} {
		found := false
		for _, w := range want {
			found = found || w == name
		}
		if !found {
			t.Fatalf("FTS tables %v lack %s", want, name)
		}
	}
	for _, shadow := range want {
		if strings.Contains(shadow, "_data") || strings.Contains(shadow, "_idx") || strings.Contains(shadow, "_docsize") || strings.Contains(shadow, "_config") {
			t.Fatalf("shadow table %q listed as FTS table", shadow)
		}
	}
	match := func() int {
		db, err := sql.Open("sqlite", fileURI(path, "mode=ro"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'needle3'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	hits := match()
	if hits == 0 {
		t.Fatal("the seed has no full-text hit")
	}

	var steps []string
	rep, err = VacuumDatabase(context.Background(), path, VacuumOptions{
		FreeBytes:   plenty,
		FTSOptimize: true,
		Progress:    func(s string) { steps = append(steps, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rep.FTSOptimized, ",") != strings.Join(want, ",") {
		t.Fatalf("optimized %v, want %v", rep.FTSOptimized, want)
	}
	if !rep.QuickCheckOK() || !rep.WALTruncated {
		t.Fatalf("report = %+v", rep)
	}
	if got := match(); got != hits {
		t.Fatalf("full-text hits %d -> %d", hits, got)
	}
	// optimize steht vor dem VACUUM, und danach wird das WAL erneut geleert.
	order := strings.Join(steps, "|")
	if !strings.Contains(order, "optimizing chunks_fts") || strings.Index(order, "optimizing") > strings.Index(order, "VACUUM") {
		t.Fatalf("steps = %v", steps)
	}
	if strings.Count(order, "checkpointing the WAL") != 3 {
		t.Fatalf("want checkpoints before optimize, after optimize and after VACUUM: %v", steps)
	}
}

func TestCheckDatabaseReportsOKAndRunsBesideAWriter(t *testing.T) {
	path := seedMaintDB(t, 50, 2_000, 0)
	before := fileDigest(t, path)

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

	for _, full := range []bool{false, true} {
		rep, err := CheckDatabase(context.Background(), path, CheckOptions{Full: full})
		if err != nil {
			t.Fatalf("full=%v: %v", full, err)
		}
		want := "quick_check"
		if full {
			want = "integrity_check"
		}
		if !rep.OK() || rep.Pragma != want {
			t.Fatalf("full=%v: report = %+v", full, rep)
		}
	}
	tx.Rollback()
	other.Close()
	if fileDigest(t, path) != before {
		t.Fatal("a check changed the database file")
	}
}

// Mit ignore_check_constraints lässt sich eine Zeile einschleusen, die gegen
// die CHECK-Bedingung mit ghosttree_valid_utf8 verstößt; quick_check muss sie
// finden, und das kann nur ein Treiber, der die Funktion kennt.
func TestCheckDatabaseReportsAViolatedCheckConstraint(t *testing.T) {
	path := seedMaintDB(t, 20, 1_000, 0)
	violateCheckConstraint(t, path)

	for _, full := range []bool{false, true} {
		rep, err := CheckDatabase(context.Background(), path, CheckOptions{Full: full})
		if err != nil {
			t.Fatalf("full=%v: %v", full, err)
		}
		if rep.OK() || len(rep.Messages) == 0 {
			t.Fatalf("full=%v: a violated CHECK constraint went unnoticed: %+v", full, rep)
		}
		if !strings.Contains(strings.Join(rep.Messages, "\n"), "context_snapshot_entries") {
			t.Fatalf("full=%v: finding does not name the table: %v", full, rep.Messages)
		}
	}

	// VACUUM kopiert die Zeile ohne die CHECK-Bedingungen zu erzwingen und
	// verweigert nicht; den Befund meldet der quick_check danach.
	rep, err := VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty})
	if err != nil {
		t.Fatal(err)
	}
	if rep.QuickCheckOK() || !strings.Contains(strings.Join(rep.QuickCheck, "\n"), "context_snapshot_entries") {
		t.Fatalf("quick_check after vacuum = %v, want the violated CHECK constraint", rep.QuickCheck)
	}
}

// SQLite wertet die CHECK-Bedingungen von quick_check und integrity_check auf
// einer Verbindung nicht aus, die auf Dateiebene schreibgeschützt ist (mode=ro).
// CheckDatabase öffnet deshalb mit query_only und sagt es, wenn es doch nicht geht.
func TestCheckDatabaseSaysWhenItCannotEvaluateCheckConstraints(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only file")
	}
	path := seedMaintDB(t, 20, 1_000, 0)
	violateCheckConstraint(t, path)

	rep, err := CheckDatabase(context.Background(), path, CheckOptions{})
	if err != nil || rep.OK() || rep.ConstraintsSkipped {
		t.Fatalf("writable file: report = %+v, err = %v", rep, err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	rep, err = CheckDatabase(context.Background(), path, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.ConstraintsSkipped {
		t.Fatalf("a read-only file did not set ConstraintsSkipped: %+v", rep)
	}
}

// violateCheckConstraint schleust eine Zeile ein, deren entry_key kein gültiges
// UTF-8 ist, gegen die CHECK-Bedingung mit ghosttree_valid_utf8.
func violateCheckConstraint(t *testing.T, path string) {
	t.Helper()
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
}

func TestCheckDatabaseWithMissingFileFails(t *testing.T) {
	if _, err := CheckDatabase(context.Background(), filepath.Join(t.TempDir(), "nope.db"), CheckOptions{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// TestHelperHoldsFile ist kein Test, sondern der Kindprozess für
// TestScanFindsAnotherProcessHoldingTheDatabase: er öffnet die Datei, meldet
// sich und wartet, bis sein stdin schließt.
func TestHelperHoldsFile(t *testing.T) {
	path := os.Getenv("GHOSTTREE_TEST_HOLD_FILE")
	if path == "" {
		t.Skip("helper process only")
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	defer f.Close()
	fmt.Println("holding")
	bufio.NewReader(os.Stdin).ReadByte()
}

func TestScanFindsAnotherProcessHoldingTheDatabase(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("scans /proc")
	}
	path := seedMaintDB(t, 10, 100, 0)
	if scan := scanDatabaseHolders(path); !scan.Supported || len(scan.Holders) != 0 {
		t.Fatalf("scan before = %+v", scan)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsFile$")
	cmd.Env = append(os.Environ(), "GHOSTTREE_TEST_HOLD_FILE="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "holding" {
		t.Fatalf("helper said %q (%v)", line, err)
	}

	scan := scanDatabaseHolders(path)
	if len(scan.Holders) != 1 || scan.Holders[0].PID != cmd.Process.Pid {
		t.Fatalf("scan = %+v, want the helper pid %d", scan, cmd.Process.Pid)
	}

	before := fileDigest(t, path)
	_, err = VacuumDatabase(context.Background(), path, VacuumOptions{FreeBytes: plenty})
	if !errors.Is(err, ErrDatabaseInUse) || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", cmd.Process.Pid)) {
		t.Fatalf("err = %v", err)
	}
	if fileDigest(t, path) != before {
		t.Fatal("the database file changed")
	}

	in.Close()
	cmd.Wait()
	if scan := scanDatabaseHolders(path); len(scan.Holders) != 0 {
		t.Fatalf("scan after the helper exited = %+v", scan)
	}
}
