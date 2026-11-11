package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// Sessions an Commits und PRs (IDEA-31, P1 und P3).
//
// Der Server hat das Repo nicht. Was eine Session an Git getan hat, steht nur im
// Transkript: ein Aufruf mit Zeit, ein Ergebnis mit Zeit, selten die SHA (Claude
// Code committet meist mit -q). Der Collector meldet deshalb den Vorgang und sein
// Zeitfenster; der Server hält daraus drei Dinge fest:
//
//   - session_git_events: der Vorgang selbst (Art, Zeitfenster, Erfolg, Branch);
//   - session_commits: ein Commit, der in der Session entstand, mit benannter Quelle
//     reported (die SHA stand in der Ausgabe), observed (der Collector hat sie aus
//     dem Reflog gelesen, folgt in P2) oder matched (der Server hat das Zeitfenster
//     gegen die Commits eines PRs gelegt);
//   - session_prs: ein PR, den die Session angelegt oder gemergt hat.
//
// Keine der Tabellen trägt Transkriptinhalt: SHA, Zeit, Art, Branch, Chunk-Nummer.
// Abgeleitete Verbindungen (Request, PR, Kriterium zur Session) werden beim Lesen
// berechnet und nicht in request_work geschrieben (DEC-10, Variante A).
const sessionGitSchema = `
CREATE TABLE IF NOT EXISTS session_git_events(
  id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  project TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('commit','push','pr_create','pr_merge')),
  op TEXT NOT NULL DEFAULT '' CHECK(op IN ('','commit','amend','merge','rebase','cherry-pick','am','revert')),
  started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL DEFAULT '',
  ok INTEGER NOT NULL DEFAULT 1 CHECK(ok IN (0,1)),
  branch TEXT NOT NULL DEFAULT '',
  resolved INTEGER NOT NULL DEFAULT 0 CHECK(resolved IN (0,1)),
  UNIQUE(session_id, seq, kind));
CREATE INDEX IF NOT EXISTS session_git_events_window
  ON session_git_events(project, kind, started_at) WHERE ok = 1;
CREATE INDEX IF NOT EXISTS session_git_events_open
  ON session_git_events(project, started_at) WHERE kind = 'commit' AND ok = 1 AND resolved = 0;

CREATE TABLE IF NOT EXISTS session_commits(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  project TEXT NOT NULL,
  sha TEXT NOT NULL CHECK(length(sha) >= 7),
  source TEXT NOT NULL CHECK(source IN ('reported','observed','matched')),
  op TEXT NOT NULL DEFAULT 'commit'
     CHECK(op IN ('commit','amend','merge','rebase','cherry-pick','am','revert')),
  chunk_seq INTEGER NOT NULL DEFAULT -1,
  patch_id TEXT NOT NULL DEFAULT '',
  committed_at TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(session_id, sha)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_commits_sha ON session_commits(project, sha);
CREATE INDEX IF NOT EXISTS session_commits_short ON session_commits(project, sha) WHERE length(sha) < 40;
CREATE INDEX IF NOT EXISTS session_commits_patch ON session_commits(project, patch_id) WHERE patch_id != '';

CREATE TABLE IF NOT EXISTS session_prs(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  project TEXT NOT NULL,
  pr INTEGER NOT NULL CHECK(pr > 0),
  source TEXT NOT NULL CHECK(source IN ('created','pushed','merged')),
  chunk_seq INTEGER NOT NULL DEFAULT -1,
  recorded_at TEXT NOT NULL,
  PRIMARY KEY(session_id, project, pr, source)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_prs_pr ON session_prs(project, pr);
`

// ensureSessionGit legt die Tabellen additiv an; auf jeder Datenbank beliebig
// oft ausführbar. Läuft nach ensurePullRequests, auf dessen Tabellen sich der
// Abgleich stützt (nur zur Laufzeit, nicht im Schema).
func ensureSessionGit(db *sql.DB) error {
	_, err := db.Exec(sessionGitSchema)
	return err
}

// Art eines Vorgangs und Quellen einer Zuordnung.
const (
	GitEventCommit   = "commit"
	GitEventPush     = "push"
	GitEventPRCreate = "pr_create"
	GitEventPRMerge  = "pr_merge"

	CommitSourceReported = "reported"
	CommitSourceObserved = "observed"
	CommitSourceMatched  = "matched"

	PRSourceCreated = "created"
	PRSourcePushed  = "pushed"
	PRSourceMerged  = "merged"

	// LinkViaPR und LinkViaPatch kennzeichnen eine Verbindung, die nicht über die
	// SHA selbst läuft: die Session gehört zu einem PR, der den Commit trägt
	// (Squash-Merge), oder sie hat denselben Patch unter anderer SHA gemacht
	// (Rebase). Ohne Wert gilt: die Session hat genau diese SHA erzeugt.
	LinkViaPR    = "pr"
	LinkViaPatch = "patch"
)

var gitEventKinds = map[string]bool{GitEventCommit: true, GitEventPush: true, GitEventPRCreate: true, GitEventPRMerge: true}

var gitEventOps = map[string]bool{
	"": true, "commit": true, "amend": true, "merge": true, "rebase": true,
	"cherry-pick": true, "am": true, "revert": true,
}

// Grenzen. Ein Zeitfenster ist die Zeit zwischen Aufruf und Ergebnis; ein
// Aufruf, der länger als maxGitCallWindow gelaufen sein soll, ist abgeschnitten.
// Ein langes Fenster träfe viele Commits und träte gegen jeden Vorgang anderer
// Sessions an; wer ein Fenster aufbläst, würde so fremde Zuordnungen blockieren.
const (
	MaxSessionGitEvents = 500
	maxGitEventSHAs     = 20
	maxGitCallWindow    = 30 * time.Minute
	// gitMatchSlack ist die Toleranz zwischen Transkriptzeit und Commit-Zeit:
	// beide stammen von derselben Maschine, die Zeilen aber haben Millisekunden,
	// GitHub meldet Sekunden.
	gitMatchSlack = 2 * time.Second
	// maxLinkedSessions begrenzt, wie viele Sessions eine Antwort nennt.
	maxLinkedSessions = 100
)

