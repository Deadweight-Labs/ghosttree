package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixture: das Projekt prProject (Organisation dw, Owner robin) mit der
// Installation prInstall, darin
//
//	robin (person:1)  Owner, besitzt die Session "s-robin" (privat, Branch feat/a)
//	anna  (person:2)  Mitglied, besitzt "s-anna" (für das Projekt freigegeben, Branch feat/b)
//	ben   (person:3)  Gast
//	mallory (person:4) kein Zugriff
//
// und ein zweites Projekt mit der Session "s-other" von robin.
type gitFix struct {
	st                          *Store
	robin, robin2, anna, other  Session
	otherProject                string
	robinAcc, annaAcc, otherAcc int64
}

const gitOtherProject = "github.com/deadweight-labs/other"

func gitFixture(t *testing.T) *gitFix {
	t.Helper()
	st := prFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	org, ok := st.ProjectByRemote(prProject)
	if !ok {
		t.Fatal("project unknown")
	}
	// Ben tritt der Organisation bei und wird im Projekt Gast.
	code, _, err := st.CreateInvitation("person:1", org.OrgID, "", OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptInvitation("person:3", code); err != nil {
		t.Fatal(err)
	}
	for who, role := range map[string]string{"person:2": RoleMember, "person:3": RoleGuest} {
		if err := st.SetProjectRole("person:1", prProject, who, role, false, RoleViaAPI); err != nil {
			t.Fatal(err)
		}
	}
	prompt := func(text string) []Chunk {
		return chunksOf(userLine("2026-11-08T16:00:00Z", text))
	}
	f := &gitFix{st: st, otherProject: gitOtherProject}
	f.robin = addSession(t, st, "s-robin", 1, prProject, "", VisPrivate, prompt("private prompt of robin"))
	f.robin2 = addSession(t, st, "s-robin-2", 1, prProject, "", VisPrivate, prompt("second private prompt of robin"))
	f.anna = addSession(t, st, "s-anna", 2, prProject, "", VisProject, prompt("shared prompt of anna"))
	f.other = addSession(t, st, "s-other", 1, gitOtherProject, "", VisPrivate, prompt("prompt in the other project"))
	for _, s := range []struct {
		id     int64
		branch string
	}{{f.robin.ID, "feat/a"}, {f.robin2.ID, "feat/a"}, {f.anna.ID, "feat/b"}} {
		if _, err := st.db.Exec(`UPDATE sessions SET branch=? WHERE id=?`, s.branch, s.id); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func sha40(prefix string) string {
	return prefix + strings.Repeat("0", 40-len(prefix))
}

func (f *gitFix) record(t *testing.T, account int64, events ...SessionGitEvent) SessionGitResult {
	t.Helper()
	res, err := f.st.RecordSessionGitEvents(account, events)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func commitEvent(s Session, seq int, start, end, branch string, shas ...string) SessionGitEvent {
	return SessionGitEvent{Harness: s.Harness, SessionExternalID: s.ExternalID, Seq: seq, Kind: GitEventCommit, Op: "commit",
		StartedAt: start, EndedAt: end, OK: true, Branch: branch, SHAs: shas}
}

func (f *gitFix) pull(t *testing.T, number int64, head string, commits ...PullCommit) {
	t.Helper()
	if _, err := f.st.RecordPullRequest(prEvent(number, "opened", "open", head)); err != nil {
		t.Fatal(err)
	}
	f.commits(t, number, commits...)
}

func (f *gitFix) commits(t *testing.T, number int64, commits ...PullCommit) {
	t.Helper()
	res, err := f.st.RecordPullCommits(PullCommitsChange{InstallationID: prInstall, Remote: prProject, Number: number, Commits: commits})
	if err != nil || !res.Applied {
		t.Fatalf("commits of PR %d: %+v %v", number, res, err)
	}
}

// owners sagt, welche Session (nach external_id) die SHA mit welcher Quelle hat.
func (f *gitFix) owners(t *testing.T, sha string) map[string]string {
	t.Helper()
	rows, err := f.st.db.Query(`SELECT s.external_id, c.source FROM session_commits c JOIN sessions s ON s.id=c.session_id WHERE c.sha=?`, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ext, src string
		if err := rows.Scan(&ext, &src); err != nil {
			t.Fatal(err)
		}
		out[ext] = src
	}
	return out
}

func (f *gitFix) resolved(t *testing.T, s Session, seq int) bool {
	t.Helper()
	var r bool
	if err := f.st.db.QueryRow(`SELECT resolved FROM session_git_events WHERE session_id=? AND seq=? AND kind='commit'`, s.ID, seq).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSessionGitSchemaIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`DROP TABLE session_git_events`, `DROP TABLE session_commits`, `DROP TABLE session_prs`} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		for _, table := range []string{"session_git_events", "session_commits", "session_prs"} {
			var n int
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
				t.Fatalf("open %d: table %s: %v", round, table, err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordSessionGitEventsIsIdempotentAndWritesNothingForAForeignSession(t *testing.T) {
	f := gitFixture(t)
	ev := commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a")
	if res := f.record(t, 1, ev); res.Recorded != 1 {
		t.Fatalf("first: %+v", res)
	}
	if res := f.record(t, 1, ev); res.Recorded != 0 {
		t.Fatalf("a repeated batch changes nothing: %+v", res)
	}
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_git_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("one row, got %d", n)
	}
	// Anna darf an robins Session nichts melden, und kein Ereignis des Stapels
	// kommt durch, auch nicht das zu ihrer eigenen.
	own := commitEvent(f.anna, 1, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/b")
	foreign := commitEvent(f.robin, 9, "2026-11-08T16:07:00Z", "2026-11-08T16:07:01Z", "feat/a", "abcdef0")
	if _, err := f.st.RecordSessionGitEvents(2, []SessionGitEvent{own, foreign}); !errors.Is(err, ErrGitEventSession) {
		t.Fatalf("a foreign session: %v", err)
	}
	// Eine unbekannte Session sieht gleich aus.
	unknown := commitEvent(Session{Harness: "claude-code", ExternalID: "nobody"}, 1, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "")
	if _, err := f.st.RecordSessionGitEvents(1, []SessionGitEvent{unknown}); !errors.Is(err, ErrGitEventSession) {
		t.Fatalf("an unknown session: %v", err)
	}
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_git_events`).Scan(&n)
	var commits int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&commits)
	if n != 1 || commits != 0 {
		t.Fatalf("nothing of the refused batches may stay: events %d, commits %d", n, commits)
	}
	// Dieselbe Kennung bei einem anderen Harness ist eine andere Session.
	other := ev
	other.Harness = "codex"
	if _, err := f.st.RecordSessionGitEvents(1, []SessionGitEvent{other}); !errors.Is(err, ErrGitEventSession) {
		t.Fatalf("the session of another harness: %v", err)
	}
}

func TestRecordSessionGitEventsRejectsInvalidInput(t *testing.T) {
	f := gitFixture(t)
	good := commitEvent(f.robin, 1, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "")
	for name, mutate := range map[string]func(*SessionGitEvent){
		"harness":      func(e *SessionGitEvent) { e.Harness = "vim" },
		"kind":         func(e *SessionGitEvent) { e.Kind = "reset" },
		"op on a push": func(e *SessionGitEvent) { e.Kind, e.Op = GitEventPush, "commit" },
		"op":           func(e *SessionGitEvent) { e.Op = "format-patch" },
		"time":         func(e *SessionGitEvent) { e.StartedAt = "yesterday" },
		"future":       func(e *SessionGitEvent) { e.StartedAt = "2999-01-01T00:00:00Z" },
		"seq":          func(e *SessionGitEvent) { e.Seq = -1 },
		"remote":       func(e *SessionGitEvent) { e.Kind, e.Op, e.PRRemote = GitEventPRCreate, "", "not a remote" },
	} {
		e := good
		mutate(&e)
		if _, err := f.st.RecordSessionGitEvents(1, []SessionGitEvent{e}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	tooMany := make([]SessionGitEvent, MaxSessionGitEvents+1)
	for i := range tooMany {
		tooMany[i] = good
	}
	if _, err := f.st.RecordSessionGitEvents(1, tooMany); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("too many: %v", err)
	}
}

func TestMatchResolvesAQuietCommitByItsTimeWindow(t *testing.T) {
	f := gitFixture(t)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, Title: "fix", CommittedAt: "2026-11-08T16:06:34Z"})
	// git commit -q: nur Aufruf und Ergebnis, 16:06:33,854 bis 16:06:33,965.
	res := f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a"))
	if res.Matched != 1 {
		t.Fatalf("matched: %+v", res)
	}
	if got := f.owners(t, c1); len(got) != 1 || got["s-robin"] != CommitSourceMatched {
		t.Fatalf("owners of the commit: %v", got)
	}
	if !f.resolved(t, f.robin, 4) {
		t.Error("the event is resolved")
	}
	// Eine Wiederholung ändert nichts.
	if res := f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a")); res.Matched != 0 {
		t.Fatalf("repeat: %+v", res)
	}
}

func TestACommitThatArrivesLaterResolvesAnOldEvent(t *testing.T) {
	f := gitFixture(t)
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a"))
	if f.resolved(t, f.robin, 4) {
		t.Fatal("nothing to match yet")
	}
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T16:06:33Z"})
	if got := f.owners(t, c1); got["s-robin"] != CommitSourceMatched {
		t.Fatalf("a late PR commit resolves the old event: %v", got)
	}
	if !f.resolved(t, f.robin, 4) {
		t.Error("resolved after the sync")
	}
	// Dieselbe Synchronisierung noch einmal: keine Dublette.
	f.commits(t, 7, PullCommit{SHA: c1, CommittedAt: "2026-11-08T16:06:33Z"})
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows: %d", n)
	}
}

func TestTwoSessionsInTheSameWindowAreSeparatedByTheirBranch(t *testing.T) {
	f := gitFixture(t)
	ca, cb := sha40("ca"), sha40("cb")
	f.pull(t, 7, "feat/a", PullCommit{SHA: ca, CommittedAt: "2026-11-08T16:06:34Z"})
	f.pull(t, 8, "feat/b", PullCommit{SHA: cb, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a"))
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", "feat/b"))
	if got := f.owners(t, ca); len(got) != 1 || got["s-robin"] == "" {
		t.Errorf("commit of feat/a: %v", got)
	}
	if got := f.owners(t, cb); len(got) != 1 || got["s-anna"] == "" {
		t.Errorf("commit of feat/b: %v", got)
	}
}

func TestTwoSessionsOfOneAccountOnTheSameBranchLeaveTheEventOpen(t *testing.T) {
	f := gitFixture(t)
	c := sha40("cc")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a"))
	if got := f.owners(t, c); got["s-robin"] == "" {
		t.Fatalf("alone in the window, the session gets the commit: %v", got)
	}
	// Ein Worker desselben Kontos trifft dasselbe Fenster auf demselben Branch:
	// jetzt ist es mehrdeutig, und die frühere Zuordnung wird zurückgenommen,
	// unabhängig davon, wer zuerst kam.
	f.record(t, 1, commitEvent(f.robin2, 2, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", "feat/a"))
	if got := f.owners(t, c); len(got) != 0 {
		t.Fatalf("ambiguous: nobody gets the commit, got %v", got)
	}
	if f.resolved(t, f.robin, 4) || f.resolved(t, f.robin2, 2) {
		t.Error("both events stay open")
	}
}

func TestAnotherAccountCannotTakeAnAssignmentBackOrStealIt(t *testing.T) {
	f := gitFixture(t)
	c := sha40("cc")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a"))
	// Annas Vorgang trifft dasselbe Fenster auf demselben Branch: ein fremdes
	// Konto ändert die Zuordnung nicht.
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", "feat/a"))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceMatched {
		t.Fatalf("robin keeps the commit: %v", got)
	}
	// Auch ein Vorgang, dessen Branch besser zu passen scheint, nimmt sie nicht.
	f.st.db.Exec(`UPDATE sessions SET branch='' WHERE id=?`, f.robin.ID)
	f.record(t, 2, commitEvent(f.anna, 3, "2026-11-08T16:06:33.950Z", "2026-11-08T16:06:34.250Z", "feat/a"))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceMatched {
		t.Fatalf("robin still keeps the commit: %v", got)
	}
}

func TestAForgedReportedShaNeitherStealsNorDeletesAnotherAccountsCommit(t *testing.T) {
	f := gitFixture(t)
	c := "7a8b9c0d1e2f30415263748596a7b8c9d0e1f203"
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a"))
	// Anna meldet die SHA aus einem Aufruf, der Stunden davor lief: das Fenster
	// widerspricht der Zeit des Commits, die Zeile wird verworfen.
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T12:00:00Z", "2026-11-08T12:00:01Z", "feat/a", "7a8b9c0"))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceMatched {
		t.Fatalf("a contradicted report is dropped: %v", got)
	}
	// Der Präfix ist kurz: die Zeile bleibt in der Datenbank (der echte Commit
	// könnte noch unbekannt sein), die Leser sehen sie aber nicht.
	links, err := f.st.SessionsForCommit(prProject, c)
	if err != nil || len(links) != 1 || links[0].SessionID != f.robin.ID {
		t.Fatalf("a contradicted short report is invisible: %+v %v", links, err)
	}
	// Anna meldet mit einem Fenster, das die Zeit enthält (so lang, wie ein
	// Fenster sein darf): die Meldung ist plausibel und bleibt, aber Robins
	// Zuordnung wird dadurch nicht gelöscht.
	f.record(t, 2, commitEvent(f.anna, 3, "2026-11-08T16:00:00Z", "2026-11-08T17:00:00Z", "feat/a", "7a8b9c0"))
	got := f.owners(t, c)
	if got["s-robin"] != CommitSourceMatched {
		t.Fatalf("a plausible foreign report does not remove robin's assignment: %v", got)
	}
}

// Zwei Commits desselben Präfixes: eine kurze gemeldete SHA gehört zu dem, der in
// ihr Fenster passt; ein anderer mit demselben Präfix verwirft sie nicht (der
// echte Commit kann noch unbekannt sein) und verlängert sie nicht.
func TestAShortShaThatSeveralCommitsShareIsOnlyJudgedByTheOneInItsWindow(t *testing.T) {
	f := gitFixture(t)
	mine := "5555555" + strings.Repeat("a", 33)
	theirs := "5555555" + strings.Repeat("b", 33)
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a", "5555555"))
	// Erst der fremde Commit mit demselben Präfix, zu einer anderen Zeit.
	f.pull(t, 7, "feat/a", PullCommit{SHA: theirs, CommittedAt: "2026-11-08T09:00:00Z"})
	if got := f.owners(t, "5555555"); got["s-robin"] != CommitSourceReported {
		t.Fatalf("a commit that only shares the prefix must not drop the row: %v", got)
	}
	if links, _ := f.st.SessionsForCommit(prProject, theirs); len(links) != 0 {
		t.Fatalf("but the other commit is not robin's: %+v", links)
	}
	// Dann der eigene: er passt ins Fenster, die Kurzfassung wird zu seiner vollen SHA.
	f.commits(t, 7, PullCommit{SHA: theirs, CommittedAt: "2026-11-08T09:00:00Z"}, PullCommit{SHA: mine, CommittedAt: "2026-11-08T16:06:34Z"})
	if got := f.owners(t, mine); got["s-robin"] != CommitSourceReported {
		t.Fatalf("upgraded to the commit in its window: %v", got)
	}
	if got := f.owners(t, theirs); len(got) != 0 {
		t.Fatalf("the other commit stays without a session: %v", got)
	}
	if got := f.owners(t, "5555555"); len(got) != 0 {
		t.Fatalf("no short row remains: %v", got)
	}
}

// Ein langer Präfix ist eindeutig genug, dass ein Widerspruch die Zeile verwirft.
func TestALongReportedShaThatContradictsTheCommitTimeIsDropped(t *testing.T) {
	f := gitFixture(t)
	c := "6666666666666" + strings.Repeat("c", 27)
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T12:00:00Z", "2026-11-08T12:00:01Z", "feat/b", c[:13]))
	if got := f.owners(t, c[:13]); len(got) != 0 {
		t.Fatalf("a contradicted long report is dropped: %v", got)
	}
	if got := f.owners(t, c); len(got) != 0 {
		t.Fatalf("and does not turn into a claim: %v", got)
	}
}

func TestAWindowIsCutAtThirtyMinutes(t *testing.T) {
	f := gitFixture(t)
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T10:00:00Z", "2026-11-08T14:00:00Z", "feat/a"))
	var ended string
	f.st.db.QueryRow(`SELECT ended_at FROM session_git_events WHERE session_id=? AND seq=4`, f.robin.ID).Scan(&ended)
	if ended != "2026-11-08T10:30:00Z" {
		t.Fatalf("a window longer than %v is cut: %s", maxGitCallWindow, ended)
	}
}

func TestAnEventWhoseBranchContradictsThePullRequestGetsNothing(t *testing.T) {
	f := gitFixture(t)
	c := sha40("cd")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	// Annas Session stand auf feat/b: ein Commit des PRs von feat/a in ihrem
	// Fenster ist zufällig, nicht ihrer.
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", "feat/b"))
	if got := f.owners(t, c); len(got) != 0 {
		t.Fatalf("a contradicting branch: %v", got)
	}
	// Ohne bekannten Branch gilt der einzige Vorgang im Fenster.
	f.st.db.Exec(`UPDATE sessions SET branch='' WHERE id=?`, f.robin.ID)
	f.record(t, 1, commitEvent(f.robin, 5, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", ""))
	if got := f.owners(t, c); got["s-robin"] == "" || got["s-anna"] != "" {
		t.Fatalf("the unknown branch matches only as the sole candidate, but annas event contradicts and drops out: %v", got)
	}
}

func TestOutsideTheWindowOrFailedOrOtherProjectDoesNotMatch(t *testing.T) {
	f := gitFixture(t)
	c := sha40("ce")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	// Fünf Sekunden daneben.
	f.record(t, 1, commitEvent(f.robin, 1, "2026-11-08T16:06:40Z", "2026-11-08T16:06:41Z", "feat/a"))
	// Im Fenster, aber der Aufruf ist fehlgeschlagen.
	failed := commitEvent(f.robin, 2, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/a")
	failed.OK = false
	f.record(t, 1, failed)
	// Im Fenster, aber in einem anderen Projekt.
	f.record(t, 1, commitEvent(f.other, 3, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/a"))
	// Ein Push und ein gh pr create sind keine Commits.
	push := commitEvent(f.robin, 4, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/a")
	push.Kind, push.Op = GitEventPush, ""
	f.record(t, 1, push)
	if got := f.owners(t, c); len(got) != 0 {
		t.Fatalf("no match expected: %v", got)
	}
}

func TestAReportedShaIsKeptAndUpgradedToTheFullOne(t *testing.T) {
	f := gitFixture(t)
	full := "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	// Gemeldet, bevor der Server den Commit kennt: kurz, Quelle reported.
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:10:00.100Z", "2026-11-08T16:10:00.420Z", "feat/a", "1a2b3c4"))
	if got := f.owners(t, "1a2b3c4"); got["s-robin"] != CommitSourceReported {
		t.Fatalf("reported short sha: %v", got)
	}
	if !f.resolved(t, f.robin, 4) {
		t.Error("an event with a reported sha is resolved")
	}
	f.pull(t, 7, "feat/a", PullCommit{SHA: full, CommittedAt: "2026-11-08T16:10:00Z"})
	if got := f.owners(t, "1a2b3c4"); len(got) != 0 {
		t.Fatalf("the short sha is replaced: %v", got)
	}
	if got := f.owners(t, full); got["s-robin"] != CommitSourceReported {
		t.Fatalf("full sha, source stays reported: %v", got)
	}
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
	if n != 1 {
		t.Fatalf("one row, got %d", n)
	}
	// Der Commit ist durch die gemeldete SHA belegt: ein zweiter Vorgang im
	// selben Fenster bekommt ihn nicht.
	f.record(t, 2, commitEvent(f.anna, 2, "2026-11-08T16:10:00.000Z", "2026-11-08T16:10:00.500Z", "feat/a"))
	if got := f.owners(t, full); len(got) != 1 {
		t.Fatalf("a reported sha is not contested by the window: %v", got)
	}
}

func TestAReportedShaFoundWhenThePullRequestAlreadyHasTheCommit(t *testing.T) {
	f := gitFixture(t)
	full := "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
	f.pull(t, 7, "feat/a", PullCommit{SHA: full, CommittedAt: "2026-11-08T16:10:00Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:10:00.100Z", "2026-11-08T16:10:00.420Z", "feat/a", "9f8e7d6"))
	if got := f.owners(t, full); got["s-robin"] != CommitSourceReported {
		t.Fatalf("upgraded at once: %v", got)
	}
}

func TestPullRequestsOfASessionComeFromTheCreateAndMergeCalls(t *testing.T) {
	f := gitFixture(t)
	create := SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-robin", Seq: 15, Kind: GitEventPRCreate, StartedAt: "2026-11-08T16:13:00Z",
		EndedAt: "2026-11-08T16:13:03Z", OK: true, PR: 7, PRRemote: prProject}
	merge := create
	merge.Seq, merge.Kind = 17, GitEventPRMerge
	// Ein PR in einem anderen Repo erscheint nicht am Projekt der Session.
	foreign := create
	foreign.Seq, foreign.PR, foreign.PRRemote = 19, 55, "github.com/someone/else"
	// gh pr merge ohne -R nennt kein Repo; der Befehl kann in einem anderen
	// Checkout gelaufen sein, also wird die Nummer keinem Projekt zugeschlagen.
	unnamed := merge
	unnamed.Seq, unnamed.PR, unnamed.PRRemote = 21, 8, ""
	f.record(t, 1, create, merge, foreign, unnamed)
	links, err := f.st.SessionsForPull(prProject, 7)
	if err != nil || len(links) != 1 || links[0].SessionID != f.robin.ID || strings.Join(links[0].Sources, ",") != "created,merged" {
		t.Fatalf("created and merged: %+v %v", links, err)
	}
	for _, n := range []int64{55, 8} {
		if links, _ := f.st.SessionsForPull(prProject, n); len(links) != 0 {
			t.Fatalf("PR %d must not be attributed: %+v", n, links)
		}
	}
	// Der Collector behauptet einen Merge; kennt der Server den PR und er ist
	// offen, gilt die Behauptung nicht. Erst ein gemergter PR bestätigt sie.
	if _, err := f.st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
		t.Fatal(err)
	}
	links, _ = f.st.SessionsForPull(prProject, 7)
	if len(links) != 1 || strings.Join(links[0].Sources, ",") != "created" {
		t.Fatalf("an open PR was not merged: %+v", links)
	}
	if _, err := f.st.RecordPullRequest(prEvent(7, "closed", "merged", "feat/a")); err != nil {
		t.Fatal(err)
	}
	links, _ = f.st.SessionsForPull(prProject, 7)
	if len(links) != 1 || strings.Join(links[0].Sources, ",") != "created,merged" {
		t.Fatalf("a merged PR confirms the claim: %+v", links)
	}
}

func TestSessionsForCommitResolveInThreeStages(t *testing.T) {
	f := gitFixture(t)
	branchCommit, other, merge := sha40("b1"), sha40("b2"), sha40("fe")
	f.pull(t, 7, "feat/a", PullCommit{SHA: branchCommit, CommittedAt: "2026-11-08T16:06:34Z"}, PullCommit{SHA: other, CommittedAt: "2026-11-08T16:30:00Z"})
	// Stufe 1: robin hat die Branch-Commits (gemeldet und zugeordnet).
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a"))
	// Annas Session hat nur den PR bedient: angelegt.
	f.record(t, 2, SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-anna", Seq: 3, Kind: GitEventPRCreate, StartedAt: "2026-11-08T16:40:00Z",
		EndedAt: "2026-11-08T16:40:02Z", OK: true, PR: 7, PRRemote: prProject})

	direct, err := f.st.SessionsForCommit(prProject, branchCommit)
	if err != nil || len(direct) != 1 || direct[0].SessionID != f.robin.ID || direct[0].Via != "" || direct[0].Sources[0] != CommitSourceMatched {
		t.Fatalf("stage 1: %+v %v", direct, err)
	}
	// Auch mit einem Präfix von zwölf Zeichen.
	if got, _ := f.st.SessionsForCommit(prProject, branchCommit[:12]); len(got) != 1 || got[0].SessionID != f.robin.ID {
		t.Fatalf("prefix: %+v", got)
	}
	// Stufe 3: ein Commit des PRs, den keine Session kennt: die Sessions des PRs, "über PR".
	viaPR, err := f.st.SessionsForCommit(prProject, other)
	if err != nil || len(viaPR) != 2 {
		t.Fatalf("stage 3: %+v %v", viaPR, err)
	}
	for _, l := range viaPR {
		if l.Via != LinkViaPR || l.PR != 7 {
			t.Errorf("must say via PR 7: %+v", l)
		}
	}
	// Stufe 2: Squash-Merge. Der Commit auf dem Hauptzweig stammt von GitHub; die
	// Frage nach der merge_sha nennt die Sessions der Branch-Commits.
	ev := prEvent(7, "closed", "merged", "feat/a")
	ev.MergeSHA = merge
	if _, err := f.st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	squash, err := f.st.SessionsForCommit(prProject, merge)
	if err != nil || len(squash) != 2 {
		t.Fatalf("stage 2: %+v %v", squash, err)
	}
	ids := map[int64]GitSessionLink{}
	for _, l := range squash {
		ids[l.SessionID] = l
		if l.Via != LinkViaPR || l.PR != 7 {
			t.Errorf("squash result must say via PR 7: %+v", l)
		}
	}
	if ids[f.robin.ID].Commits != 1 || ids[f.anna.ID].Commits != 0 {
		t.Errorf("robin made a commit of the PR, anna only created it: %+v", ids)
	}
	// Eine unbekannte SHA und ein zu kurzer Präfix.
	if got, _ := f.st.SessionsForCommit(prProject, sha40("dd")); len(got) != 0 {
		t.Errorf("unknown: %+v", got)
	}
	if got, _ := f.st.SessionsForCommit(prProject, "abc"); len(got) != 0 {
		t.Errorf("too short: %+v", got)
	}
	// Ein anderes Projekt sieht nichts, auch nicht mit derselben SHA.
	if got, _ := f.st.SessionsForCommit(gitOtherProject, branchCommit); len(got) != 0 {
		t.Errorf("other project: %+v", got)
	}
}

func TestSessionsForCommitFindTheOriginalSessionByPatchID(t *testing.T) {
	f := gitFixture(t)
	orig, rebased := sha40("a1"), sha40("a2")
	// P2 liefert die Patch-ID; hier steht sie von Hand.
	for _, r := range []struct {
		session Session
		sha     string
	}{{f.robin, orig}, {f.anna, rebased}} {
		if _, err := f.st.db.Exec(`INSERT INTO session_commits(session_id,project,sha,source,op,patch_id,committed_at,recorded_at)
			VALUES(?,?,?,'observed','commit','patch-1','2026-11-08T16:00:00Z','2026-11-08T16:00:00Z')`, r.session.ID, prProject, r.sha); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.st.SessionsForCommit(prProject, rebased)
	if err != nil || len(got) != 2 {
		t.Fatalf("rebased: %+v %v", got, err)
	}
	for _, l := range got {
		switch l.SessionID {
		case f.anna.ID:
			if l.Via != "" {
				t.Errorf("the rebasing session has the sha itself: %+v", l)
			}
		case f.robin.ID:
			if l.Via != LinkViaPatch {
				t.Errorf("the original session is found through the patch: %+v", l)
			}
		}
	}
}

func TestSessionCommitsListsWhatASessionMade(t *testing.T) {
	f := gitFixture(t)
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T16:10:00.100Z", "2026-11-08T16:10:00.420Z", "feat/a", "1a2b3c4"),
		commitEvent(f.robin, 2, "2026-11-08T16:20:00Z", "2026-11-08T16:20:01Z", "feat/a", "5d6e7f8"))
	rows, err := f.st.SessionCommits(f.robin.ID)
	if err != nil || len(rows) != 2 || rows[0].SHA != "1a2b3c4" || rows[1].SHA != "5d6e7f8" || rows[0].Source != CommitSourceReported {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows, _ := f.st.SessionCommits(f.anna.ID); len(rows) != 0 {
		t.Fatalf("anna made none: %+v", rows)
	}
}

// ------------------------------------------------------------ Sichtbarkeit

// linkedFixture: ein PR 7 mit je einem Commit aus robins privater, annas
// freigegebener und der Session im anderen Projekt.
func linkedFixture(t *testing.T) (*gitFix, string) {
	t.Helper()
	f := gitFixture(t)
	c := sha40("e1")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T16:06:33.854Z", "2026-11-08T16:06:33.965Z", "feat/a", c[:7]))
	f.record(t, 2, SessionGitEvent{Harness: "claude-code", SessionExternalID: "s-anna", Seq: 3, Kind: GitEventPRCreate, StartedAt: "2026-11-08T16:40:00Z",
		EndedAt: "2026-11-08T16:40:02Z", OK: true, PR: 7, PRRemote: prProject})
	return f, c
}

func TestPullSessionsShowOnlyWhatTheViewerMayRead(t *testing.T) {
	f, _ := linkedFixture(t)
	st := f.st
	pull := func(who, label string) (GitSessionLinks, error) {
		return st.Access(Principal{ID: who, Label: label}).PullSessions(prProject, 7)
	}
	// Der Besitzer der privaten Session sieht beide Sessions mit Titel und Adresse
	// (die freigegebene gehört dem Projekt).
	robin, err := pull("person:1", "robin")
	if err != nil || len(robin.Items) != 2 || robin.Hidden != 0 {
		t.Fatalf("robin: %+v %v", robin, err)
	}
	for _, it := range robin.Items {
		if it.Level != GitSessionLinkFull || it.SessionRef == "" || it.Title == "" || it.Person == "" || it.Date == "" {
			t.Errorf("robin sees title and ref: %+v", it)
		}
	}
	// Anna (Mitglied): ihre eigene und - für das Projekt freigegeben - die von
	// robin nicht: private heißt privat, sie zählt nur.
	anna, err := pull("person:2", "anna")
	if err != nil || len(anna.Items) != 1 || anna.Hidden != 1 {
		t.Fatalf("anna: %+v %v", anna, err)
	}
	if it := anna.Items[0]; it.Level != GitSessionLinkFull || it.Person != "anna" {
		t.Fatalf("anna sees her own: %+v", it)
	}
	// Ben (Gast): kein Transkript, keine Metadaten, nur die Zahl.
	ben, err := pull("person:3", "ben")
	if err != nil || len(ben.Items) != 0 || ben.Hidden != 2 {
		t.Fatalf("a guest sees a number only: %+v %v", ben, err)
	}
	// Mallory (kein Zugriff aufs Projekt): sieht so wenig wie bei einem Projekt,
	// das es nicht gibt, auch keine Zahl.
	if out, err := pull("person:4", "mallory"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("an outsider: %+v %v", out, err)
	}
	// Anonym: kein Konto, nichts.
	if out, err := st.Access(Principal{}).PullSessions(prProject, 7); !errors.Is(err, sql.ErrNoRows) || len(out.Items) != 0 || out.Hidden != 0 {
		t.Fatalf("anonymous: %+v %v", out, err)
	}
}

// Die mittlere Stufe (DEC-11) ist mit der heutigen Rechtetabelle nicht zu
// erreichen: wer Metadaten lesen darf, darf bei einer freigegebenen Session auch
// das Transkript, und eine private zeigt sich einem Nicht-Besitzer gar nicht. Die
// Stufe steht trotzdem im Code, und dieser Test hält ihre Form fest: Person und
// Datum, nie Titel, Adresse oder Harness.
func TestTheMetadataLevelShowsPersonAndDateWithoutTitle(t *testing.T) {
	f, _ := linkedFixture(t)
	links, err := f.st.SessionsForPull(prProject, 7)
	if err != nil || len(links) != 2 {
		t.Fatalf("%+v %v", links, err)
	}
	onlyMeta := func(Session) bool { return false }
	allMeta := func(Session) bool { return true }
	out, err := shapeGitLinks(f.st, prProject, links, onlyMeta, allMeta)
	if err != nil || len(out.Items) != 2 || out.Hidden != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, it := range out.Items {
		if it.Level != GitSessionLinkMeta || it.Title != "" || it.SessionRef != "" || it.Harness != "" || it.Person == "" || it.Date == "" {
			t.Errorf("metadata level shows person and date only: %+v", it)
		}
	}
	// Ohne beide Rechte bleibt nur die Zahl.
	out, err = shapeGitLinks(f.st, prProject, links, onlyMeta, onlyMeta)
	if err != nil || len(out.Items) != 0 || out.Hidden != 2 {
		t.Fatalf("neither right: %+v %v", out, err)
	}
}

func TestSessionsOfAnotherProjectNeverAppear(t *testing.T) {
	f, c := linkedFixture(t)
	// Eine Zeile ordnet eine Session aus dem anderen Projekt diesem PR und Commit
	// zu (etwa ein Fehler beim Schreiben): die Sichtprüfung nimmt das Projekt der
	// Session, nicht das der Zeile.
	if _, err := f.st.db.Exec(`INSERT INTO session_prs(session_id,project,pr,source,recorded_at) VALUES(?,?,7,'created','2026-11-08T00:00:00Z')`, f.other.ID, prProject); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`INSERT INTO session_commits(session_id,project,sha,source,recorded_at) VALUES(?,?,?,'reported','2026-11-08T00:00:00Z')`, f.other.ID, prProject, c); err != nil {
		t.Fatal(err)
	}
	robin := f.st.Access(Principal{ID: "person:1", Label: "robin"})
	for name, call := range map[string]func() (GitSessionLinks, error){
		"pull":   func() (GitSessionLinks, error) { return robin.PullSessions(prProject, 7) },
		"commit": func() (GitSessionLinks, error) { return robin.CommitSessions(prProject, c) },
	} {
		out, err := call()
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range out.Items {
			if it.Title == "prompt in the other project" {
				t.Errorf("%s: a session of another project: %+v", name, it)
			}
		}
		if out.Hidden != 0 {
			t.Errorf("%s: not even as a number: %+v", name, out)
		}
	}
	// Das andere Projekt selbst kennt den PR nicht.
	if out, err := robin.PullSessions(gitOtherProject, 7); err != nil || len(out.Items) != 0 || out.Hidden != 0 {
		t.Fatalf("other project: %+v %v", out, err)
	}
}

func TestCommitSessionsRejectAnInvalidHash(t *testing.T) {
	f, _ := linkedFixture(t)
	if _, err := f.st.Access(Principal{ID: "person:1", Label: "robin"}).CommitSessions(prProject, "xyz"); err == nil {
		t.Fatal("an invalid hash is an error, not an empty answer")
	}
}
