package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/rawcodec"
)

// Bestandslauf für Sessions an Commits und PRs (IDEA-31, P5): ctx db backfill-session-git.
//
// Der Collector erkennt Git-Vorgänge live. Was vor ihm in der Datenbank liegt (und was
// ein Server ohne die Route verpasst hat), steht nur in den Zeilen von session_chunks.
// Der Lauf liest sie in der Reihenfolge der Sessions, lässt dieselbe Erkennung darüber
// laufen wie der Collector (GitLineScanner, gitcmd) und schreibt dasselbe wie die Route
// POST /api/session-git-events: Vorgänge, gemeldete SHAs, PRs. Danach löst er den
// Abgleich gegen git_pr_commits aus (gemeldete SHAs prüfen, Zeitfenster auflegen), den
// der Server sonst nach einem PR-Sync fährt.
//
// Er läuft neben dem Server (Wartungsverbindung, WAL): gelesen wird ohne Sperre in
// Seiten, geschrieben in kurzen Transaktionen mit Pause (Vorbild SlimText). Er ist
// wiederholbar (jede Zeile ist idempotent, ein zweiter Lauf schreibt nichts) und
// fortsetzbar (AfterID, oder Resume ab dem Fortschritt in index_state).
//
// Der Probelauf legt die drei Tabellen als TEMP-Kopien an und läuft dagegen: dieselbe
// Erkennung, dieselben Schreibzugriffe und derselbe Abgleich, aber nichts davon trifft die
// Datei, und die Zahlen des Berichts sind die, die der echte Lauf erzeugt.
//
// Gelesen wird aus raw, nicht aus text: text ist seit IDEA-25 leer oder abgeleitet und
// kürzt Argumente auf 600 Zeichen (der Befehl eines git commit mit langer Nachricht ginge
// verloren). Der Collector liest dieselbe Zeile, bevor er sie schlankt; slim lässt
// Aufrufe und die kurzen Ergebnisse, auf die die Erkennung baut (Zeit, cwd, Branch,
// Befehl, Ausgabe), stehen.

// GitLineScanner liest die gespeicherten Zeilen einer Session in der Reihenfolge ihrer
// Nummern und nennt danach die Git-Vorgänge. Der Collector liefert die Erkennung (der
// Store kennt sie nicht, sie lebt neben dem Parser der Transkripte).
type GitLineScanner interface {
	Line(seq int, raw string)
	Events() []SessionGitEvent
}

// BackfillGitOptions steuert BackfillSessionGit.
type BackfillGitOptions struct {
	// DryRun schreibt nichts in die Datei: er läuft gegen TEMP-Kopien der Tabellen.
	DryRun bool
	// Resume setzt hinter der Session fort, bis zu der der letzte Lauf kam (index_state).
	Resume bool
	// AfterID setzt hinter dieser Session fort (ein abgebrochener Lauf nennt sie).
	AfterID int64
	// Batch ist die Zahl der Vorgänge, die eine Schreibtransaktion höchstens schreibt
	// (Standard 100).
	Batch int
	// MatchBatch ist die Zahl der Commits oder gemeldeten SHAs, die eine Transaktion des
	// Abgleichs höchstens abwägt (Standard 20).
	MatchBatch int
	// Pause wartet nach jeder Transaktion mindestens so lange, wie sie die
	// Schreibsperre hielt, und nie kürzer als Pause (Standard 20 ms).
	Pause time.Duration
	// Scan erzeugt die Erkennung für eine Session.
	Scan func(harness, externalID string) GitLineScanner
	// Progress wird nach jeder Schreibtransaktion mit dem bisherigen Stand aufgerufen.
	Progress func(BackfillGitReport)
}

// BySource zählt nach der stärksten Quelle: reported vor observed vor message+pushed vor
// message vor pushed vor matched; None hat keine. SignalWeak ist der Teil von MessagePushed,
// Message und Pushed, MatchedWeak der Teil von Matched, dessen Zuordnung nur schwach ist (der
// Autor ließ sich nicht prüfen).
type BySource struct {
	Reported      int64 `json:"reported"`
	Observed      int64 `json:"observed"`
	MessagePushed int64 `json:"message_pushed"`
	Message       int64 `json:"message"`
	Pushed        int64 `json:"pushed"`
	SignalWeak    int64 `json:"signal_weak"`
	Matched       int64 `json:"matched"`
	MatchedWeak   int64 `json:"matched_weak"`
	None          int64 `json:"none"`
}