// ErrGitEventSession: die Session ist unbekannt oder gehört einem anderen Konto.
// Beides sieht gleich aus, damit niemand durch Raten Sessions findet.
var ErrGitEventSession = errors.New("the session is unknown or belongs to someone else")

// ErrSessionGitEventsUnsupported meldet der Client, wenn der Server die Route
// zum Melden von Git-Vorgängen nicht kennt (ein älterer Server). Der Collector
// sendet dann bis zum nächsten Start nichts mehr.
var ErrSessionGitEventsUnsupported = errors.New("the server does not take git events of sessions")

// SessionGitEvent ist ein Git-Vorgang einer Session, wie der Collector ihn
// meldet. Die Session wird über (Harness, Kennung) benannt; wem sie gehört,
// entscheidet allein das Konto des Tokens.
type SessionGitEvent struct {
	Harness           string `json:"harness"`
	SessionExternalID string `json:"session_external_id"`
	// Seq ist die Zeilennummer des Aufrufs im Transkript.
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`
	// Op nennt bei einem Commit-Vorgang den Befehl (commit, amend, merge, rebase,
	// cherry-pick, am, revert).
	Op        string `json:"op,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	OK        bool   `json:"ok"`
	// Branch ist der Branch, auf dem die Session beim Aufruf stand (Claude Code
	// schreibt ihn je Zeile), bei einem Push der Branch auf der Gegenseite.
	Branch string `json:"branch,omitempty"`
	// SHAs sind die Hashes aus der Ausgabe von git commit (kurz oder voll).
	SHAs []string `json:"shas,omitempty"`
	// PR und PRRemote stammen aus der Adresse, die gh pr create ausgibt, bzw. aus
	// der Meldung von gh pr merge.
	PR       int64  `json:"pr,omitempty"`
	PRRemote string `json:"pr_remote,omitempty"`
}

// SessionGitResult sagt, was ein Stapel bewirkt hat.
type SessionGitResult struct {
	Recorded int `json:"recorded"`
	// Matched ist die Zahl der Commits, die der Abgleich dabei einer Session
	// zugeordnet hat.
	Matched int `json:"matched"`
}

