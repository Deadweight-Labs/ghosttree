package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/gitmsg"
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
  account_id INTEGER NOT NULL DEFAULT 0,
  msg_title TEXT NOT NULL DEFAULT '',
  msg_full TEXT NOT NULL DEFAULT '',
  push_old TEXT NOT NULL DEFAULT '',
  push_new TEXT NOT NULL DEFAULT '',
  UNIQUE(session_id, seq, kind));
CREATE INDEX IF NOT EXISTS session_git_events_open
  ON session_git_events(project, started_at) WHERE kind = 'commit' AND ok = 1 AND resolved = 0;

CREATE TABLE IF NOT EXISTS session_commits(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  project TEXT NOT NULL,
  sha TEXT NOT NULL CHECK(length(sha) >= 7),
  source TEXT NOT NULL CHECK(source IN ('reported','observed','message+pushed','message','pushed','matched')),
  op TEXT NOT NULL DEFAULT 'commit'
     CHECK(op IN ('commit','amend','merge','rebase','cherry-pick','am','revert')),
  chunk_seq INTEGER NOT NULL DEFAULT -1,
  patch_id TEXT NOT NULL DEFAULT '',
  committed_at TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL,
  weak INTEGER NOT NULL DEFAULT 0 CHECK(weak IN (0,1)),
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

-- Die Refs, die ein Push auf der Gegenseite verändert hat, einer je Zeile (ein Push
-- kann mehrere nennen). old und new sind SHA-Präfixe des Bereichs; leer bei einem
-- neuen Branch.
CREATE TABLE IF NOT EXISTS session_git_pushes(
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  seq INTEGER NOT NULL,
  n INTEGER NOT NULL,
  project TEXT NOT NULL,
  branch TEXT NOT NULL,
  src TEXT NOT NULL DEFAULT '',
  old TEXT NOT NULL DEFAULT '',
  new TEXT NOT NULL DEFAULT '',
  new_branch INTEGER NOT NULL DEFAULT 0 CHECK(new_branch IN (0,1)),
  forced INTEGER NOT NULL DEFAULT 0 CHECK(forced IN (0,1)),
  PRIMARY KEY(session_id, seq, n)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_git_pushes_branch ON session_git_pushes(project, branch);

-- Wie viele Vorgänge ein Konto an einem Tag melden durfte (Last, nicht Recht).
CREATE TABLE IF NOT EXISTS session_git_quota(
  account_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  events INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(account_id, day)) WITHOUT ROWID;
`

// sessionGitIndexes steht getrennt vom Schema: sie stützen sich auf Spalten, die
// ein früherer Stand der Tabellen noch nicht hatte (ensureColumn legt sie vorher an).
//
// Die Fenster-Indizes tragen ended_at, damit die Prüfung "Fenster enthält die
// Commit-Zeit" im Index geschieht und keine Zeile der Tabelle lädt; der zweite
// führt das Konto vor der Zeit, so liest die Suche nach den Vorgängen des
// Autors nur dessen Zeilen.
const sessionGitIndexes = `
DROP INDEX IF EXISTS session_git_events_window;
CREATE INDEX IF NOT EXISTS session_git_events_span
  ON session_git_events(project, kind, started_at, ended_at) WHERE ok = 1;
CREATE INDEX IF NOT EXISTS session_git_events_account_span
  ON session_git_events(project, kind, account_id, started_at, ended_at) WHERE ok = 1;
CREATE INDEX IF NOT EXISTS git_pull_requests_merge ON git_pull_requests(project, merge_sha);
CREATE INDEX IF NOT EXISTS session_git_events_msg
  ON session_git_events(project, msg_title, started_at) WHERE msg_title != '';
`

// ensureSessionGit legt die Tabellen additiv an; auf jeder Datenbank beliebig
// oft ausführbar. Läuft nach ensurePullRequests, auf dessen Tabellen sich der
// Abgleich stützt (nur zur Laufzeit, nicht im Schema), und der Index auf
// git_pull_requests.merge_sha.
func ensureSessionGit(db *sql.DB) error {
	if _, err := db.Exec(sessionGitSchema); err != nil {
		return err
	}
	if err := ensureColumn(db, "session_git_events", "account_id", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("session_git_events.account_id: %w", err)
	}
	if err := ensureColumn(db, "session_commits", "weak", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("session_commits.weak: %w", err)
	}
	// Nachricht und Push-Bereich (P6): nur Hashes der Nachricht, nie ihr Text.
	for _, c := range []string{"msg_title", "msg_full", "push_old", "push_new"} {
		if err := ensureColumn(db, "session_git_events", c, `TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("session_git_events.%s: %w", c, err)
		}
	}
	if err := allowEvidenceSources(db); err != nil {
		return fmt.Errorf("session_commits sources: %w", err)
	}
	_, err := db.Exec(sessionGitIndexes)
	return err
}