// Die Ränge einer Quelle: kleiner ist stärker.
const (
	rankReported = iota
	rankObserved
	rankMessagePushed
	rankMessage
	rankPushed
	rankMatched
)

func (b BySource) With() int64 {
	return b.Reported + b.Observed + b.MessagePushed + b.Message + b.Pushed + b.Matched
}

func (b *BySource) add(rank int, weak bool) {
	switch rank {
	case rankReported:
		b.Reported++
	case rankObserved:
		b.Observed++
	case rankMessagePushed, rankMessage, rankPushed:
		switch rank {
		case rankMessagePushed:
			b.MessagePushed++
		case rankMessage:
			b.Message++
		default:
			b.Pushed++
		}
		if weak {
			b.SignalWeak++
		}
	case rankMatched:
		b.Matched++
		if weak {
			b.MatchedWeak++
		}
	default:
		b.None++
	}
}

// BackfillCoverage ist die Abdeckung nach dem Lauf: wie viele Commit-Aufrufe, PRs und
// Commits eine Session haben, je nach stärkster Quelle. Sie gilt für die ganze
// Datenbank, nicht nur für diesen Lauf.
type BackfillCoverage struct {
	Sessions int64 `json:"sessions"`
	// SessionsWithCalls sind die Sessions mit mindestens einem Git-Vorgang.
	SessionsWithCalls int64 `json:"sessions_with_calls"`
	// CommitCalls sind die erfolgreichen Aufrufe, die einen Commit erzeugen können
	// (commit, merge, rebase, cherry-pick, am, revert), nach der Quelle ihrer SHA.
	CommitCalls BySource `json:"commit_calls"`
	// PRs: alle PRs, die der Server kennt, und wie viele davon Sessions haben (so, wie
	// die PR-Seite sie zeigt, ohne die über PR verbundenen). MergedPRs ist die Teilmenge der
	// gemergten.
	PRs       int64    `json:"prs"`
	PRsBy     BySource `json:"prs_by_source"`
	MergedPRs int64    `json:"merged_prs"`
	MergedBy  BySource `json:"merged_prs_by_source"`
	// Commits: die Commits in PRs (git_pr_commits, in_pr) und wie viele davon eine Session
	// über ihre SHA kennt.
	Commits   int64    `json:"commits"`
	CommitsBy BySource `json:"commits_by_source"`
	// ShortReported: gemeldete SHAs, die noch kurz sind, weil der Server den Commit nicht
	// kennt (kein PR, oder der PR kam noch nicht).
	ShortReported int64 `json:"short_reported"`
}

// BackfillGitReport ist das Ergebnis, auch das eines abgebrochenen Laufs. Zahlen, kein
// Inhalt.
type BackfillGitReport struct {
	DryRun bool `json:"dry_run"`
	// Sessions, die der Lauf gelesen hat, und Zeilen darin.
	Sessions int64 `json:"sessions"`
	Chunks   int64 `json:"chunks"`
	// SessionsFailed: Sessions, deren Zeilen sich nicht lesen ließen; der Lauf geht weiter.
	SessionsFailed int64  `json:"sessions_failed"`
	FirstFailure   string `json:"first_failure,omitempty"`
	// Found sind die Vorgänge, die die Erkennung in den Transkripten fand, je Art.
	Found map[string]int64 `json:"found"`
	// Recorded sind davon die neuen (ein zweiter Lauf findet 0), Known die schon
	// vorhandenen. Rejected: für sich ungültig, Capped: an der Grenze je Session.
	Recorded int64 `json:"recorded"`
	Known    int64 `json:"known"`
	Rejected int64 `json:"rejected"`
	Capped   int64 `json:"capped"`
	// ReportedAdded sind die neuen Zeilen in session_commits (SHAs aus der Ausgabe).
	ReportedAdded int64 `json:"reported_added"`
	// Enriched sind Vorgänge, die schon da waren und jetzt Nachricht oder Push-Bereich bekamen
	// (aus raw nachgetragen, P6).
	Enriched int64 `json:"enriched"`
	// Abgleich: geprüfte gemeldete oder beobachtete SHAs, geprüfte Vorgänge ohne SHA, und
	// die Commits, die dabei eine Session bekamen (matched, neu).
	ClaimsChecked  int64 `json:"claims_checked"`
	EventsMatched  int64 `json:"events_matched"`
	CommitsMatched int64 `json:"commits_matched"`
	// Nachricht und Push-Bereich (P6): geprüfte Commits der PRs mit einer Nachricht, die ein
	// Vorgang trägt, geprüfte Pushes, und die Zeilen, die dabei neu entstanden (message, pushed,
	// message+pushed).
	EvidenceCommits int64 `json:"evidence_commits"`
	EvidencePushes  int64 `json:"evidence_pushes"`
	EvidenceAdded   int64 `json:"evidence_added"`
	// EvidenceUpgraded: Zeilen, die dabei eine stärkere Quelle bekamen (matched wurde message oder
	// pushed, message und pushed zusammen message+pushed).
	EvidenceUpgraded int64 `json:"evidence_upgraded"`

	Coverage BackfillCoverage `json:"coverage"`

	// LastID ist die letzte Session, deren Vorgänge festgeschrieben sind.
	LastID   int64         `json:"last_id"`
	Duration time.Duration `json:"duration_ns"`
	// Die Zeit je Abschnitt: Zeilen lesen und erkennen, Abgleich, Abdeckung zählen.
	ScanTook     time.Duration `json:"scan_ns"`
	MatchTook    time.Duration `json:"match_ns"`
	CoverageTook time.Duration `json:"coverage_ns"`
	// Batches ist die Zahl der schreibenden Transaktionen, Write die Zeit, in der der Lauf
	// die Schreibsperre hielt, LockP50, LockP95 und LockMax die Dauer je Transaktion.
	Batches int           `json:"batches"`
	Write   time.Duration `json:"write_ns"`
	LockP50 time.Duration `json:"lock_p50_ns"`
	LockP95 time.Duration `json:"lock_p95_ns"`
	LockMax time.Duration `json:"lock_max_ns"`
}

