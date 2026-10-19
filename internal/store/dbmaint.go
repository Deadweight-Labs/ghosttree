package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// Wartung der Datenbankdatei: VACUUM und Prüfung. Beides muss durch den
// Treiber laufen, bei dem init() die ghosttree-SQL-Funktionen registriert hat
// (ghosttree_valid_utf8 steckt in CHECK-Bedingungen), also nicht durch die
// sqlite3-CLI. Wie StripSessionBlobs öffnen diese Funktionen die Datei mit einer
// eigenen Verbindung und ohne die Migrationen von Open: ein Wartungsbefehl ändert
// nie das Schema.

var (
	// ErrDatabaseInUse: ein anderer Prozess oder eine andere Verbindung hält die
	// Datenbank offen oder gesperrt. Es wurde nichts geändert.
	ErrDatabaseInUse = errors.New("the database is in use by another process")
	// ErrNotEnoughSpace: im Temp- oder im Datenbankverzeichnis ist weniger Platz
	// frei, als VACUUM braucht. Es wurde nichts geändert.
	ErrNotEnoughSpace = errors.New("not enough free disk space")
	// ErrVacuumDeclined: Confirm hat abgelehnt. Es wurde nichts geändert.
	ErrVacuumDeclined = errors.New("vacuum declined")
)

const (
	defaultVacuumLockTimeout = 3 * time.Second
	// vacuumHeadroomBytes und ein Zehntel der Nutzdaten kommen zur Schätzung
	// hinzu: Seiten der neuen Datei sind nicht in jedem Fall so dicht gepackt wie
	// angenommen, und die Platzprüfung soll lieber zu früh als mitten im Lauf
	// anschlagen.
	vacuumHeadroomBytes = 64 << 20
	// checkDefaultLimit ist die Zahl der Befunde, die eine Prüfung höchstens liefert.
	checkDefaultLimit = 20

	sqliteBusy   = 5
	sqliteLocked = 6
)

// DBHolder ist ein anderer Prozess, der die Datenbank (oder ihr -wal / -shm)
// offen hat.
type DBHolder struct {
	PID     int
	Command string
}

func (h DBHolder) String() string {
	if h.Command == "" {
		return fmt.Sprintf("pid %d", h.PID)
	}
	return fmt.Sprintf("pid %d (%s)", h.PID, h.Command)
}

// HolderScan ist das Ergebnis der Suche nach Prozessen, die die Datenbank offen
// haben.
type HolderScan struct {
	Holders []DBHolder
	// Unreadable zählt Prozesse, deren offene Dateien sich nicht auflisten
	// ließen (fremde Benutzer ohne Root): dort kann ein Server unerkannt laufen.
	Unreadable int
	// Supported ist falsch, wo die Suche nicht möglich ist (kein /proc).
	Supported bool
}

// VacuumOptions steuert VacuumDatabase.
type VacuumOptions struct {
	// TempDir ist das Verzeichnis für die Zwischenkopie, die VACUUM anlegt. Leer
	// heißt: das Verzeichnis der Datenbank. SQLite nähme sonst eines von
	// SQLITE_TMPDIR, TMPDIR, /var/tmp, /usr/tmp, /tmp, das für eine große
	// Datenbank zu klein sein kann.
	TempDir string
	// FTSOptimize führt vor dem VACUUM für jede FTS5-Tabelle den Befehl
	// 'optimize' aus. Bis dahin liegen Token gelöschter oder geschwärzter Zeilen
	// weiter in den Segmenten des Volltextindex.
	FTSOptimize bool
	// LockTimeout ist die Wartezeit auf die Sperre der Datenbank (Standard 3 s).
	LockTimeout time.Duration
	// FreeBytes liefert den freien Platz eines Verzeichnisses (nil: Dateisystem).
	FreeBytes func(dir string) (uint64, error)
	// Holders sucht andere Prozesse, die die Datenbank offen haben (nil: /proc).
	Holders func(path string) HolderScan
	// Confirm sieht den Plan, nachdem alle Prüfungen bestanden sind und bevor
	// etwas geändert wird. Ein Fehler bricht ab; ErrVacuumDeclined heißt Nein.
	Confirm func(VacuumPlan) error
	// Progress meldet die Schritte, sobald sie beginnen.
	Progress func(step string)
}