// allowEvidenceSources erweitert die Prüfung der Quellen von session_commits um message,
// pushed und message+pushed. SQLite ändert eine CHECK-Bedingung nicht; die Tabelle wird
// deshalb neu gebaut: gleiche Spalten, alle Zeilen, danach die Indizes. Auf einer
// Datenbank, die die Quellen schon kennt, geschieht nichts.
func allowEvidenceSources(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='session_commits'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "'message+pushed'") {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	steps := []string{
		`ALTER TABLE session_commits RENAME TO session_commits_old`,
		`DROP INDEX IF EXISTS session_commits_sha`,
		`DROP INDEX IF EXISTS session_commits_short`,
		`DROP INDEX IF EXISTS session_commits_patch`,
	}
	for _, q := range steps {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(sessionGitSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO session_commits(session_id,project,sha,source,op,chunk_seq,patch_id,committed_at,recorded_at,weak)
		SELECT session_id,project,sha,source,op,chunk_seq,patch_id,committed_at,recorded_at,weak FROM session_commits_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE session_commits_old`); err != nil {
		return err
	}
	return tx.Commit()
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
	// MaxSessionGitEvents ist die Grenze je Request.
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

// Grenzen der Last. Der Writer ist ein einziger; was ein Konto meldet, darf ihn
// nicht beschäftigen, auch wenn jede Angabe für sich gültig ist. Als Variablen,
// damit ein Test sie senken kann.
var (
	// maxSessionGitEventsPerSession: so viele Vorgänge hält der Server je Session.
	// Eine Session, die mehr meldet, ist erfunden oder außer Kontrolle; der Rest
	// wird verworfen (Capped), denn Wiederholen ändert daran nichts.
	maxSessionGitEventsPerSession = 2000
	// maxSessionGitSHAsPerSession: so viele gemeldete oder zugeordnete SHAs je Session.
	maxSessionGitSHAsPerSession = 2000
	// maxSessionGitEventsPerDay: so viele neue Vorgänge nimmt der Server je Konto
	// und UTC-Tag an. Darüber antwortet er mit ErrGitEventQuota; der Collector hält
	// die Vorgänge zurück und versucht es später (am nächsten Tag) wieder.
	maxSessionGitEventsPerDay = 20000
	// maxWindowCandidates: gleichzeitig offene Vorgänge, die ein Commit höchstens
	// gegeneinander abwägt. Treffen mehr zu, ist die Zeit nicht aussagekräftig; der
	// Commit bleibt dann offen, statt dass der Server Hunderte Zeilen je Commit lädt.
	maxWindowCandidates = 64
	// maxWindowCommits: Commits, die ein Fenster je Anfrage liefert.
	maxWindowCommits = 300
	// maxResolvePerRun und maxMatchTime deckeln die Arbeit des Abgleichs je Stapel
	// bzw. je PR-Sync. Was darüber hinausgeht, bleibt offen und wird mit dem
	// nächsten Sync des PRs oder dem nächsten Vorgang nachgeholt.
	maxResolvePerRun = 1500
	maxMatchTime     = 3 * time.Second
)

// ErrGitEventSession: die Session ist unbekannt oder gehört einem anderen Konto.
// Beides sieht gleich aus, damit niemand durch Raten Sessions findet.
var ErrGitEventSession = errors.New("the session is unknown or belongs to someone else")

// ErrGitEventMachine: der Token ist an eine Maschine gebunden, die Session lief auf
// einer anderen.
var ErrGitEventMachine = errors.New("the token is bound to another machine than the session")

// ErrGitEventQuota: das Konto hat die Tagesgrenze für Vorgänge erreicht. Kein
// Fehler des Stapels; der Collector wiederholt ihn später.
var ErrGitEventQuota = errors.New("the account reached its daily limit of git events")

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
	// Observed sind die Commits, die der Collector nach dem erfolgreichen Aufruf im
	// Reflog des Repos gefunden hat (P2): volle SHA, Art und Patch-ID. Auch bei
	// git commit -q, das keine SHA ausgibt.
	Observed []ObservedCommit `json:"observed,omitempty"`
	// PR und PRRemote stammen aus der Adresse, die gh pr create ausgibt, bzw. aus
	// der Meldung von gh pr merge.
	PR       int64  `json:"pr,omitempty"`
	PRRemote string `json:"pr_remote,omitempty"`
	// MsgTitle und MsgFull sind die Schlüssel der Commit-Nachricht, die der Befehl trug
	// (gitmsg): der Hash der Titelzeile, und der von Titel und Rumpf (leer ohne Rumpf, oder
	// wenn git der Nachricht Zeilen anhängt). Nie der Text. Nur bei einem Commit-Vorgang.
	MsgTitle string `json:"msg_title,omitempty"`
	MsgFull  string `json:"msg_full,omitempty"`
	// PushOld und PushNew sind der Bereich "old..new" des ersten verändernden Refs eines
	// Push (SHA-Präfixe aus der Ausgabe); leer bei einem neuen Branch oder ohne Ausgabe
	// (git push -q). Alle Refs des Push stehen in PushRefs.
	PushOld  string      `json:"push_old,omitempty"`
	PushNew  string      `json:"push_new,omitempty"`
	PushRefs []PushedRef `json:"push_refs,omitempty"`
}

// PushedRef ist ein Ref, den ein Push auf der Gegenseite verändert hat.
type PushedRef struct {
	// Branch ist der Name auf der Gegenseite, Src das lokale Ref.
	Branch string `json:"branch"`
	Src    string `json:"src,omitempty"`
	// Old und New sind SHA-Präfixe des Bereichs; leer bei einem neuen Branch.
	Old       string `json:"old,omitempty"`
	New       string `json:"new,omitempty"`
	NewBranch bool   `json:"new_branch,omitempty"`
	Forced    bool   `json:"forced,omitempty"`
}

// maxPushRefs begrenzt die Refs, die ein Push-Vorgang trägt.
const maxPushRefs = 8

// ObservedCommit ist ein Commit, den der Collector im Reflog des Arbeitsverzeichnisses
// im Zeitfenster eines Aufrufs gelesen hat.
type ObservedCommit struct {
	// SHA ist voll (40 oder 64 Hexzeichen).
	SHA string `json:"sha"`
	// Op nennt, welcher Befehl den Commit erzeugt hat (commit, amend, rebase,
	// cherry-pick, merge, am, revert), wie das Reflog es sagt.
	Op string `json:"op,omitempty"`
	// PatchID ist die Ausgabe von git patch-id --stable für den Commit; leer, wenn
	// sie sich nicht bilden ließ (Merge, leerer Commit, zu groß).
	PatchID string `json:"patch_id,omitempty"`
	// CommittedAt ist die Committer-Zeit (RFC 3339).
	CommittedAt string `json:"committed_at,omitempty"`
}

// SessionGitResult sagt, was ein Stapel bewirkt hat.
type SessionGitResult struct {
	Recorded int `json:"recorded"`
	// Matched ist die Zahl der Commits, die der Abgleich dabei einer Session
	// zugeordnet hat.
	Matched int `json:"matched"`
	// Rejected zählt die Vorgänge, die für sich ungültig waren (Uhr geht vor,
	// unbekannte Art, kaputter Branch). Sie werden einzeln verworfen; die übrigen
	// des Stapels gelten. Reason nennt den ersten Grund.
	Rejected int    `json:"rejected,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Capped zählt, was an der Grenze je Session oder je Tag liegen blieb.
	Capped int `json:"capped,omitempty"`
	// Enriched zählt die Vorgänge, die schon bekannt waren und jetzt Nachricht oder
	// Push-Bereich bekamen (P6: ein Bestandslauf trägt sie nach).
	Enriched int `json:"enriched,omitempty"`
}

// RecordSessionGitEvents schreibt die Vorgänge fort. Idempotent: der Collector
// liest Transkripte mehrfach, und ein wiederholter Stapel ändert nichts.
//
// Gehört auch nur eine Session nicht dem Konto, geschieht nichts (ErrGitEventSession):
// so kann ein Token keine fremde Arbeit erfinden, und ein erfundener Commit an
// einer fremden Session wäre schlimmer als ein fehlender. Dasselbe gilt für einen
// Token, der an eine andere Maschine gebunden ist als die Session (ErrGitEventMachine).
//
// Ein Vorgang, der für sich ungültig ist (die Uhr des Rechners geht vor, eine
// unbekannte Art), kippt den Stapel nicht: er wird einzeln verworfen und in
// Rejected gezählt. Die Last ist begrenzt je Request (MaxSessionGitEvents), je
// Session und je Konto und Tag; was an der Grenze je Session liegen bleibt, zählt
// Capped, und ein Konto, das seine Tagesgrenze erreicht hat, bekommt
// ErrGitEventQuota.
func (s *Store) RecordSessionGitEvents(account int64, machine string, events []SessionGitEvent) (SessionGitResult, error) {
	if s.writer != nil {
		return queueValue(s, []any{account, machine, events}, func(d *Store, p []any) (SessionGitResult, error) {
			return d.RecordSessionGitEvents(p[0].(int64), p[1].(string), p[2].([]SessionGitEvent))
		})
	}
	var res SessionGitResult
	if len(events) == 0 {
		return res, nil
	}
	if len(events) > MaxSessionGitEvents {
		return res, fmt.Errorf("%w: at most %d events per request", ErrInvalidInput, MaxSessionGitEvents)
	}
	clean := make([]SessionGitEvent, 0, len(events))
	for _, e := range events {
		c, err := cleanSessionGitEvent(e)
		if err != nil {
			res.Rejected++
			if res.Reason == "" {
				res.Reason = strings.TrimPrefix(err.Error(), ErrInvalidInput.Error()+": ")
			}
			continue
		}
		clean = append(clean, c)
	}
	if len(clean) == 0 {
		return res, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return SessionGitResult{}, err
	}
	defer tx.Rollback()
	owner := instanceOwnerID(tx)
	day := time.Now().UTC().Format("2006-01-02")
	var usedToday int
	if err := tx.QueryRow(`SELECT events FROM session_git_quota WHERE account_id=? AND day=?`, account, day).Scan(&usedToday); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SessionGitResult{}, err
	}
	if usedToday >= maxSessionGitEventsPerDay {
		return SessionGitResult{}, ErrGitEventQuota
	}
	sessions := map[string]*gitSession{}
	ts := now()
	batch := newGitBatch()
	for _, e := range clean {
		key := e.Harness + "\x00" + e.SessionExternalID
		sess, ok := sessions[key]
		if !ok {
			sess = &gitSession{}
			var sessMachine string
			err := tx.QueryRow(`SELECT id, project, branch, machine FROM sessions WHERE harness=? AND external_id=?
				AND (CASE WHEN account_id=0 THEN ? ELSE account_id END)=?`,
				e.Harness, e.SessionExternalID, owner, account).Scan(&sess.id, &sess.project, &sess.branch, &sessMachine)
			if errors.Is(err, sql.ErrNoRows) {
				return SessionGitResult{}, ErrGitEventSession
			}
			if err != nil {
				return SessionGitResult{}, err
			}
			if machine != "" && sessMachine != "" && !strings.EqualFold(machine, sessMachine) {
				return SessionGitResult{}, ErrGitEventMachine
			}
			if err := tx.QueryRow(`SELECT COUNT(*) FROM session_git_events WHERE session_id=?`, sess.id).Scan(&sess.events); err != nil {
				return SessionGitResult{}, err
			}
			if err := tx.QueryRow(`SELECT COUNT(*) FROM session_commits WHERE session_id=?`, sess.id).Scan(&sess.shas); err != nil {
				return SessionGitResult{}, err
			}
			sessions[key] = sess
		}
		if sess.events >= maxSessionGitEventsPerSession || usedToday+res.Recorded >= maxSessionGitEventsPerDay {
			res.Capped++
			continue
		}
		if err := writeGitEvent(tx, sess, account, e, ts, batch, &res); err != nil {
			return SessionGitResult{}, err
		}
	}
	// Abgleich: erst die gemeldeten und beobachteten SHAs dieses Stapels gegen die
	// Commits der PRs (bestätigen, verlängern oder verwerfen), dann die Zeitfenster
	// der Vorgänge ohne SHA. Die Arbeit ist je Stapel gedeckelt (maxResolvePerRun,
	// maxMatchTime).
	m := newMatcher(tx, owner)
	if err := m.settleClaims(batch); err != nil {
		return SessionGitResult{}, err
	}
	// Nachricht und Push-Bereich vor dem Zeitfenster: ein Vorgang, den sie beantworten,
	// tritt nicht mehr gegen andere an.
	ne, err := m.matchEvidence(batch)
	if err != nil {
		return SessionGitResult{}, err
	}
	n, err := m.matchWindows(batch)
	if err != nil {
		return SessionGitResult{}, err
	}
	res.Matched += ne + n
	if res.Recorded > 0 {
		if _, err := tx.Exec(`INSERT INTO session_git_quota(account_id,day,events) VALUES(?,?,?)
			ON CONFLICT(account_id,day) DO UPDATE SET events=events+excluded.events`, account, day, res.Recorded); err != nil {
			return SessionGitResult{}, err
		}
		if _, err := tx.Exec(`DELETE FROM session_git_quota WHERE day < ?`, time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")); err != nil {
			return SessionGitResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SessionGitResult{}, err
	}
	return res, nil
}

// gitSession ist die Session eines Vorgangs mit den Zählern ihrer Grenzen.
type gitSession struct {
	id              int64
	project, branch string
	events, shas    int
}

// gitBatch sammelt, was ein Stapel von Vorgängen dem Abgleich hinterlässt: die
// SHAs, die Sessions gemeldet oder beobachtet haben und gegen die Commits der PRs
// zu prüfen sind, und die Zeitfenster der Commit-Vorgänge ohne jede SHA.
type gitBatch struct {
	claimed map[string][]string
	windows map[string][]gitWindow
	// evidence sind die Vorgänge mit Nachricht oder Push-Bereich (P6), je Projekt; der
	// Abgleich liest sie aus der Datenbank (sessiongit_evidence.go).
	evidence map[string][]evidenceEvent
}

func newGitBatch() *gitBatch {
	return &gitBatch{claimed: map[string][]string{}, windows: map[string][]gitWindow{}, evidence: map[string][]evidenceEvent{}}
}

// writeGitEvent schreibt einen Vorgang einer Session fort und alles, was sich ohne
// den Abgleich daraus ergibt: den Vorgang selbst, die gemeldeten und beobachteten
// SHAs, den PR. Der Abgleich gegen die PRs (settleClaims, matchWindows) folgt aus
// dem Sammelwert batch, damit der Aufrufer entscheidet, wann und in welcher
// Transaktion er läuft: der Server gleich im selben Stapel, der Bestandslauf
// (BackfillSessionGit) in eigenen, kurzen Transaktionen.
//
// Die Grenzen je Session prüft der Aufrufer (sess.events), die SHAs hier.
func writeGitEvent(tx dbTx, sess *gitSession, account int64, e SessionGitEvent, ts string, batch *gitBatch, res *SessionGitResult) error {
	branch := e.Branch
	if branch == "" && e.Kind == GitEventCommit {
		branch = sess.branch
	}
	ok1 := 0
	if e.OK {
		ok1 = 1
	}
	r, err := tx.Exec(`INSERT INTO session_git_events(session_id,project,seq,kind,op,started_at,ended_at,ok,branch,account_id,msg_title,msg_full,push_old,push_new)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,seq,kind) DO NOTHING`,
		sess.id, sess.project, e.Seq, e.Kind, e.Op, e.StartedAt, e.EndedAt, ok1, branch, account, e.MsgTitle, e.MsgFull, e.PushOld, e.PushNew)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n > 0 {
		res.Recorded++
		sess.events++
	} else if e.MsgTitle != "" || e.PushNew != "" || len(e.PushRefs) > 0 {
		// Der Vorgang ist bekannt (ein früherer Lauf kannte Nachricht und Push-Bereich noch
		// nicht): nur nachtragen, was fehlt, nie etwas ersetzen.
		r, err := tx.Exec(`UPDATE session_git_events SET
				msg_title=CASE WHEN msg_title='' THEN ? ELSE msg_title END,
				msg_full=CASE WHEN msg_full='' THEN ? ELSE msg_full END,
				push_old=CASE WHEN push_old='' THEN ? ELSE push_old END,
				push_new=CASE WHEN push_new='' THEN ? ELSE push_new END
			WHERE session_id=? AND seq=? AND kind=?
			  AND ((msg_title='' AND ?!='') OR (msg_full='' AND ?!='') OR (push_old='' AND ?!='') OR (push_new='' AND ?!=''))`,
			e.MsgTitle, e.MsgFull, e.PushOld, e.PushNew, sess.id, e.Seq, e.Kind, e.MsgTitle, e.MsgFull, e.PushOld, e.PushNew)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Enriched++
		}
	}
	if sess.project == "" {
		// Ohne Projekt gibt es nichts, woran sich eine SHA oder ein PR binden
		// ließe: das Ereignis bleibt (Abdeckung), mehr nicht.
		return nil
	}
	op := e.Op
	if op == "" {
		op = "commit"
	}
	for _, sha := range e.SHAs {
		if sess.shas >= maxSessionGitSHAsPerSession {
			res.Capped++
			break
		}
		added, err := putSessionCommit(tx, sessionCommit{
			SessionID: sess.id, Project: sess.project, SHA: sha, Source: CommitSourceReported,
			Op: op, ChunkSeq: e.Seq, CommittedAt: e.EndedAt,
		}, ts)
		if err != nil {
			return err
		}
		if added {
			sess.shas++
		}
		batch.claimed[sess.project] = append(batch.claimed[sess.project], sha)
	}
	// Beobachtet: der Collector hat die SHA im Reflog des Repos gelesen. Für die
	// Rechte ist das dieselbe Behauptung wie eine gemeldete SHA (der Server kann das
	// Repo nicht sehen); settleClaims prüft beide gleich, sobald er die Commits des
	// PRs kennt.
	for _, o := range e.Observed {
		if sess.shas >= maxSessionGitSHAsPerSession {
			res.Capped++
			break
		}
		oop := o.Op
		if oop == "" {
			oop = "commit"
		}
		at := o.CommittedAt
		if at == "" {
			at = e.EndedAt
		}
		added, err := putSessionCommit(tx, sessionCommit{
			SessionID: sess.id, Project: sess.project, SHA: o.SHA, Source: CommitSourceObserved,
			Op: oop, ChunkSeq: e.Seq, PatchID: o.PatchID, CommittedAt: at,
		}, ts)
		if err != nil {
			return err
		}
		if added {
			sess.shas++
		}
		batch.claimed[sess.project] = append(batch.claimed[sess.project], o.SHA)
	}
	if len(e.SHAs) > 0 || len(e.Observed) > 0 {
		if _, err := tx.Exec(`UPDATE session_git_events SET resolved=1 WHERE session_id=? AND seq=? AND kind=?`, sess.id, e.Seq, e.Kind); err != nil {
			return err
		}
	}
	// Ein PR, den ein fehlgeschlagener Aufruf nennt (gh pr create schlägt fehl und
	// gibt die Adresse des schon bestehenden PRs aus), ist keiner, den die
	// Session angelegt hat.
	if e.PR > 0 && e.PRRemote == sess.project && e.OK {
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
				return err
			}
		}
	}
	if e.Kind == GitEventCommit && e.OK && len(e.SHAs) == 0 && len(e.Observed) == 0 {
		batch.windows[sess.project] = append(batch.windows[sess.project], gitWindow{from: e.StartedAt, to: e.EndedAt})
	}
	if e.Kind == GitEventCommit && e.OK {
		// Auch ein Aufruf ohne Nachricht: ein Push danach muss ihn zählen können.
		batch.evidence[sess.project] = append(batch.evidence[sess.project], evidenceEvent{session: sess.id, seq: e.Seq, kind: GitEventCommit, title: e.MsgTitle, op: e.Op, from: e.StartedAt, to: e.EndedAt})
	}
	if e.Kind == GitEventPush && len(e.PushRefs) > 0 {
		for n, r := range e.PushRefs {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO session_git_pushes(session_id,seq,n,project,branch,src,old,new,new_branch,forced) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				sess.id, e.Seq, n, sess.project, r.Branch, r.Src, r.Old, r.New, boolInt(r.NewBranch), boolInt(r.Forced)); err != nil {
				return err
			}
			batch.evidence[sess.project] = append(batch.evidence[sess.project], evidenceEvent{session: sess.id, seq: e.Seq, kind: GitEventPush, branch: r.Branch})
		}
	}
	return nil
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
	e.Observed = cleanObserved(e.Observed)
	if e.Kind != GitEventCommit || !e.OK {
		e.Observed = nil
	}
	// Nachricht und Push-Bereich (P6). Ein Schlüssel, der keiner ist, entfällt; der
	// Vorgang bleibt.
	if e.Kind != GitEventCommit || (e.Op != "" && e.Op != "commit" && e.Op != "amend" && e.Op != "merge") || !gitmsg.Valid(e.MsgTitle) {
		e.MsgTitle, e.MsgFull = "", ""
	}
	if !gitmsg.Valid(e.MsgFull) {
		e.MsgFull = ""
	}
	e.PushRefs, e.PushOld, e.PushNew = cleanPushRefs(e)
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