// backfillGitCursor: der Fortschritt eines Laufs. Seit P6 trägt der Lauf Nachricht und Push-Bereich
// nach; der Fortschritt eines früheren Laufs gilt darum nicht mehr (anderer Schlüssel), und
// --resume beginnt bei der ersten Session.
const backfillGitCursor = "backfill_session_git_cursor_p6"

// backfillGitDSN öffnet die Datenbank für den Lauf; Tests setzen ein kurzes busy_timeout.
//
// Die Schreibtransaktionen beginnen mit BEGIN IMMEDIATE: die des Abgleichs lesen zuerst und
// schreiben dann, und ein Lesezustand lässt sich nicht hochstufen, wenn der Server
// dazwischen festgeschrieben hat (SQLITE_BUSY ohne Wartezeit, wiederholt nützt dann
// wenig, wenn der Server oft schreibt). Mit IMMEDIATE wartet der Lauf auf die Sperre
// (busy_timeout) und hält sie dann für die Transaktion. Der Probelauf schreibt nur in
// TEMP-Tabellen und braucht die Sperre der Datei nicht.
var backfillGitDSN = func(path string, dryRun bool) string {
	dsn := maintenanceDSN(path)
	if !dryRun {
		dsn += "&_txlock=immediate"
	}
	return dsn
}

// chunkPage ist die Zahl der Zeilen, die der Lauf je Lesen einer Session holt.
const backfillChunkPage = 400