// RecordSessionGitEvents schreibt die Vorgänge fort. Idempotent: der Collector
// liest Transkripte mehrfach, und ein wiederholter Stapel ändert nichts.
//
// Der Stapel wird ganz oder gar nicht angenommen. Gehört auch nur eine Session
// nicht dem Konto, geschieht nichts (ErrGitEventSession): so kann ein Token keine
// fremde Arbeit erfinden, und ein erfundener Commit an einer fremden Session
// wäre schlimmer als ein fehlender.
func (s *Store) RecordSessionGitEvents(account int64, events []SessionGitEvent) (SessionGitResult, error) {
	if s.writer != nil {
		return queueValue(s, []any{account, events}, func(d *Store, p []any) (SessionGitResult, error) {
			return d.RecordSessionGitEvents(p[0].(int64), p[1].([]SessionGitEvent))
		})
	}
	if len(events) == 0 {
		return SessionGitResult{}, nil
	}
	if len(events) > MaxSessionGitEvents {
		return SessionGitResult{}, fmt.Errorf("%w: at most %d events per request", ErrInvalidInput, MaxSessionGitEvents)
	}
	clean := make([]SessionGitEvent, len(events))
	for i, e := range events {
		c, err := cleanSessionGitEvent(e)
		if err != nil {
			return SessionGitResult{}, err
		}
		clean[i] = c
	}
	tx, err := s.db.Begin()
	if err != nil {
		return SessionGitResult{}, err
	}
	defer tx.Rollback()
	owner := instanceOwnerID(tx)
	type gitSession struct {
		id              int64
		project, branch string
	}
	sessions := map[string]gitSession{}
	var res SessionGitResult
	ts := now()
	reportedShas := map[string][]string{}
	for _, e := range clean {
		key := e.Harness + "\x00" + e.SessionExternalID
		sess, ok := sessions[key]
		if !ok {
			err := tx.QueryRow(`SELECT id, project, branch FROM sessions WHERE harness=? AND external_id=?
				AND (CASE WHEN account_id=0 THEN ? ELSE account_id END)=?`,
				e.Harness, e.SessionExternalID, owner, account).Scan(&sess.id, &sess.project, &sess.branch)
			if errors.Is(err, sql.ErrNoRows) {
				return SessionGitResult{}, ErrGitEventSession
			}
			if err != nil {
				return SessionGitResult{}, err
			}
			sessions[key] = sess
		}
		branch := e.Branch
		if branch == "" && e.Kind == GitEventCommit {
			branch = sess.branch
		}
		ok1 := 0
		if e.OK {
			ok1 = 1
		}
		r, err := tx.Exec(`INSERT INTO session_git_events(session_id,project,seq,kind,op,started_at,ended_at,ok,branch)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,seq,kind) DO NOTHING`,
			sess.id, sess.project, e.Seq, e.Kind, e.Op, e.StartedAt, e.EndedAt, ok1, branch)
		if err != nil {
			return SessionGitResult{}, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Recorded++
		}
		if sess.project == "" {
			// Ohne Projekt gibt es nichts, woran sich eine SHA oder ein PR binden
			// ließe: das Ereignis bleibt (Abdeckung), mehr nicht.
			continue
		}
		op := e.Op
		if op == "" {
			op = "commit"
		}
		for _, sha := range e.SHAs {
			if _, err := putSessionCommit(tx, sessionCommit{
				SessionID: sess.id, Project: sess.project, SHA: sha, Source: CommitSourceReported,
				Op: op, ChunkSeq: e.Seq, CommittedAt: e.EndedAt,
			}, ts); err != nil {
				return SessionGitResult{}, err
			}
			reportedShas[sess.project] = append(reportedShas[sess.project], sha)
		}
		if len(e.SHAs) > 0 {
			if _, err := tx.Exec(`UPDATE session_git_events SET resolved=1 WHERE session_id=? AND seq=? AND kind=?`, sess.id, e.Seq, e.Kind); err != nil {
				return SessionGitResult{}, err
			}
		}
		if e.PR > 0 && e.PRRemote == sess.project {
			var src string
			switch e.Kind {
			case GitEventPRCreate:
				src = PRSourceCreated
			case GitEventPRMerge:
				src = PRSourceMerged
			}
			if src != "" {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO session_prs(session_id,project,pr,source,chunk_seq,recorded_at) VALUES(?,?,?,?,?,?)`,
					sess.id, sess.project, e.PR, src, e.Seq, ts); err != nil {
					return SessionGitResult{}, err
				}
			}
		}
	}
	// Abgleich: erst die gemeldeten SHAs dieses Stapels gegen die Commits der
	// PRs (bestätigen, verlängern oder verwerfen), dann die Zeitfenster der
	// Vorgänge ohne SHA.
	for project, list := range reportedShas {
		var known []pullCommitAt
		for _, sha := range uniqueStrings(list) {
			rows, err := tx.Query(`SELECT DISTINCT sha, committed_at FROM git_pr_commits
				WHERE project=? AND sha >= ? AND sha < ? AND committed_at != ''`, project, sha, sha+"g")
			if err != nil {
				return SessionGitResult{}, err
			}
			for rows.Next() {
				var full, at string
				if err := rows.Scan(&full, &at); err != nil {
					rows.Close()
					return SessionGitResult{}, err
				}
				if t, ok := parseGitTime(at); ok {
					known = append(known, pullCommitAt{SHA: full, CommittedAt: t})
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return SessionGitResult{}, err
			}
			rows.Close()
		}
		if err := settleReported(tx, project, known); err != nil {
			return SessionGitResult{}, err
		}
	}
	for _, e := range clean {
		sess := sessions[e.Harness+"\x00"+e.SessionExternalID]
		if sess.project == "" || e.Kind != GitEventCommit || !e.OK || len(e.SHAs) > 0 {
			continue
		}
		n, err := matchEventWindow(tx, sess.project, e.StartedAt, e.EndedAt)
		if err != nil {
			return SessionGitResult{}, err
		}
		res.Matched += n
	}
	if err := tx.Commit(); err != nil {
		return SessionGitResult{}, err
	}
	return res, nil
}

// cleanSessionGitEvent prüft einen Vorgang und bringt ihn in die gespeicherte
// Form: Zeiten als UTC-Sekunden (der Anfang abgerundet, das Ende aufgerundet, so
// bleibt das Fenster, was es war), SHAs klein und gültig.
func cleanSessionGitEvent(e SessionGitEvent) (SessionGitEvent, error) {
	bad := func(msg string) (SessionGitEvent, error) {
		return SessionGitEvent{}, fmt.Errorf("%w: %s", ErrInvalidInput, msg)
	}
	if e.Harness != "claude-code" && e.Harness != "codex" {
		return bad("harness must be claude-code or codex")
	}
	if strings.TrimSpace(e.SessionExternalID) == "" || len(e.SessionExternalID) > 200 {
		return bad("a git event needs the external id of its session")
	}
	if !gitEventKinds[e.Kind] {
		return bad("kind must be commit, push, pr_create or pr_merge")
	}
	if !gitEventOps[e.Op] || (e.Op != "" && e.Kind != GitEventCommit) {
		return bad("op is only for commit events and must be a known command")
	}
	if e.Seq < 0 {
		return bad("seq must not be negative")
	}
	started, ok := parseGitTime(e.StartedAt)
	if !ok {
		return bad("started_at must be an RFC 3339 time")
	}
	ended := started
	if strings.TrimSpace(e.EndedAt) != "" {
		var okEnd bool
		if ended, okEnd = parseGitTime(e.EndedAt); !okEnd {
			return bad("ended_at must be an RFC 3339 time")
		}
	}
	if ended.Before(started) {
		ended = started
	}
	if ended.Sub(started) > maxGitCallWindow {
		ended = started.Add(maxGitCallWindow)
	}
	// Eine Zeit aus der Zukunft ist ein Uhrenfehler und würde ein Fenster öffnen,
	// das Commits trifft, die es noch nicht gibt.
	if limit := time.Now().UTC().Add(5 * time.Minute); started.After(limit) {
		return bad("started_at lies in the future")
	}
	endSec := ended.Truncate(time.Second)
	if endSec.Before(ended) {
		endSec = endSec.Add(time.Second)
	}
	e.StartedAt = started.Truncate(time.Second).Format(time.RFC3339)
	e.EndedAt = endSec.Format(time.RFC3339)
	if e.Branch != "" {
		b, ok := cleanRef(e.Branch)
		if !ok {
			return bad("branch is not a usable name")
		}
		e.Branch = b
	}
	if len(e.SHAs) > maxGitEventSHAs {
		e.SHAs = e.SHAs[:maxGitEventSHAs]
	}
	shas := make([]string, 0, len(e.SHAs))
	for _, sha := range e.SHAs {
		sha = gitanchor.Normalize(sha)
		if !gitanchor.ValidPrefix(sha) {
			continue
		}
		shas = append(shas, sha)
	}
	e.SHAs = shas
	if e.Kind != GitEventCommit {
		e.SHAs = nil
	}
	if e.PR < 0 || e.PR > 1<<31 {
		return bad("pr must be a pull request number")
	}
	e.PRRemote = strings.TrimSpace(e.PRRemote)
	if e.PRRemote != "" {
		r, err := normalizeRemote(e.PRRemote)
		if err != nil {
			return bad("pr_remote is not a usable repository")
		}
		e.PRRemote = r
	}
	if e.Kind != GitEventPRCreate && e.Kind != GitEventPRMerge {
		e.PR, e.PRRemote = 0, ""
	}
	return e, nil
}

func parseGitTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// sessionCommit ist eine Zeile von session_commits.
type sessionCommit struct {
	SessionID   int64
	Project     string
	SHA         string
	Source      string
	Op          string
	ChunkSeq    int
	PatchID     string
	CommittedAt string
}

// putSessionCommit legt einen Commit der Session an. Eine kurze SHA derselben
// Session, die ein Präfix der neuen ist, wird durch die volle ersetzt (sie bleibt
// mit ihrer Quelle stehen: gemeldet und bestätigt); eine kürzere neue SHA zu
// einer bekannten vollen legt nichts an. added sagt, ob die Zeile neu ist.
func putSessionCommit(tx *sql.Tx, c sessionCommit, recordedAt string) (added bool, err error) {
	rows, err := tx.Query(`SELECT sha FROM session_commits WHERE session_id=?`, c.SessionID)
	if err != nil {
		return false, err
	}
	var existing []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			rows.Close()
			return false, err
		}
		existing = append(existing, sha)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	for _, old := range existing {
		switch {
		case old == c.SHA:
			return false, nil
		case strings.HasPrefix(c.SHA, old):
			// old ist die kurze Fassung von c.SHA.
			if _, err := tx.Exec(`UPDATE session_commits SET sha=?, committed_at=CASE WHEN committed_at='' THEN ? ELSE committed_at END
				WHERE session_id=? AND sha=?`, c.SHA, c.CommittedAt, c.SessionID, old); err != nil {
				return false, err
			}
			return false, nil
		case strings.HasPrefix(old, c.SHA):
			return false, nil
		}
	}
	if c.Op == "" {
		c.Op = "commit"
	}
	if _, err := tx.Exec(`INSERT INTO session_commits(session_id,project,sha,source,op,chunk_seq,patch_id,committed_at,recorded_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, c.SessionID, c.Project, c.SHA, c.Source, c.Op, c.ChunkSeq, c.PatchID, c.CommittedAt, recordedAt); err != nil {
		return false, err
	}
	return true, nil
}

// shaPrefixes nennt alle Präfixe einer SHA ab sieben Zeichen, sie selbst
// eingeschlossen: so findet ein Index die kurze Fassung einer vollen SHA.
func shaPrefixes(sha string) []string {
	out := make([]string, 0, len(sha))
	for n := gitanchor.MinPrefix; n <= len(sha); n++ {
		out = append(out, sha[:n])
	}
	return out
}

func jsonStrings(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// settleReported prüft die gemeldeten SHAs gegen Commits, die der Server aus den
// PRs kennt, und verlängert kurze zu vollen.
//
// Eine gemeldete SHA ist eine Behauptung des Collectors. Kennt der Server den
// Commit mit seiner Zeit, muss sie zum Zeitfenster des Vorgangs passen, der sie
// gemeldet hat: ein Commit, der Stunden vor oder nach dem Aufruf entstand, kann
// nicht aus diesem Aufruf stammen. Eine Zeile, die dem widerspricht, wird
// verworfen, und so nimmt eine erfundene SHA keinem anderen einen Commit.
// Passt sie, wird eine kurze SHA durch die volle ersetzt (Quelle bleibt reported).
func settleReported(tx *sql.Tx, project string, commits []pullCommitAt) error {
	type reported struct {
		session int64
		sha     string
		seq     int
	}
	for _, c := range commits {
		rows, err := tx.Query(`SELECT session_id, sha, chunk_seq FROM session_commits
			WHERE project=? AND source='reported' AND sha IN (SELECT value FROM json_each(?))`, project, jsonStrings(shaPrefixes(c.SHA)))
		if err != nil {
			return err
		}
		var found []reported
		for rows.Next() {
			var r reported
			if err := rows.Scan(&r.session, &r.sha, &r.seq); err != nil {
				rows.Close()
				return err
			}
			found = append(found, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, r := range found {
			fits, err := reportedWindowFits(tx, r.session, r.seq, c.CommittedAt)
			if err != nil {
				return err
			}
			switch {
			case !fits:
				_, err = tx.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=? AND source='reported'`, r.session, r.sha)
			case r.sha != c.SHA:
				var have int
				if err = tx.QueryRow(`SELECT COUNT(*) FROM session_commits WHERE session_id=? AND sha=?`, r.session, c.SHA).Scan(&have); err != nil {
					return err
				}
				if have > 0 {
					_, err = tx.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=?`, r.session, r.sha)
				} else {
					_, err = tx.Exec(`UPDATE session_commits SET sha=? WHERE session_id=? AND sha=?`, c.SHA, r.session, r.sha)
				}
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// reportedWindowFits sagt, ob die Zeit eines Commits im Fenster des Vorgangs
// liegt, der seine SHA gemeldet hat. Ein Vorgang, den es nicht gibt, widerspricht
// nicht.
func reportedWindowFits(tx *sql.Tx, session int64, seq int, at time.Time) (bool, error) {
	var started, ended string
	err := tx.QueryRow(`SELECT started_at, ended_at FROM session_git_events WHERE session_id=? AND seq=? AND kind='commit'`, session, seq).Scan(&started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	from, ok1 := parseGitTime(started)
	to, ok2 := parseGitTime(ended)
	if !ok1 || !ok2 {
		return true, nil
	}
	return !at.Before(from.Add(-gitMatchSlack)) && !at.After(to.Add(gitMatchSlack)), nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// pullCommitAt ist ein Commit eines PRs mit seiner Zeit.
type pullCommitAt struct {
	SHA         string
	CommittedAt time.Time
}

// matchEventWindow legt das Fenster eines Commit-Vorgangs über die Commits der
// PRs des Projekts und entscheidet für jeden, ob er zu einer Session gehört.
func matchEventWindow(tx *sql.Tx, project, startedAt, endedAt string) (int, error) {
	from, ok1 := parseGitTime(startedAt)
	to, ok2 := parseGitTime(endedAt)
	if !ok1 || !ok2 {
		return 0, nil
	}
	rows, err := tx.Query(`SELECT DISTINCT sha, committed_at FROM git_pr_commits
		WHERE project=? AND committed_at >= ? AND committed_at <= ? AND committed_at != ''`,
		project, from.Add(-gitMatchSlack).Format(time.RFC3339), to.Add(gitMatchSlack).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	var commits []pullCommitAt
	for rows.Next() {
		var sha, at string
		if err := rows.Scan(&sha, &at); err != nil {
			rows.Close()
			return 0, err
		}
		if t, ok := parseGitTime(at); ok {
			commits = append(commits, pullCommitAt{SHA: sha, CommittedAt: t})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	return resolveCommits(tx, project, commits)
}

// resolveCommits entscheidet je Commit, welche Session ihn erzeugt hat.
func resolveCommits(tx *sql.Tx, project string, commits []pullCommitAt) (int, error) {
	assigned := 0
	for _, c := range commits {
		n, err := resolveCommit(tx, project, c)
		if err != nil {
			return assigned, err
		}
		assigned += n
	}
	return assigned, nil
}

type windowEvent struct {
	id       int64
	session  int64
	account  int64
	seq      int
	op       string
	branch   string
	reported bool
}

// resolveCommit ordnet einen Commit eines PRs der Session zu, deren Vorgang ihn
// erzeugt haben kann, oder keiner. Der Zeitpunkt des Commits stammt von GitHub,
// die Fenster aus den Transkripten der Sessions.
//
// Regeln:
//  1. Eine SHA, die eine Session selbst gemeldet oder beobachtet hat (reported,
//     observed) und deren Fenster die Commit-Zeit enthält, ist belegt; das
//     Zeitfenster der anderen entscheidet dann nichts mehr.
//  2. Vorgänge anderer Sessions, deren Fenster die Commit-Zeit enthält, treten in
//     Wettbewerb. Ein Vorgang, dessen Branch dem Branch eines PRs widerspricht,
//     der den Commit trägt, scheidet aus.
//  3. Bleibt genau eine Session, bekommt sie den Commit. Bleiben mehrere, bekommt
//     ihn die einzige, deren Branch zum PR passt; sonst niemand.
//  4. Was einmal einem Konto zugeordnet ist, nimmt kein anderes Konto zurück:
//     ein Vorgang eines fremden Kontos im selben Fenster ändert die Zuordnung
//     nicht, und ein fremder Anspruch ersetzt sie nicht. Zwischen Sessions
//     desselben Kontos (ein Orchestrator und sein Worker auf einem Branch) bleibt
//     das Ergebnis unabhängig von der Reihenfolge, in der die Vorgänge eintrafen,
//     und eine Zuordnung, die ein späterer zweiter Vorgang mehrdeutig macht, wird
//     wieder zurückgenommen. Wer ein Fenster oder einen Branch erfindet, kann so
//     fremde Arbeit nicht löschen; er kann nur seine eigene Zeile anlegen.
func resolveCommit(tx *sql.Tx, project string, c pullCommitAt) (int, error) {
	owner := instanceOwnerID(tx)
	at := c.CommittedAt
	loStr := at.Add(-gitMatchSlack).Format(time.RFC3339)
	hiStr := at.Add(gitMatchSlack).Format(time.RFC3339)
	// Regel 1: belegte Ansprüche.
	var claimants []windowEvent
	rows, err := tx.Query(`SELECT r.session_id, CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END
		FROM session_commits r JOIN sessions s ON s.id=r.session_id
		JOIN session_git_events e ON e.session_id=r.session_id AND e.seq=r.chunk_seq AND e.kind='commit'
		WHERE r.project=? AND r.source IN ('reported','observed') AND r.sha IN (SELECT value FROM json_each(?))
		  AND e.started_at <= ? AND e.ended_at >= ?`, owner, project, jsonStrings(shaPrefixes(c.SHA)), hiStr, loStr)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var e windowEvent
		if err := rows.Scan(&e.session, &e.account); err != nil {
			rows.Close()
			return 0, err
		}
		claimants = append(claimants, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	// Regel 2: Wettbewerb.
	var events []windowEvent
	if len(claimants) == 0 {
		lo := at.Add(-gitMatchSlack).Add(-maxGitCallWindow).Format(time.RFC3339)
		rows, err := tx.Query(`SELECT e.id, e.session_id, CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END, e.seq, e.op,
				COALESCE(NULLIF(e.branch,''), s.branch),
				EXISTS(SELECT 1 FROM session_commits r WHERE r.session_id=e.session_id AND r.chunk_seq=e.seq AND r.source IN ('reported','observed'))
			FROM session_git_events e JOIN sessions s ON s.id=e.session_id
			WHERE e.project=? AND e.kind='commit' AND e.ok=1 AND e.started_at >= ? AND e.started_at <= ?
			  AND e.ended_at >= ? ORDER BY e.started_at, e.id`, owner, project, lo, hiStr, loStr)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var e windowEvent
			if err := rows.Scan(&e.id, &e.session, &e.account, &e.seq, &e.op, &e.branch, &e.reported); err != nil {
				rows.Close()
				return 0, err
			}
			// Ein Vorgang mit gemeldeter SHA hat seine Antwort; er nimmt weder
			// teil noch blockiert er.
			if !e.reported {
				events = append(events, e)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
	}
	winner, err := windowOwner(tx, project, c.SHA, events)
	if err != nil {
		return 0, err
	}
	// Regel 4: was schon zugeordnet ist.
	rows, err = tx.Query(`SELECT r.session_id, CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END, r.chunk_seq
		FROM session_commits r JOIN sessions s ON s.id=r.session_id
		WHERE r.project=? AND r.sha=? AND r.source='matched'`, owner, project, c.SHA)
	if err != nil {
		return 0, err
	}
	var held []windowEvent
	for rows.Next() {
		var h windowEvent
		if err := rows.Scan(&h.session, &h.account, &h.seq); err != nil {
			rows.Close()
			return 0, err
		}
		held = append(held, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	involved := map[int64]bool{}
	for _, e := range events {
		involved[e.account] = true
	}
	for _, e := range claimants {
		involved[e.account] = true
	}
	kept := false
	for _, h := range held {
		if winner != nil && h.session == winner.session {
			continue
		}
		// Nimmt ein anderes Konto an der Entscheidung teil, bleibt die Zuordnung
		// stehen (Regel 4).
		foreign := winner != nil && winner.account != h.account
		for acct := range involved {
			foreign = foreign || acct != h.account
		}
		if foreign {
			kept = true
			continue
		}
		if _, err := tx.Exec(`DELETE FROM session_commits WHERE session_id=? AND sha=? AND source='matched'`, h.session, c.SHA); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`UPDATE session_git_events SET resolved = EXISTS(
				SELECT 1 FROM session_commits sc WHERE sc.session_id=session_git_events.session_id AND sc.chunk_seq=session_git_events.seq)
			WHERE session_id=? AND seq=? AND kind='commit'`, h.session, h.seq); err != nil {
			return 0, err
		}
	}
	if winner == nil || kept {
		return 0, nil
	}
	op := winner.op
	if op == "" {
		op = "commit"
	}
	added, err := putSessionCommit(tx, sessionCommit{
		SessionID: winner.session, Project: project, SHA: c.SHA, Source: CommitSourceMatched,
		Op: op, ChunkSeq: winner.seq, CommittedAt: c.CommittedAt.Format(time.RFC3339),
	}, now())
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE session_git_events SET resolved=1 WHERE id=?`, winner.id); err != nil {
		return 0, err
	}
	if added {
		return 1, nil
	}
	return 0, nil
}

// windowOwner wendet die Regeln 2 und 3 an.
func windowOwner(tx *sql.Tx, project, sha string, events []windowEvent) (*windowEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	// Die Branches der PRs, die den Commit tragen.
	rows, err := tx.Query(`SELECT DISTINCT p.head_ref FROM git_pr_commits k JOIN git_pull_requests p ON p.project=k.project AND p.number=k.pr
		WHERE k.project=? AND k.sha=?`, project, sha)
	if err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		heads[h] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var fitting, unknown []windowEvent
	for _, e := range events {
		switch {
		case e.branch == "" || len(heads) == 0:
			unknown = append(unknown, e)
		case heads[e.branch]:
			fitting = append(fitting, e)
		}
	}
	// Mehrere Vorgänge derselben Session im selben Fenster sind kein Wettbewerb.
	switch {
	case len(sessionsOf(fitting)) == 1:
		return &fitting[0], nil
	case len(fitting) == 0 && len(sessionsOf(unknown)) == 1:
		return &unknown[0], nil
	}
	return nil, nil
}

func sessionsOf(events []windowEvent) map[int64]bool {
	out := map[int64]bool{}
	for _, e := range events {
		out[e.session] = true
	}
	return out
}

// matchPullCommits gleicht nach einem PR-Sync ab: kurze SHAs werden voll, und die
// Commits des PRs werden gegen offene und frühere Vorgänge gelegt. Ein Fehler
// hier darf den Sync nicht scheitern lassen (er läuft nach dessen Commit); er wird
// protokolliert und beim nächsten Sync nachgeholt.
func (s *Store) matchPullCommits(project string, commits []pullCommitAt) {
	if len(commits) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	defer tx.Rollback()
	if err := settleReported(tx, project, commits); err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	if _, err := resolveCommits(tx, project, commits); err != nil {
		log.Printf("session git match for %s: %v", project, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("session git match for %s: %v", project, err)
	}
}

// ------------------------------------------------------------------ Lesen

// GitSessionLink ist die Verbindung einer Session zu einem Commit oder PR, wie sie
// in der Datenbank steht; ohne Sichtprüfung. Aufrufer mit einem Betrachter gehen
// über ProjectAccess.
type GitSessionLink struct {
	SessionID int64
	// Sources nennt, woher die Verbindung stammt: reported, observed, matched
	// (der Commit), created, merged (der PR).
	Sources []string
	// Via ist leer, wenn die Session die SHA selbst erzeugt hat bzw. den PR
	// bedient hat; sonst LinkViaPatch oder LinkViaPR.
	Via string
	// PR ist die Nummer des PRs, über den die Verbindung läuft (bei LinkViaPR),
	// oder des PRs der Anfrage.
	PR int64
	// Commits ist, wie viele Commits der Session zum PR gehören.
	Commits int
	// At ist der früheste Zeitpunkt, den die Zuordnung kennt.
	At string
}

// linkRank ordnet die Stärke einer Verbindung: direkt vor Patch vor PR.
func linkRank(via string) int {
	switch via {
	case "":
		return 0
	case LinkViaPatch:
		return 1
	}
	return 2
}

type linkSet map[int64]*GitSessionLink

func (l linkSet) add(n GitSessionLink) {
	cur, ok := l[n.SessionID]
	if !ok {
		c := n
		c.Sources = uniqueStrings(c.Sources)
		l[n.SessionID] = &c
		return
	}
	cur.Sources = uniqueStrings(append(cur.Sources, n.Sources...))
	cur.Commits += n.Commits
	if cur.At == "" || (n.At != "" && n.At < cur.At) {
		cur.At = n.At
	}
	if linkRank(n.Via) < linkRank(cur.Via) {
		cur.Via, cur.PR = n.Via, n.PR
	}
	if cur.PR == 0 {
		cur.PR = n.PR
	}
}

func (l linkSet) list() []GitSessionLink {
	out := make([]GitSessionLink, 0, len(l))
	for _, v := range l {
		sort.Strings(v.Sources)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// SessionsForPull nennt die Sessions eines PRs: die ihn angelegt oder gemergt
// haben (session_prs) und die, in denen einer seiner Commits entstand
// (session_commits gegen git_pr_commits, auch ein durch Force-Push verdrängter).
// Ein Squash-Merge ändert daran nichts: die Commits des PRs sind die des Branches.
func (s *Store) SessionsForPull(project string, number int64) ([]GitSessionLink, error) {
	if s.reader != nil {
		return s.reader.SessionsForPull(project, number)
	}
	project = normalizeProject(project)
	if project == "" || number <= 0 {
		return nil, nil
	}
	set := linkSet{}
	if err := s.sessionsForPull(project, number, "", set); err != nil {
		return nil, err
	}
	return set.list(), nil
}

func (s *Store) sessionsForPull(project string, number int64, via string, set linkSet) error {
	// Ein "merged" gilt nur, solange der PR, wenn der Server ihn kennt, wirklich
	// gemergt ist: die Meldung stammt vom Collector und ist eine Behauptung.
	rows, err := s.db.Query(`SELECT sp.session_id, sp.source, sp.recorded_at FROM session_prs sp WHERE sp.project=? AND sp.pr=?
		AND (sp.source != 'merged' OR COALESCE((SELECT p.state FROM git_pull_requests p WHERE p.project=sp.project AND p.number=sp.pr), 'merged') = 'merged')`, project, number)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var src, at string
		if err := rows.Scan(&id, &src, &at); err != nil {
			rows.Close()
			return err
		}
		set.add(GitSessionLink{SessionID: id, Sources: []string{src}, Via: via, PR: number, At: at})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT sc.session_id, sc.source, COUNT(DISTINCT sc.sha), MIN(COALESCE(NULLIF(sc.committed_at,''), sc.recorded_at))
		FROM git_pr_commits k JOIN session_commits sc ON sc.project=k.project AND sc.sha=k.sha
		WHERE k.project=? AND k.pr=? GROUP BY sc.session_id, sc.source`, project, number)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var src, at string
		var n int
		if err := rows.Scan(&id, &src, &n, &at); err != nil {
			return err
		}
		set.add(GitSessionLink{SessionID: id, Sources: []string{src}, Via: via, PR: number, Commits: n, At: at})
	}
	return rows.Err()
}

// normalizeProject bringt einen Projektnamen in die gespeicherte Form; leer, wenn
// er keine Remote ist.
func normalizeProject(project string) string {
	r, err := normalizeRemote(strings.TrimSpace(project))
	if err != nil {
		return ""
	}
	return r
}

// SessionsForCommit nennt die Sessions zu einer SHA (voll oder ab sieben Zeichen)
// in drei Stufen:
//
//  1. Die Session hat die SHA selbst (session_commits). Dieselbe Patch-ID unter
//     anderer SHA (ein fremder Rebase) nennt die ursprüngliche Session als
//     LinkViaPatch.
//  2. Die SHA ist der Merge-Commit eines PRs (merge_sha): alle Sessions dieses PRs
//     als LinkViaPR. Bei einem Squash-Merge ist das der einzige Weg, denn der
//     Commit auf dem Hauptzweig stammt von GitHub, nicht aus einer Session.
//  3. Kennt keine Session die SHA, aber ein PR trägt sie (git_pr_commits), gelten
//     ebenfalls die Sessions dieses PRs, als LinkViaPR.
//
// Je Session gilt die stärkste Verbindung: direkt vor Patch vor PR.
func (s *Store) SessionsForCommit(project, sha string) ([]GitSessionLink, error) {
	if s.reader != nil {
		return s.reader.SessionsForCommit(project, sha)
	}
	project = normalizeProject(project)
	sha = gitanchor.Normalize(sha)
	if project == "" || !gitanchor.ValidPrefix(sha) {
		return nil, nil
	}
	set := linkSet{}
	// Stufe 1: die SHA selbst, auch in ihrer kurzen Fassung und, wenn sie kurz
	// angefragt wird, in jeder vollen, die mit ihr beginnt.
	rows, err := s.db.Query(`SELECT session_id, source, patch_id, COALESCE(NULLIF(committed_at,''), recorded_at)
		FROM session_commits WHERE project=? AND (sha IN (SELECT value FROM json_each(?)) OR (sha >= ? AND sha < ?))`,
		project, jsonStrings(shaPrefixes(sha)), sha, sha+"g")
	if err != nil {
		return nil, err
	}
	var patches []string
	for rows.Next() {
		var id int64
		var src, patch, at string
		if err := rows.Scan(&id, &src, &patch, &at); err != nil {
			rows.Close()
			return nil, err
		}
		set.add(GitSessionLink{SessionID: id, Sources: []string{src}, Commits: 1, At: at})
		if patch != "" {
			patches = append(patches, patch)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	direct := len(set) > 0
	if len(patches) > 0 {
		rows, err := s.db.Query(`SELECT session_id, source, COALESCE(NULLIF(committed_at,''), recorded_at) FROM session_commits
			WHERE project=? AND patch_id IN (SELECT value FROM json_each(?))`, project, jsonStrings(uniqueStrings(patches)))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var src, at string
			if err := rows.Scan(&id, &src, &at); err != nil {
				rows.Close()
				return nil, err
			}
			if _, ok := set[id]; !ok {
				set.add(GitSessionLink{SessionID: id, Sources: []string{src}, Via: LinkViaPatch, Commits: 1, At: at})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	// Stufe 2: Merge-Commit eines PRs.
	prs, err := s.pullsWithMerge(project, sha)
	if err != nil {
		return nil, err
	}
	// Stufe 3: nur, wenn keine Session die SHA kennt.
	if !direct {
		more, err := s.pullsCarrying(project, sha)
		if err != nil {
			return nil, err
		}
		prs = append(prs, more...)
	}
	for _, n := range uniqueInts(prs) {
		if err := s.sessionsForPull(project, n, LinkViaPR, set); err != nil {
			return nil, err
		}
	}
	return set.list(), nil
}

func uniqueInts(in []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// pullsWithMerge nennt die PRs, deren Merge-Commit die SHA ist (voll oder als
// Präfix in beiden Richtungen, denn merge_sha ist nicht immer voll).
func (s *Store) pullsWithMerge(project, sha string) ([]int64, error) {
	return s.pullNumbers(`SELECT number FROM git_pull_requests WHERE project=? AND merge_sha != ''
		AND (merge_sha IN (SELECT value FROM json_each(?)) OR (merge_sha >= ? AND merge_sha < ?))`,
		project, jsonStrings(shaPrefixes(sha)), sha, sha+"g")
}

func (s *Store) pullsCarrying(project, sha string) ([]int64, error) {
	return s.pullNumbers(`SELECT DISTINCT pr FROM git_pr_commits WHERE project=? AND (sha IN (SELECT value FROM json_each(?)) OR (sha >= ? AND sha < ?))`,
		project, jsonStrings(shaPrefixes(sha)), sha, sha+"g")
}

func (s *Store) pullNumbers(query string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SessionCommitRow ist ein Commit einer Session, wie er in der Datenbank steht.
type SessionCommitRow struct {
	SHA         string
	Source      string
	Op          string
	PatchID     string
	CommittedAt string
}

// SessionCommits nennt die Commits einer Session, die frühesten zuerst.
func (s *Store) SessionCommits(sessionID int64) ([]SessionCommitRow, error) {
	if s.reader != nil {
		return s.reader.SessionCommits(sessionID)
	}
	rows, err := s.db.Query(`SELECT sha, source, op, patch_id, committed_at FROM session_commits WHERE session_id=?
		ORDER BY COALESCE(NULLIF(committed_at,''), recorded_at), sha`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionCommitRow
	for rows.Next() {
		var r SessionCommitRow
		if err := rows.Scan(&r.SHA, &r.Source, &r.Op, &r.PatchID, &r.CommittedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionsWithOwners lädt Sessions samt Besitzer, für die Sichtprüfung der Verweise.
// Es prüft selbst nichts: wer sie weitergibt, filtert über ProjectAccess.
func (s *Store) SessionsWithOwners(ids []int64) (map[int64]Session, error) {
	if s.reader != nil {
		return s.reader.SessionsWithOwners(ids)
	}
	out := map[int64]Session{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE id IN (SELECT value FROM json_each(?))`, idsJSONOf(ids))
	if err != nil {
		return nil, err
	}
	found, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	if err := s.fillSessionOwners(found); err != nil {
		return nil, err
	}
	for _, sess := range found {
		out[sess.ID] = sess
	}
	return out, nil
}

// ---------------------------------------------------------- Sichtbarkeit

// Stufen, in denen ein Betrachter eine Session am Commit oder PR sieht (DEC-11).
const (
	// GitSessionLinkFull: er darf das Transkript lesen. Titel und Adresse stehen da.
	GitSessionLinkFull = "full"
	// GitSessionLinkMeta: er darf nur die Metadaten lesen. Person und Datum, kein Titel
	// (oft der erste Prompt) und keine Adresse.
	GitSessionLinkMeta = "meta"
)

// GitSessionLinkView ist eine Session am Commit oder PR in dem Umfang, den der
// Betrachter sehen darf.
type GitSessionLinkView struct {
	Level string `json:"level"`
	// SessionRef ist die öffentliche Adresse der Session (public_id) und nur auf
	// der Stufe full gesetzt; mit ihr liest der Agent gezielt nach.
	SessionRef string `json:"session_ref,omitempty"`
	Title      string `json:"title,omitempty"`
	Harness    string `json:"harness,omitempty"`
	// Person und Date stehen auf beiden Stufen. Date ist der Tag des Beginns.
	Person string `json:"person,omitempty"`
	Date   string `json:"date,omitempty"`
	// Sources, Via, PR und Commits sagen, woher die Verbindung stammt.
	Sources []string `json:"sources,omitempty"`
	Via     string   `json:"via,omitempty"`
	PR      int64    `json:"pr,omitempty"`
	Commits int      `json:"commits,omitempty"`
}

// GitSessionLinks ist, was ein Betrachter zu einem Commit oder PR von den Sessions
// sehen darf. Hidden zählt die Sessions, die er nicht sehen darf, ohne mehr zu
// verraten als die Zahl.
type GitSessionLinks struct {
	Project string               `json:"project"`
	Items   []GitSessionLinkView `json:"items"`
	Hidden  int                  `json:"hidden"`
	// More: so viele sichtbare Sessions fehlen in Items (Obergrenze).
	More int `json:"more,omitempty"`
}

// PullSessions nennt die Sessions eines PRs, soweit der Betrachter sie sehen darf.
func (a *ProjectAccess) PullSessions(project string, number int64) (GitSessionLinks, error) {
	project, err := a.readProject(project)
	if err != nil {
		return GitSessionLinks{}, err
	}
	links, err := a.st.SessionsForPull(project, number)
	if err != nil {
		return GitSessionLinks{}, err
	}
	return a.shapeSessionLinks(project, links)
}

// CommitSessions nennt die Sessions zu einer SHA, soweit der Betrachter sie sehen
// darf.
func (a *ProjectAccess) CommitSessions(project, sha string) (GitSessionLinks, error) {
	project, err := a.readProject(project)
	if err != nil {
		return GitSessionLinks{}, err
	}
	sha = gitanchor.Normalize(sha)
	if !gitanchor.ValidPrefix(sha) {
		return GitSessionLinks{}, pullRuleError("commit_invalid", fmt.Sprintf("a commit needs at least %d hexadecimal characters", gitanchor.MinPrefix), "pass the hash or its first characters")
	}
	links, err := a.st.SessionsForCommit(project, sha)
	if err != nil {
		return GitSessionLinks{}, err
	}
	return a.shapeSessionLinks(project, links)
}

// shapeSessionLinks wendet die Sichtbarkeit an (DEC-11, Variante A): wer das
// Transkript lesen darf, sieht Titel und Adresse; wer nur die Metadaten lesen
// darf, Person und Datum; alle anderen, die das Projekt lesen, eine Zahl. Wer das
// Projekt nicht lesen darf (auch der anonyme Leser), kommt gar nicht erst hierher:
// readProject antwortet ihm wie auf ein Projekt, das es nicht gibt.
//
// Die Sichtprüfung nimmt das Projekt der SESSION, nicht das der Anfrage: eine
// Session aus einem anderen Projekt erscheint nie, auch wenn eine Zeile sie
// diesem Projekt zuordnet.
func (a *ProjectAccess) shapeSessionLinks(project string, links []GitSessionLink) (GitSessionLinks, error) {
	return shapeGitLinks(a.st, project, links, a.CanSeeTranscript, a.CanSeeSessionMeta)
}

// shapeGitLinks ist die Sichtbarkeit selbst; die Prüfungen kommen als Funktionen,
// damit ein Test jede Stufe unabhängig von der Rechtetabelle ausprobieren kann.
func shapeGitLinks(st *Store, project string, links []GitSessionLink, canTranscript, canMeta func(Session) bool) (GitSessionLinks, error) {
	out := GitSessionLinks{Project: project, Items: []GitSessionLinkView{}}
	if len(links) == 0 {
		return out, nil
	}
	ids := make([]int64, len(links))
	for i, l := range links {
		ids[i] = l.SessionID
	}
	sessions, err := st.SessionsWithOwners(ids)
	if err != nil {
		return GitSessionLinks{}, err
	}
	for _, l := range links {
		sess, ok := sessions[l.SessionID]
		if !ok || sess.Scope.Project != project {
			continue
		}
		v := GitSessionLinkView{Sources: l.Sources, Via: l.Via, PR: l.PR, Commits: l.Commits}
		switch {
		case canTranscript(sess):
			v.Level, v.SessionRef, v.Title, v.Harness = GitSessionLinkFull, sess.PublicID, sess.Title, sess.Harness
		case canMeta(sess):
			v.Level = GitSessionLinkMeta
		default:
			out.Hidden++
			continue
		}
		v.Person = sess.Owner
		if len(sess.StartedAt) >= 10 {
			v.Date = sess.StartedAt[:10]
		}
		if len(out.Items) >= maxLinkedSessions {
			out.More++
			continue
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}