// cleanPushRefs prüft die Refs eines Push: gültige Namen, SHA-Präfixe nur als Hex von
// sieben bis 64 Zeichen. Ein Ref ohne Bereich und ohne neuen Branch sagt nichts über Commits
// und entfällt. Ein Client, der nur PushOld und PushNew meldet (ein Ref), bekommt daraus das
// Ref des Branches. Der erste Ref bestimmt PushOld und PushNew des Vorgangs.
func cleanPushRefs(e SessionGitEvent) (refs []PushedRef, oldSHA, newSHA string) {
	if e.Kind != GitEventPush {
		return nil, "", ""
	}
	in := e.PushRefs
	if len(in) == 0 && e.Branch != "" && (e.PushOld != "" || e.PushNew != "") {
		in = []PushedRef{{Branch: e.Branch, Old: e.PushOld, New: e.PushNew}}
	}
	seen := map[string]bool{}
	for _, r := range in {
		b, ok := cleanRef(r.Branch)
		if !ok {
			continue
		}
		r.Branch = b
		r.Src, _ = cleanRef(r.Src)
		r.Old, r.New = gitanchor.Normalize(r.Old), gitanchor.Normalize(r.New)
		if r.Old != "" && !gitanchor.ValidPrefix(r.Old) {
			r.Old = ""
		}
		if !gitanchor.ValidPrefix(r.New) {
			r.New = ""
		}
		if r.New == "" {
			r.Old = ""
		}
		if r.New == "" && !r.NewBranch {
			continue
		}
		key := r.Branch + "\x00" + r.Old + "\x00" + r.New
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, r)
		if len(refs) >= maxPushRefs {
			break
		}
	}
	if len(refs) > 0 {
		oldSHA, newSHA = refs[0].Old, refs[0].New
	}
	return refs, oldSHA, newSHA
}