// VacuumPlan ist, was vor dem Lauf feststeht.
type VacuumPlan struct {
	Path        string
	TempDir     string
	DBDir       string
	FileBytes   int64
	WALBytes    int64
	PageSize    int64
	PageCount   int64
	FreePages   int64
	JournalMode string
	// LiveBytes sind die Seiten, die nach dem VACUUM noch gebraucht werden.
	LiveBytes int64
	// NeedTemp ist der Platz für die Zwischenkopie im Temp-Verzeichnis, NeedDir
	// der für das WAL (oder, ohne WAL, das Rollback-Journal) neben der Datenbank.
	NeedTemp, NeedDir int64
	// SameFS: beide Verzeichnisse liegen auf einem Dateisystem, die Summe muss dort frei sein.
	SameFS            bool
	FreeTemp, FreeDir uint64
	// FTSTables sind die FTS5-Tabellen, die --fts-optimize bearbeiten würde.
	FTSTables []string
	// Scan ist das Ergebnis der Suche nach anderen Prozessen.
	Scan HolderScan
}

// VacuumReport ist das Ergebnis eines Laufs.
type VacuumReport struct {
	Plan VacuumPlan
	// SizeAfter und WALAfter sind die Dateigrößen nach dem Lauf.
	SizeAfter, WALAfter int64
	// FTSOptimized sind die Tabellen, die optimiert wurden.
	FTSOptimized []string
	// WALTruncated ist falsch, wenn der Checkpoint nach dem VACUUM nicht durchkam,
	// weil jemand die Datenbank währenddessen geöffnet hat: die Datei schrumpft
	// dann erst beim nächsten Checkpoint.
	WALTruncated bool
	// QuickCheck ist die Ausgabe von PRAGMA quick_check; ein Eintrag "ok" heißt gesund.
	QuickCheck []string
	Duration   time.Duration
}

// QuickCheckOK sagt, ob quick_check "ok" gemeldet hat.
func (r VacuumReport) QuickCheckOK() bool { return integrityOK(r.QuickCheck) }

func integrityOK(msgs []string) bool { return len(msgs) == 1 && msgs[0] == "ok" }