// BackfillSessionGit führt den Bestandslauf aus (siehe oben).
func BackfillSessionGit(ctx context.Context, path string, opts BackfillGitOptions) (BackfillGitReport, error) {
	start := time.Now()
	rep := BackfillGitReport{DryRun: opts.DryRun, Found: map[string]int64{}, LastID: opts.AfterID}
	if opts.Scan == nil {
		return rep, errors.New("no scanner for the stored lines")
	}
	if opts.Batch <= 0 {
		opts.Batch = 100
	}
	if opts.MatchBatch <= 0 {
		opts.MatchBatch = 20
	}
	if opts.Pause <= 0 {
		opts.Pause = 20 * time.Millisecond
	}
	info, err := os.Stat(path)
	if err != nil {
		return rep, err
	}
	if !info.Mode().IsRegular() {
		return rep, fmt.Errorf("database path %q is not a regular file", path)
	}
	db, err := sql.Open("sqlite", backfillGitDSN(path, opts.DryRun))
	if err != nil {
		return rep, err
	}
	defer db.Close()
	// Eine Verbindung, und sie bleibt: die TEMP-Tabellen des Probelaufs gehören ihr. Nie
	// zwei Anfragen zugleich: jede Abfrage wird gelesen und geschlossen, bevor die nächste
	// beginnt.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	run := &backfillRun{ctx: ctx, db: db, opts: opts, rep: &rep, raw: rawStateFor(db), start: start}
	fail := func(err error) (BackfillGitReport, error) {
		rep.Duration = time.Since(start)
		run.finishLocks()
		if cerr := ctx.Err(); cerr != nil {
			return rep, cerr
		}
		return rep, err
	}
	if err := run.preconditions(); err != nil {
		return rep, err
	}
	if opts.DryRun {
		if err := run.tempCopies(); err != nil {
			return fail(err)
		}
	}
	if opts.Resume && opts.AfterID == 0 {
		if rep.LastID, err = run.cursor(); err != nil {
			return fail(err)
		}
	}
	run.owner = instanceOwnerID(db)
	phase := time.Now()
	took := func(into *time.Duration) {
		*into = time.Since(phase)
		phase = time.Now()
	}
	if err := run.scanSessions(); err != nil {
		return fail(err)
	}
	took(&rep.ScanTook)
	if err := run.settleClaims(); err != nil {
		return fail(err)
	}
	if err := run.matchEvidence(); err != nil {
		return fail(err)
	}
	if err := run.matchEvents(); err != nil {
		return fail(err)
	}
	took(&rep.MatchTook)
	if err := run.coverage(); err != nil {
		return fail(err)
	}
	took(&rep.CoverageTook)
	// Kein wal_checkpoint(TRUNCATE) am Ende: der Lauf schreibt nur wenige kleine Zeilen, und
	// ein TRUNCATE hielte die Schreiber des Servers an, bis er fertig ist.
	rep.Duration = time.Since(start)
	run.finishLocks()
	return rep, nil
}

type backfillRun struct {
	ctx   context.Context
	db    *sql.DB
	opts  BackfillGitOptions
	rep   *BackfillGitReport
	raw   *rawState
	start time.Time
	owner int64
	locks []time.Duration
}

func (r *backfillRun) finishLocks() {
	r.rep.LockP50, r.rep.LockP95, r.rep.LockMax = lockPercentiles(r.locks)
}

// preconditions prüft, dass der Server dieser Version die Tabellen angelegt hat.
func (r *backfillRun) preconditions() error {
	for _, t := range []string{"sessions", "session_chunks", "session_git_events", "session_commits", "session_prs", "session_git_pushes", "git_pr_commits", "git_pull_requests"} {
		var n int
		if err := r.db.QueryRowContext(r.ctx, `SELECT count(*) FROM main.sqlite_master WHERE type='table' AND name=?`, t).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no %s table: start the new ghosttree server once so it migrates the database", t)
		}
	}
	for _, c := range [][2]string{{"session_git_events", "account_id"}, {"session_commits", "weak"}, {"session_git_events", "msg_title"},
		{"session_git_events", "push_new"}, {"git_pr_commits", "title_key"}} {
		if ok, err := hasColumn(r.db, c[0], c[1]); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%s has no %s column: start the new ghosttree server once so it migrates the database", c[0], c[1])
		}
	}
	var ddl string
	if err := r.db.QueryRowContext(r.ctx, `SELECT sql FROM main.sqlite_master WHERE type='table' AND name='session_commits'`).Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "'message+pushed'") {
		return errors.New("session_commits does not take the sources message and pushed yet: start the new ghosttree server once so it migrates the database")
	}
	return nil
}