// cleanObserved lässt nur Commits mit voller SHA stehen (eine kürzere kann der
// Reflog nicht liefern; sie wäre eine Behauptung ohne Gewicht), je Vorgang höchstens
// maxGitEventSHAs, ohne Dubletten. Eine Patch-ID, die keine ist, entfällt; der
// Commit bleibt.
func cleanObserved(in []ObservedCommit) []ObservedCommit {
	var out []ObservedCommit
	seen := map[string]bool{}
	for _, o := range in {
		o.SHA = gitanchor.Normalize(o.SHA)
		if (len(o.SHA) != 40 && len(o.SHA) != 64) || !gitanchor.ValidPrefix(o.SHA) || seen[o.SHA] {
			continue
		}
		seen[o.SHA] = true
		if !gitEventOps[o.Op] {
			o.Op = "commit"
		}
		if p := gitanchor.Normalize(o.PatchID); len(p) >= 40 && len(p) <= 64 && gitanchor.ValidPrefix(p) {
			o.PatchID = p
		} else {
			o.PatchID = ""
		}
		if t, ok := parseGitTime(o.CommittedAt); ok {
			o.CommittedAt = t.Format(time.RFC3339)
		} else {
			o.CommittedAt = ""
		}
		out = append(out, o)
		if len(out) >= maxGitEventSHAs {
			break
		}
	}
	return out
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
	// Weak: die Zuordnung (matched) beruht auf einem Zeitfenster, und der Autor des
	// Commits ließ sich nicht gegen das Konto der Session prüfen.
	Weak bool
}