// VacuumDatabase verkleinert die Datenbankdatei: Checkpoint, optional
// FTS-optimize, VACUUM, Checkpoint, quick_check.
//
// Vor jeder Änderung prüft sie, dass niemand sonst die Datenbank benutzt (kein
// anderer Prozess hat Datei, -wal oder -shm offen, BEGIN EXCLUSIVE bekommt die
// Schreibsperre, der erste Checkpoint läuft ohne Leser durch) und dass in
// Temp- und Datenbankverzeichnis genug Platz frei ist. Scheitert eine Prüfung,
// ist die Datei unberührt. Die Prüfungen laufen nach Confirm noch einmal, weil
// zwischen Frage und Antwort ein Server gestartet worden sein kann.
//
// Im WAL-Modus schreibt VACUUM die neue Fassung zuerst ins WAL, braucht also
// neben der Zwischenkopie im Temp-Verzeichnis noch einmal die Größe der
// Nutzdaten neben der Datenbank.
func VacuumDatabase(ctx context.Context, path string, opts VacuumOptions) (VacuumReport, error) {
	var rep VacuumReport
	abs, err := databaseFilePath(path)
	if err != nil {
		return rep, err
	}
	tempDir := opts.TempDir
	if tempDir == "" {
		tempDir = filepath.Dir(abs)
	} else if tempDir, err = filepath.Abs(tempDir); err != nil {
		return rep, err
	}
	if info, err := os.Stat(tempDir); err != nil {
		return rep, fmt.Errorf("temp directory: %w", err)
	} else if !info.IsDir() {
		return rep, fmt.Errorf("temp directory %q is not a directory", tempDir)
	}
	timeout := opts.LockTimeout
	if timeout <= 0 {
		timeout = defaultVacuumLockTimeout
	}
	progress := func(step string) {
		if opts.Progress != nil {
			opts.Progress(step)
		}
	}

	db, err := sql.Open("sqlite", fileURI(abs, fmt.Sprintf("_pragma=busy_timeout(%d)&_pragma=temp_store(1)", timeout.Milliseconds())))
	if err != nil {
		return rep, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return rep, err
	}
	defer conn.Close()

	// temp_store_directory gilt für den ganzen Prozess; am Ende zurücksetzen.
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store_directory = "+sqlQuote(tempDir)); err != nil {
		return rep, fmt.Errorf("temp directory %s: %w", tempDir, err)
	}
	defer conn.ExecContext(context.Background(), "PRAGMA temp_store_directory = ''")
	var got string
	if err := conn.QueryRowContext(ctx, "PRAGMA temp_store_directory").Scan(&got); err != nil {
		return rep, err
	}
	if got != tempDir {
		return rep, fmt.Errorf("this SQLite build ignores the temp directory setting (asked for %q, got %q); refusing, VACUUM would write to the system default", tempDir, got)
	}

	plan, err := vacuumPreflight(ctx, conn, abs, tempDir, timeout, opts)
	rep.Plan = plan
	if err != nil {
		return rep, err
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(plan); err != nil {
			return rep, err
		}
		if plan, err = vacuumPreflight(ctx, conn, abs, tempDir, timeout, opts); err != nil {
			rep.Plan = plan
			return rep, err
		}
		rep.Plan = plan
	}

	start := time.Now()
	checkpoint := func() (busy bool, err error) {
		progress("checkpointing the WAL")
		var b, logFrames, moved int
		if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&b, &logFrames, &moved); err != nil {
			return false, fmt.Errorf("wal checkpoint: %w", err)
		}
		return b != 0, nil
	}
	// Der erste Checkpoint kommt vor jeder Änderung: ein Leser, den weder die
	// Prozess- noch die Schreibsperrenprüfung gefunden hat, hält ihn auf.
	if busy, err := checkpoint(); err != nil {
		return rep, err
	} else if busy {
		return rep, fmt.Errorf("%w: a reader kept the WAL checkpoint from finishing", ErrDatabaseInUse)
	}
	if opts.FTSOptimize {
		for _, table := range plan.FTSTables {
			progress("optimizing " + table)
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES('optimize')", sqlIdent(table), sqlIdent(table))); err != nil {
				return rep, fmt.Errorf("optimize %s: %w", table, err)
			}
			rep.FTSOptimized = append(rep.FTSOptimized, table)
		}
		if len(plan.FTSTables) > 0 {
			// Das WAL, das 'optimize' gefüllt hat, soll nicht in die Kopie von VACUUM hineinwachsen.
			if busy, err := checkpoint(); err != nil {
				return rep, err
			} else if busy {
				return rep, fmt.Errorf("%w: a reader kept the WAL checkpoint from finishing after optimize", ErrDatabaseInUse)
			}
		}
	}
	progress("VACUUM (rewrites the whole file, minutes on a large database)")
	if _, err := conn.ExecContext(ctx, "VACUUM"); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return rep, fmt.Errorf("VACUUM stopped: %w (VACUUM is atomic, the database is unchanged)", cerr)
		}
		return rep, fmt.Errorf("VACUUM: %w (VACUUM is atomic, the database is unchanged)", err)
	}
	busy, err := checkpoint()
	if err != nil {
		return rep, err
	}
	rep.WALTruncated = !busy
	rep.SizeAfter, rep.WALAfter = fileSizes(abs)

	progress("quick_check")
	rep.QuickCheck, err = runIntegrity(ctx, conn, false, checkDefaultLimit)
	rep.Duration = time.Since(start)
	if err != nil {
		return rep, fmt.Errorf("quick_check: %w", err)
	}
	return rep, nil
}