// tempCopies legt TEMP-Kopien von session_git_events, session_commits, session_prs,
// session_git_pushes und session_git_quota an. Der Name ohne Schema meint danach die Kopie: alles, was der Lauf
// schreibt oder liest, geht an ihr vorbei an der Datei.
func (r *backfillRun) tempCopies() error {
	for _, table := range []string{"session_git_events", "session_commits", "session_prs", "session_git_pushes", "session_git_quota"} {
		var ddl string
		if err := r.db.QueryRowContext(r.ctx, `SELECT sql FROM main.sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		temp := strings.Replace(ddl, "CREATE TABLE ", "CREATE TEMP TABLE ", 1)
		if temp == ddl {
			return fmt.Errorf("%s: unexpected definition", table)
		}
		if _, err := r.db.ExecContext(r.ctx, temp); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
		idx, err := r.db.QueryContext(r.ctx, `SELECT sql FROM main.sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL`, table)
		if err != nil {
			return err
		}
		var defs []string
		for idx.Next() {
			var d string
			if err := idx.Scan(&d); err != nil {
				idx.Close()
				return err
			}
			defs = append(defs, d)
		}
		err = idx.Err()
		idx.Close()
		if err != nil {
			return err
		}
		for _, d := range defs {
			d = strings.Replace(d, "CREATE UNIQUE INDEX ", "CREATE UNIQUE INDEX temp.", 1)
			d = strings.Replace(d, "CREATE INDEX ", "CREATE INDEX temp.", 1)
			if _, err := r.db.ExecContext(r.ctx, d); err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
		}
		if _, err := r.db.ExecContext(r.ctx, `INSERT INTO temp.`+table+` SELECT * FROM main.`+table); err != nil {
			return fmt.Errorf("%s: %w", table, err)
		}
	}
	return nil
}

func (r *backfillRun) cursor() (int64, error) {
	var v sql.NullString
	err := r.db.QueryRowContext(r.ctx, `SELECT val FROM index_state WHERE key=?`, backfillGitCursor).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || isNoSuchTable(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(v.String, 10, 64)
	return n, nil
}

// ------------------------------------------------------------ Vorgänge lesen und schreiben

type backfillSession struct {
	id              int64
	harness, extID  string
	project, branch string
	account         int64
	events          []SessionGitEvent
}

// scanSessions geht die Sessions in der Reihenfolge von id durch. Je Session wird gelesen
// und erkannt, ohne zu sperren; die Vorgänge sammeln sich, bis opts.Batch erreicht ist,
// und gehen dann in einer kurzen Transaktion in die Datenbank.
func (r *backfillRun) scanSessions() error {
	var pending []backfillSession
	count := 0
	flush := func(upTo int64) error {
		if err := r.flush(pending, upTo); err != nil {
			return err
		}
		pending, count = nil, 0
		return nil
	}
	after := r.rep.LastID
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		rows, err := r.db.QueryContext(r.ctx, `SELECT id, harness, external_id, project, branch,
				CASE WHEN account_id=0 THEN ? ELSE account_id END
			FROM sessions WHERE id > ? AND harness IN ('claude-code','codex') ORDER BY id LIMIT 50`, r.owner, after)
		if err != nil {
			return err
		}
		var page []backfillSession
		for rows.Next() {
			var s backfillSession
			if err := rows.Scan(&s.id, &s.harness, &s.extID, &s.project, &s.branch, &s.account); err != nil {
				rows.Close()
				return err
			}
			page = append(page, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return flush(after)
		}
		for _, s := range page {
			if err := r.ctx.Err(); err != nil {
				return err
			}
			events, err := r.readSession(s)
			r.rep.Sessions++
			after = s.id
			if err != nil {
				if cerr := r.ctx.Err(); cerr != nil {
					return cerr
				}
				r.rep.SessionsFailed++
				if r.rep.FirstFailure == "" {
					r.rep.FirstFailure = fmt.Sprintf("session %d: %v", s.id, err)
				}
				continue
			}
			if len(events) == 0 {
				continue
			}
			s.events = events
			pending = append(pending, s)
			count += len(events)
			if count >= r.opts.Batch {
				if err := flush(s.id); err != nil {
					return err
				}
			}
		}
	}
}

// readSession liest die Zeilen einer Session in Seiten und gibt die Vorgänge zurück, die
// die Erkennung fand, in ihrer gespeicherten Form.
func (r *backfillRun) readSession(s backfillSession) ([]SessionGitEvent, error) {
	scan := r.opts.Scan(s.harness, s.extID)
	seq := -1
	for {
		rows, err := r.db.QueryContext(r.ctx, `SELECT seq, raw FROM session_chunks WHERE session_id=? AND seq>? ORDER BY seq LIMIT ?`,
			s.id, seq, backfillChunkPage)
		if err != nil {
			return nil, err
		}
		type line struct {
			seq int
			raw rawcodec.Value
		}
		var page []line
		for rows.Next() {
			var l line
			if err := rows.Scan(&l.seq, &l.raw); err != nil {
				rows.Close()
				return nil, err
			}
			page = append(page, l)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, l := range page {
			text, err := r.raw.line(r.ctx, r.db, l.raw)
			if err != nil {
				return nil, fmt.Errorf("chunk %d: %w", l.seq, err)
			}
			scan.Line(l.seq, text)
			seq = l.seq
		}
		r.rep.Chunks += int64(len(page))
		if len(page) < backfillChunkPage {
			break
		}
	}
	var out []SessionGitEvent
	for _, e := range scan.Events() {
		e.Harness, e.SessionExternalID = s.harness, s.extID
		r.rep.Found[e.Kind]++
		c, err := cleanSessionGitEvent(e)
		if err != nil {
			r.rep.Rejected++
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