// putSessionCommit legt einen Commit der Session an. Eine kurze SHA derselben
// Session, die ein Präfix der neuen ist, wird durch die volle ersetzt (sie bleibt
// mit ihrer Quelle stehen: gemeldet und bestätigt); eine kürzere neue SHA zu
// einer bekannten vollen legt nichts an. added sagt, ob die Zeile neu ist.
//
// Je Aufruf drei gezielte Abfragen auf den Primärschlüssel (session_id, sha):
// dieselbe oder eine kürzere SHA, eine längere, dann das Einfügen. Die Session
// wird nicht geladen; sonst wuchs die Arbeit mit der Zahl ihrer Commits im Quadrat.
func putSessionCommit(tx dbTx, c sessionCommit, recordedAt string) (added bool, err error) {
	var old string
	err = tx.QueryRow(sqlOwnShorterSHA, c.SessionID, jsonStrings(shaPrefixes(c.SHA))).Scan(&old)
	switch {
	case err == nil && old == c.SHA:
		return false, nil
	case err == nil:
		// old ist die kurze Fassung von c.SHA.
		if _, err := tx.Exec(`UPDATE session_commits SET sha=?, committed_at=CASE WHEN committed_at='' THEN ? ELSE committed_at END,
			patch_id=CASE WHEN patch_id='' THEN ? ELSE patch_id END
			WHERE session_id=? AND sha=?`, c.SHA, c.CommittedAt, c.PatchID, c.SessionID, old); err != nil {
			return false, err
		}
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	var one int
	err = tx.QueryRow(sqlOwnLongerSHA, c.SessionID, c.SHA, c.SHA+"g").Scan(&one)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	if c.Op == "" {
		c.Op = "commit"
	}
	weak := 0
	if c.Weak {
		weak = 1
	}
	if _, err := tx.Exec(`INSERT INTO session_commits(session_id,project,sha,source,op,chunk_seq,patch_id,committed_at,recorded_at,weak)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, c.SessionID, c.Project, c.SHA, c.Source, c.Op, c.ChunkSeq, c.PatchID, c.CommittedAt, recordedAt, weak); err != nil {
		return false, err
	}
	return true, nil
}

// Die Abfragen von putSessionCommit; ein Test prüft ihre Pläne.
const (
	// dieselbe SHA oder eine kürzere Fassung (die längste zuerst).
	sqlOwnShorterSHA = `SELECT sha FROM session_commits WHERE session_id=? AND sha IN (SELECT value FROM json_each(?))
		ORDER BY length(sha) DESC LIMIT 1`
	// eine längere SHA, die mit der neuen beginnt.
	sqlOwnLongerSHA = `SELECT 1 FROM session_commits WHERE session_id=? AND sha > ? AND sha < ? LIMIT 1`
)

// shaPrefixes nennt alle Präfixe einer SHA ab sieben Zeichen, sie selbst
// eingeschlossen: so findet ein Index die kurze Fassung einer vollen SHA.
func shaPrefixes(sha string) []string {
	out := make([]string, 0, len(sha))
	for n := gitanchor.MinPrefix; n <= len(sha); n++ {
		out = append(out, sha[:n])
	}
	return out
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
	// Weak: die Verbindung beruht nur auf einem Zeitfenster (matched), und der
	// Autor des Commits ließ sich nicht gegen das Konto der Session prüfen. Sie ist
	// ein Kandidat, kein Beleg.
	Weak bool
	// Origins sagt nur bei der Abfrage zu einem Request, auf welchem Weg die Session
	// zu ihm kommt: RequestOriginPR oder RequestOriginEvidence.
	Origins []string
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
	cur.Weak = cur.Weak && n.Weak
	cur.Sources = uniqueStrings(append(cur.Sources, n.Sources...))
	cur.Origins = uniqueStrings(append(cur.Origins, n.Origins...))
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
		sort.Strings(v.Origins)
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

// sqlPullSessionCommits: die Commits eines PRs gegen die der Sessions; weak ist 1,
// wenn alle Zeilen einer Quelle schwach sind.
//
// CROSS JOIN legt die Reihenfolge fest: von den Commits des PRs (Primärschlüssel) aus
// je SHA in den Index von session_commits, nicht über alle Zeilen des Projekts.
const sqlPullSessionCommits = `SELECT sc.session_id, sc.source, COUNT(DISTINCT sc.sha), MIN(COALESCE(NULLIF(sc.committed_at,''), sc.recorded_at)), MIN(sc.weak)
	FROM git_pr_commits k CROSS JOIN session_commits sc ON sc.project=k.project AND sc.sha=k.sha
	WHERE k.project=? AND k.pr=? GROUP BY sc.session_id, sc.source`

func (s *Store) sessionsForPull(project string, number int64, via string, set linkSet) error {
	// Ein "merged" gilt nur, solange der PR, wenn der Server ihn kennt, wirklich
	// gemergt ist: die Meldung stammt vom Collector und ist eine Behauptung.
	//
	// Ein "created" ist ebenfalls eine Behauptung: wer eine fremde PR-Adresse als
	// Ausgabe von gh pr create meldet, würde sich sonst an den PR hängen. Kennt der
	// Server den Autor des PRs, wird er wie der eines Commits gegen das GitHub-Konto
	// des Besitzers der Session gelegt: ein anderer Login schließt die Verbindung aus,
	// ein nicht prüfbarer macht sie schwach.
	rows, err := s.db.Query(`SELECT sp.session_id, sp.source, sp.recorded_at,
			CASE WHEN s.account_id=0 THEN ? ELSE s.account_id END, COALESCE(p.author,'')
		FROM session_prs sp JOIN sessions s ON s.id=sp.session_id
		LEFT JOIN git_pull_requests p ON p.project=sp.project AND p.number=sp.pr
		WHERE sp.project=? AND sp.pr=?
		AND (sp.source != 'merged' OR COALESCE(p.state, 'merged') = 'merged')`, instanceOwnerID(s.db), project, number)
	if err != nil {
		return err
	}
	type prClaim struct {
		id           int64
		src, at, who string
		acct         int64
	}
	var claims []prClaim
	for rows.Next() {
		var c prClaim
		if err := rows.Scan(&c.id, &c.src, &c.at, &c.acct, &c.who); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	m := newMatcher(s.db, instanceOwnerID(s.db))
	for _, c := range claims {
		weak := false
		if c.src == PRSourceCreated {
			lvl, err := m.fit(c.acct, c.who)
			if err != nil {
				return err
			}
			if lvl == fitNone {
				continue
			}
			weak = lvl == fitWeak
		}
		set.add(GitSessionLink{SessionID: c.id, Sources: []string{c.src}, Via: via, PR: number, At: c.at, Weak: weak})
	}
	rows, err = s.db.Query(sqlPullSessionCommits, project, number)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var src, at string
		var n, weak int
		if err := rows.Scan(&id, &src, &n, &at, &weak); err != nil {
			return err
		}
		set.add(GitSessionLink{SessionID: id, Sources: SourceNames(src), Via: via, PR: number, Commits: n, At: at, Weak: weak == 1})
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
	// angefragt wird, in jeder vollen, die mit ihr beginnt. Zwei Abfragen statt
	// eines OR: jede liest den Index (project, sha), das OR las alle Zeilen.
	type directRow struct {
		id                  int64
		src, patch, at, sha string
		seq                 int
		weak                bool
	}
	var found []directRow
	for _, q := range []struct {
		query string
		args  []any
	}{
		{sqlSessionCommitsExact, []any{project, jsonStrings(shaPrefixes(sha))}},
		{sqlSessionCommitsLonger, []any{project, sha, sha + "g"}},
	} {
		rows, err := s.db.Query(q.query, q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r directRow
			if err := rows.Scan(&r.id, &r.src, &r.patch, &r.at, &r.sha, &r.seq, &r.weak); err != nil {
				rows.Close()
				return nil, err
			}
			found = append(found, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	var patches []string
	for _, r := range found {
		// Eine gemeldete kurze SHA, der die bekannten Commits mit diesem Präfix
		// alle widersprechen, ist eine nicht belegte Behauptung.
		if r.src == CommitSourceReported && len(r.sha) < 40 {
			bad, err := reportedContradicted(s.db, project, r.id, r.seq, r.sha)
			if err != nil {
				return nil, err
			}
			if bad {
				continue
			}
		}
		set.add(GitSessionLink{SessionID: r.id, Sources: SourceNames(r.src), Commits: 1, At: r.at, Weak: r.weak})
		if r.patch != "" {
			patches = append(patches, r.patch)
		}
	}
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
				set.add(GitSessionLink{SessionID: id, Sources: SourceNames(src), Via: LinkViaPatch, Commits: 1, At: at})
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

// Die Abfragen, die eine SHA gegen einen Index legen. Je Tabelle zwei: die SHA
// und ihre kürzeren Fassungen (IN über die Präfixe), und die längeren, die mit ihr
// beginnen (Bereich). Ein OR aus beiden las alle Zeilen des Projekts; ein Test
// prüft die Pläne.
const (
	sqlSessionCommitsCols   = `SELECT session_id, source, patch_id, COALESCE(NULLIF(committed_at,''), recorded_at), sha, chunk_seq, weak FROM session_commits`
	sqlSessionCommitsExact  = sqlSessionCommitsCols + ` WHERE project=? AND sha IN (SELECT value FROM json_each(?))`
	sqlSessionCommitsLonger = sqlSessionCommitsCols + ` WHERE project=? AND sha > ? AND sha < ?`

	sqlPullsMergeExact  = `SELECT number FROM git_pull_requests WHERE project=? AND merge_sha IN (SELECT value FROM json_each(?))`
	sqlPullsMergeLonger = `SELECT number FROM git_pull_requests WHERE project=? AND merge_sha > ? AND merge_sha < ?`

	// Von der Liste der Präfixe aus: ein Nachschlagen im Index (project, sha) je Präfix.
	sqlPullsCarryExact  = `SELECT DISTINCT k.pr FROM json_each(?) j CROSS JOIN git_pr_commits k ON k.project=? AND k.sha=j.value`
	sqlPullsCarryLonger = `SELECT DISTINCT pr FROM git_pr_commits WHERE project=? AND sha > ? AND sha < ?`
)

// pullsWithMerge nennt die PRs, deren Merge-Commit die SHA ist (voll oder als
// Präfix in beiden Richtungen, denn merge_sha ist nicht immer voll).
func (s *Store) pullsWithMerge(project, sha string) ([]int64, error) {
	return s.pullsByPrefix(sqlPullsMergeExact, sqlPullsMergeLonger, project, sha)
}

func (s *Store) pullsCarrying(project, sha string) ([]int64, error) {
	return s.pullsByPrefix(sqlPullsCarryExact, sqlPullsCarryLonger, project, sha)
}

func (s *Store) pullsByPrefix(exact, longer, project, sha string) ([]int64, error) {
	a, err := s.pullNumbers(exact, exactArgs(exact, project, sha)...)
	if err != nil {
		return nil, err
	}
	b, err := s.pullNumbers(longer, project, sha, sha+"g")
	if err != nil {
		return nil, err
	}
	return append(a, b...), nil
}

// exactArgs: die Argumente der Abfrage nach der SHA und ihren kürzeren Fassungen;
// bei sqlPullsCarryExact steht die Liste vor dem Projekt.
func exactArgs(query, project, sha string) []any {
	if query == sqlPullsCarryExact {
		return []any{jsonStrings(shaPrefixes(sha)), project}
	}
	return []any{project, jsonStrings(shaPrefixes(sha))}
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
	// Weak: die Verbindung beruht nur auf einem Zeitfenster, und der Autor des
	// Commits ließ sich nicht gegen die Person prüfen. Ein Kandidat, kein Beleg.
	Weak bool `json:"weak,omitempty"`
	// Origins nennt bei einem Request, auf welchem Weg die Session zu ihm kommt:
	// pr (ein PR im Paket) oder evidence (ein Commit-Nachweis).
	Origins []string `json:"origins,omitempty"`
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
		v := GitSessionLinkView{Sources: l.Sources, Via: l.Via, PR: l.PR, Commits: l.Commits, Weak: l.Weak, Origins: l.Origins}
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