// vacuumPreflight führt alle Prüfungen aus, die nichts ändern.
func vacuumPreflight(ctx context.Context, conn *sql.Conn, abs, tempDir string, timeout time.Duration, opts VacuumOptions) (plan VacuumPlan, err error) {
	plan = VacuumPlan{Path: abs, TempDir: tempDir, DBDir: filepath.Dir(abs)}

	holders := opts.Holders
	if holders == nil {
		holders = scanDatabaseHolders
	}
	plan.Scan = holders(abs)
	if len(plan.Scan.Holders) > 0 {
		names := make([]string, len(plan.Scan.Holders))
		for i, h := range plan.Scan.Holders {
			names[i] = h.String()
		}
		return plan, fmt.Errorf("%w: %s has it open; stop the server first", ErrDatabaseInUse, strings.Join(names, ", "))
	}

	// BEGIN EXCLUSIVE bekommt im WAL-Modus die Schreibsperre: wer schreibt, hält
	// sie, und wir warten höchstens timeout darauf. Leser stört es nicht.
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		if isBusy(err) {
			return plan, fmt.Errorf("%w: another connection holds a lock on the database (waited %s); stop the server first", ErrDatabaseInUse, timeout)
		}
		return plan, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&plan.PageSize); err != nil {
		return plan, err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&plan.PageCount); err != nil {
		return plan, err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&plan.FreePages); err != nil {
		return plan, err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&plan.JournalMode); err != nil {
		return plan, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND sql LIKE 'CREATE VIRTUAL TABLE%' AND sql LIKE '%USING fts5%' ORDER BY name`)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return plan, err
		}
		plan.FTSTables = append(plan.FTSTables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return plan, err
	}
	rows.Close()

	plan.FileBytes, plan.WALBytes = fileSizes(abs)
	plan.LiveBytes = (plan.PageCount - plan.FreePages) * plan.PageSize
	need := plan.LiveBytes + plan.LiveBytes/10 + vacuumHeadroomBytes
	plan.NeedTemp = need
	plan.NeedDir = need
	if !strings.EqualFold(plan.JournalMode, "wal") {
		// Ohne WAL hält ein Rollback-Journal die alten Seiten: bis zur Dateigröße.
		plan.NeedDir = plan.FileBytes + vacuumHeadroomBytes
	}
	free := opts.FreeBytes
	if free == nil {
		free = freeDiskBytes
	}
	if plan.FreeTemp, err = free(tempDir); err != nil {
		return plan, fmt.Errorf("cannot read the free space of %s: %w", tempDir, err)
	}
	if plan.FreeDir, err = free(plan.DBDir); err != nil {
		return plan, fmt.Errorf("cannot read the free space of %s: %w", plan.DBDir, err)
	}
	plan.SameFS = sameFilesystem(tempDir, plan.DBDir)
	if plan.SameFS {
		if total := uint64(plan.NeedTemp + plan.NeedDir); plan.FreeDir < total {
			return plan, fmt.Errorf("%w: VACUUM needs about %s for its temporary copy and %s for the new WAL, both on the filesystem of %s, together %s; %s are free. Free up space or pass --tmpdir <dir> on another filesystem",
				ErrNotEnoughSpace, byteSize(plan.NeedTemp), byteSize(plan.NeedDir), plan.DBDir, byteSize(int64(total)), byteSize(int64(plan.FreeDir)))
		}
		return plan, nil
	}
	if plan.FreeTemp < uint64(plan.NeedTemp) {
		return plan, fmt.Errorf("%w: VACUUM needs about %s for its temporary copy in %s; %s are free. Pass --tmpdir <dir> with more room",
			ErrNotEnoughSpace, byteSize(plan.NeedTemp), tempDir, byteSize(int64(plan.FreeTemp)))
	}
	if plan.FreeDir < uint64(plan.NeedDir) {
		return plan, fmt.Errorf("%w: VACUUM needs about %s for the new WAL next to the database in %s; %s are free",
			ErrNotEnoughSpace, byteSize(plan.NeedDir), plan.DBDir, byteSize(int64(plan.FreeDir)))
	}
	return plan, nil
}

// CheckOptions steuert CheckDatabase.
type CheckOptions struct {
	// Full ruft integrity_check statt quick_check auf; das prüft zusätzlich, dass
	// die Indizes zu den Tabellen passen, und ist deutlich langsamer.
	Full bool
	// Limit ist die Zahl der Befunde, bei der die Prüfung aufhört (Standard 20).
	Limit int
}

// CheckReport ist das Ergebnis von CheckDatabase.
type CheckReport struct {
	// Pragma ist "quick_check" oder "integrity_check".
	Pragma string
	// Messages ist die Ausgabe des Pragmas: ein Eintrag "ok" oder die Befunde.
	Messages []string
	// ConstraintsSkipped: die Datei ist für diesen Benutzer nicht beschreibbar,
	// die Verbindung musste schreibgeschützt geöffnet werden, und dann prüft
	// SQLite die CHECK-Bedingungen der Zeilen nicht. "ok" gilt dann nur für den
	// Aufbau der Datei.
	ConstraintsSkipped bool
	Duration           time.Duration
}

// OK sagt, ob die Prüfung "ok" gemeldet hat.
func (r CheckReport) OK() bool { return integrityOK(r.Messages) }

// CheckDatabase prüft die Datenbank mit quick_check (oder integrity_check). Sie
// schreibt nichts (query_only), und es darf ein Server daneben laufen. Wegen der
// CHECK-Bedingungen mit ghosttree-Funktionen muss das durch diesen Treiber
// geschehen und nicht durch die sqlite3-CLI.
//
// Die Verbindung ist bewusst nicht mode=ro: SQLite 3.53 (modernc) wertet auf
// einer schreibgeschützt geöffneten Verbindung die CHECK-Bedingungen in
// quick_check und integrity_check nicht aus und meldet "ok", auch wenn eine Zeile
// sie verletzt. Mit query_only wird geprüft und trotzdem nichts geschrieben.
func CheckDatabase(ctx context.Context, path string, opts CheckOptions) (CheckReport, error) {
	rep := CheckReport{Pragma: "quick_check"}
	if opts.Full {
		rep.Pragma = "integrity_check"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = checkDefaultLimit
	}
	abs, err := databaseFilePath(path)
	if err != nil {
		return rep, err
	}
	// Vor dem Öffnen und vor jeder SQLite-Sperre: das Schließen einer Datei
	// gibt unter POSIX alle Sperren des Prozesses auf ihr frei.
	query := "_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	if f, err := os.OpenFile(abs, os.O_RDWR, 0); err == nil {
		f.Close()
	} else {
		query = "mode=ro&_pragma=busy_timeout(5000)"
		rep.ConstraintsSkipped = true
	}
	db, err := sql.Open("sqlite", fileURI(abs, query))
	if err != nil {
		return rep, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	start := time.Now()
	rep.Messages, err = runIntegrity(ctx, db, opts.Full, limit)
	rep.Duration = time.Since(start)
	return rep, err
}

// runIntegrity führt quick_check oder integrity_check aus und liefert die
// Zeilen. Ein Fehler der Abfrage selbst (zerstörte Seite) ist kein "ok".
func runIntegrity(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, full bool, limit int) ([]string, error) {
	pragma := "quick_check"
	if full {
		pragma = "integrity_check"
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf("PRAGMA %s(%d)", pragma, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return out, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// databaseFilePath macht den Pfad absolut und prüft, dass die Datei da ist:
// sql.Open legt eine fehlende Datei leer an, und ein Tippfehler im Pfad soll
// keine zweite Datenbank erzeugen.
func databaseFilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("database path %q is not a regular file", abs)
	}
	// -wal und -shm liegen neben der Datei, auf die ein Symlink zeigt.
	return filepath.EvalSymlinks(abs)
}

func fileURI(abs, rawQuery string) string {
	return (&url.URL{Scheme: "file", Path: abs, RawQuery: rawQuery}).String()
}

// fileSizes liefert die Größe der Datenbankdatei und ihres WAL (0, wenn es fehlt).
func fileSizes(abs string) (db, wal int64) {
	if info, err := os.Stat(abs); err == nil {
		db = info.Size()
	}
	if info, err := os.Stat(abs + "-wal"); err == nil {
		wal = info.Size()
	}
	return db, wal
}

func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == sqliteBusy || code == sqliteLocked
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func sqlIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
