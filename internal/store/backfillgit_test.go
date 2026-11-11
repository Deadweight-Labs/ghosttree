package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Der Bestandslauf mit einer Erkennung aus dem Test: scriptScanner liest "Zeilen", die
// selbst die Vorgänge nennen ("commit 2026-11-08T10:00:00Z 10:00:01Z feat/a [sha]"). Die
// echte Erkennung (gitcmd, Transkriptzeilen) prüfen die Tests im Collector und in cmd/ctx.

type scriptScanner struct{ events []SessionGitEvent }

func (s *scriptScanner) Line(seq int, raw string) {
	f := strings.Fields(raw)
	if len(f) < 4 || f[0] != "commit" && f[0] != "pr_create" && f[0] != "push" {
		return
	}
	e := SessionGitEvent{Seq: seq, Kind: f[0], StartedAt: f[1], EndedAt: f[2], OK: true, Branch: f[3]}
	if e.Kind == GitEventCommit {
		e.Op = "commit"
		e.SHAs = f[4:]
	}
	if e.Kind == GitEventPRCreate {
		e.PR, e.PRRemote = 9, bfProject
	}
	s.events = append(s.events, e)
}

func (s *scriptScanner) Events() []SessionGitEvent { return s.events }

const bfProject = "github.com/deadweight-labs/app"

type bfFix struct {
	path string
	st   *Store
	ids  map[string]int64
}

// bfFixture: eine Datei mit einem Konto-Owner, Sessions und PR-Tabellen. ext nennt die
// Sessions, lines je Session die "Zeilen".
func bfFixture(t *testing.T, sessions map[string][]string) *bfFix {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &bfFix{path: path, st: st, ids: map[string]int64{}}
	// Die Reihenfolge der Sessions soll die der Namen sein.
	names := make([]string, 0, len(sessions))
	for n := range sessions {
		names = append(names, n)
	}
	sortStrings(names)
	for _, ext := range names {
		id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: ext, AccountID: 1,
			Scope: scope.Axes{Project: bfProject, Branch: "feat/a"}, StartedAt: "2026-11-08T09:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		var chunks []Chunk
		for i, l := range sessions[ext] {
			chunks = append(chunks, Chunk{Seq: i, Role: "assistant", Raw: l})
		}
		if len(chunks) > 0 {
			if err := st.AppendChunks(id, chunks); err != nil {
				t.Fatal(err)
			}
		}
		f.ids[ext] = id
	}
	t.Cleanup(func() { st.Close() })
	return f
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// pr legt einen PR und seine Commits an (ohne den Weg über GitHub).
func (f *bfFix) pr(t *testing.T, number int64, state, head string, commits ...[3]string) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO git_pull_requests(project,number,state,head_ref,opened_at,updated_at) VALUES(?,?,?,?,?,?)`,
		bfProject, number, state, head, "2026-11-08T00:00:00Z", "2026-11-08T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for i, c := range commits {
		if _, err := f.st.db.Exec(`INSERT INTO git_pr_commits(project,pr,sha,author,committed_at,position,recorded_at) VALUES(?,?,?,?,?,?,?)`,
			bfProject, number, c[0], c[1], c[2], i, "2026-11-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *bfFix) scan(harness, ext string) GitLineScanner { return &scriptScanner{} }

func (f *bfFix) run(t *testing.T, opts BackfillGitOptions) BackfillGitReport {
	t.Helper()
	if opts.Scan == nil {
		opts.Scan = f.scan
	}
	if opts.Pause == 0 {
		opts.Pause = time.Millisecond
	}
	rep, err := BackfillSessionGit(context.Background(), f.path, opts)
	if err != nil {
		t.Fatalf("backfill %+v: %v", opts, err)
	}
	return rep
}

func (f *bfFix) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// owners: SHA → Quelle, für die Session ext.
func (f *bfFix) source(t *testing.T, ext, sha string) string {
	t.Helper()
	var src string
	err := f.st.db.QueryRow(`SELECT source FROM session_commits WHERE session_id=? AND sha=?`, f.ids[ext], sha).Scan(&src)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return src
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
	shaD = "dddddddddddddddddddddddddddddddddddddddd"
)

// fixtureSessions: s1 committed leise (kein SHA) und legte einen PR an, s2 meldet einen SHA
// (kurz), s3 committed leise in einem Zeitfenster, in dem kein PR-Commit liegt.
func fixtureSessions() map[string][]string {
	return map[string][]string{
		"s1": {"noise", "commit 2026-11-08T10:00:00Z 2026-11-08T10:00:01Z feat/a", "pr_create 2026-11-08T10:05:00Z 2026-11-08T10:05:02Z feat/a"},
		"s2": {"commit 2026-11-08T11:00:00Z 2026-11-08T11:00:01Z feat/b " + shaB[:7]},
		"s3": {"commit 2026-11-08T15:00:00Z 2026-11-08T15:00:01Z feat/c", "push 2026-11-08T15:01:00Z 2026-11-08T15:01:02Z feat/c"},
	}
}

func TestBackfillWritesTheEventsReportsHashesAndMatchesTheWindows(t *testing.T) {
	f := bfFixture(t, fixtureSessions())
	f.pr(t, 7, "merged", "feat/a",
		[3]string{shaA, "", "2026-11-08T10:00:00Z"},
		[3]string{shaD, "", "2026-11-08T12:00:00Z"})
	f.pr(t, 8, "open", "feat/b", [3]string{shaB, "", "2026-11-08T11:00:01Z"})

	rep := f.run(t, BackfillGitOptions{})
	if rep.Sessions != 3 || rep.Found["commit"] != 3 || rep.Found["push"] != 1 || rep.Found["pr_create"] != 1 {
		t.Fatalf("what the scan found: %+v", rep)
	}
	if rep.Recorded != 5 || rep.Known != 0 || rep.ReportedAdded != 1 {
		t.Fatalf("what was written: %+v", rep)
	}
	if n := f.count(t, "session_git_events"); n != 5 {
		t.Fatalf("five events, got %d", n)
	}
	// s2: der gemeldete kurze SHA ist durch die Commits des PRs voll geworden (reported).
	if got := f.source(t, "s2", shaB); got != CommitSourceReported {
		t.Errorf("s2 reported %s: %q", shaB, got)
	}
	// s1: der leise Commit liegt im Fenster des PR-Commits (matched).
	if got := f.source(t, "s1", shaA); got != CommitSourceMatched {
		t.Errorf("s1 matched %s: %q", shaA, got)
	}
	// Der Commit um 12:00 hat kein Fenster; s3 hat nichts, was passt.
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits WHERE sha=?`, shaD).Scan(&n)
	if n != 0 {
		t.Errorf("no window covers %s", shaD)
	}
	if rep.CommitsMatched != 1 || rep.ClaimsChecked != 1 || rep.EventsMatched < 2 {
		t.Errorf("the match: %+v", rep)
	}
	// Das PR-Create ist in session_prs.
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_prs WHERE session_id=? AND pr=9`, f.ids["s1"]).Scan(&n)
	if n != 1 {
		t.Errorf("the created pull request is recorded: %d", n)
	}
	// Abdeckung: drei Commit-Aufrufe, zwei mit Quelle (matched, reported), einer ohne.
	c := rep.Coverage
	if c.CommitCalls.Reported != 1 || c.CommitCalls.Matched != 1 || c.CommitCalls.None != 1 || c.CommitCalls.Observed != 0 {
		t.Errorf("commit calls by source: %+v", c.CommitCalls)
	}
	if c.Commits != 3 || c.CommitsBy.Reported != 1 || c.CommitsBy.Matched != 1 || c.CommitsBy.None != 1 {
		t.Errorf("commits in PRs: %d %+v", c.Commits, c.CommitsBy)
	}
	if c.PRs != 2 || c.PRsBy.With() != 2 || c.MergedPRs != 1 || c.MergedBy.With() != 1 {
		t.Errorf("pull requests: %d %+v merged %d %+v", c.PRs, c.PRsBy, c.MergedPRs, c.MergedBy)
	}
	if c.Sessions != 3 || c.SessionsWithCalls != 3 || c.ShortReported != 0 {
		t.Errorf("sessions: %+v", c)
	}
}

func TestBackfillIsRepeatableWithoutDuplicates(t *testing.T) {
	f := bfFixture(t, fixtureSessions())
	f.pr(t, 7, "merged", "feat/a", [3]string{shaA, "", "2026-11-08T10:00:00Z"})
	first := f.run(t, BackfillGitOptions{})
	rows := func() (int, int, int) {
		return f.count(t, "session_git_events"), f.count(t, "session_commits"), f.count(t, "session_prs")
	}
	e1, c1, p1 := rows()
	second := f.run(t, BackfillGitOptions{})
	e2, c2, p2 := rows()
	if e1 != e2 || c1 != c2 || p1 != p2 {
		t.Fatalf("a second run adds nothing: events %d→%d, commits %d→%d, prs %d→%d", e1, e2, c1, c2, p1, p2)
	}
	if second.Recorded != 0 || second.ReportedAdded != 0 || second.CommitsMatched != 0 || second.Known != first.Recorded {
		t.Errorf("the second run: %+v (first recorded %d)", second, first.Recorded)
	}
	if first.Coverage != second.Coverage {
		t.Errorf("the coverage stays: %+v vs %+v", first.Coverage, second.Coverage)
	}
}

func TestBackfillDryRunWritesNothingAndReportsWhatARealRunLeaves(t *testing.T) {
	f := bfFixture(t, fixtureSessions())
	f.pr(t, 7, "merged", "feat/a", [3]string{shaA, "", "2026-11-08T10:00:00Z"}, [3]string{shaB, "", "2026-11-08T11:00:01Z"})
	before := [3]int{f.count(t, "session_git_events"), f.count(t, "session_commits"), f.count(t, "session_prs")}
	var walBefore string
	f.st.db.QueryRow(`SELECT COALESCE(MAX(val),'') FROM index_state WHERE key=?`, backfillGitCursor).Scan(&walBefore)

	dry := f.run(t, BackfillGitOptions{DryRun: true})
	after := [3]int{f.count(t, "session_git_events"), f.count(t, "session_commits"), f.count(t, "session_prs")}
	if before != after {
		t.Fatalf("a dry run writes nothing: %v → %v", before, after)
	}
	var cursor string
	f.st.db.QueryRow(`SELECT COALESCE(MAX(val),'') FROM index_state WHERE key=?`, backfillGitCursor).Scan(&cursor)
	if cursor != walBefore {
		t.Errorf("and keeps no cursor: %q", cursor)
	}
	if !dry.DryRun || dry.Recorded != 5 || dry.CommitsMatched != 1 || dry.ReportedAdded != 1 {
		t.Fatalf("the dry run counts the work: %+v", dry)
	}
	real := f.run(t, BackfillGitOptions{})
	if dry.Recorded != real.Recorded || dry.ReportedAdded != real.ReportedAdded || dry.CommitsMatched != real.CommitsMatched ||
		dry.ClaimsChecked != real.ClaimsChecked || dry.Coverage != real.Coverage {
		t.Errorf("the dry run predicts the real one:\n dry  %+v\n real %+v", dry, real)
	}
}

func TestBackfillResumesBehindTheLastSessionAndSkipsAnUnreadableOne(t *testing.T) {
	f := bfFixture(t, fixtureSessions())
	rep := f.run(t, BackfillGitOptions{Batch: 1})
	if rep.LastID != f.ids["s3"] {
		t.Fatalf("the last session is s3: %d", rep.LastID)
	}
	var cursor int64
	if err := f.st.db.QueryRow(`SELECT val FROM index_state WHERE key=?`, backfillGitCursor).Scan(&cursor); err != nil || cursor != f.ids["s3"] {
		t.Fatalf("the progress is kept: %d %v", cursor, err)
	}
	// Fortsetzen hinter s2 liest nur s3.
	after := f.run(t, BackfillGitOptions{AfterID: f.ids["s2"]})
	if after.Sessions != 1 || after.Found["commit"] != 1 {
		t.Errorf("--after-id: %+v", after)
	}
	// --resume geht hinter den gemerkten Stand: nichts mehr zu lesen.
	if res := f.run(t, BackfillGitOptions{Resume: true}); res.Sessions != 0 {
		t.Errorf("--resume has nothing left: %+v", res)
	}
	// Eine Session, deren Zeilen sich nicht lesen lassen, hält den Lauf nicht auf.
	if _, err := f.st.db.Exec(`UPDATE session_chunks SET raw = X'deadbeef' WHERE session_id=? AND seq=0`, f.ids["s2"]); err != nil {
		t.Fatal(err)
	}
	broken := f.run(t, BackfillGitOptions{})
	if broken.SessionsFailed != 1 || !strings.Contains(broken.FirstFailure, fmt.Sprintf("session %d", f.ids["s2"])) || broken.Sessions != 3 {
		t.Errorf("an unreadable session is counted and skipped: %+v", broken)
	}
}

func TestBackfillStopsOnCancelAndContinuesWithAfterID(t *testing.T) {
	f := bfFixture(t, fixtureSessions())
	ctx, cancel := context.WithCancel(context.Background())
	opts := BackfillGitOptions{Batch: 1, Pause: time.Millisecond, Scan: f.scan}
	opts.Progress = func(r BackfillGitReport) { cancel() }
	rep, err := BackfillSessionGit(ctx, f.path, opts)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("an interrupted run reports it: %v", err)
	}
	if rep.LastID == 0 || rep.LastID >= f.ids["s3"] {
		t.Fatalf("it stopped part-way, behind session %d", rep.LastID)
	}
	done := f.run(t, BackfillGitOptions{AfterID: rep.LastID})
	if n := f.count(t, "session_git_events"); n != 5 || done.Sessions == 0 {
		t.Errorf("together the runs wrote everything once: %d events", n)
	}
}

func TestBackfillRefusesADatabaseWithoutTheTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = BackfillSessionGit(context.Background(), path, BackfillGitOptions{Scan: func(string, string) GitLineScanner { return &scriptScanner{} }})
	if err == nil || !strings.Contains(err.Error(), "start the new ghosttree server once") {
		t.Fatalf("a database from before the tables is refused with a hint: %v", err)
	}
}

// Der Lauf hält die Schreibsperre nur kurz und blockiert den Server nicht: ein Schreiber
// daneben kommt zwischen den Transaktionen durch.
func TestBackfillWritesInShortTransactionsBesideAWriter(t *testing.T) {
	sessions := map[string][]string{}
	for i := 0; i < 30; i++ {
		var lines []string
		for j := 0; j < 5; j++ {
			lines = append(lines, fmt.Sprintf("commit 2026-11-08T10:%02d:%02dZ 2026-11-08T10:%02d:%02dZ feat/a", i, j, i, j+1))
		}
		sessions[fmt.Sprintf("s%03d", i)] = lines
	}
	f := bfFixture(t, sessions)
	// Pull request commits in the window of every second call: the match reads and then
	// writes in each transaction, which is where a writer beside it used to make an
	// upgrade of the read snapshot fail with SQLITE_BUSY (found at 2 million lines).
	var prCommits [][3]string
	for i := 0; i < 30; i++ {
		for j := 0; j < 5; j += 2 {
			prCommits = append(prCommits, [3]string{fmt.Sprintf("%040x", i*10+j+1), "", fmt.Sprintf("2026-11-08T10:%02d:%02dZ", i, j)})
		}
	}
	f.pr(t, 1, "merged", "feat/a", prCommits...)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var worst time.Duration
	var writes int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			began := time.Now()
			if _, err := f.st.db.Exec(`INSERT INTO index_state(key,val) VALUES('probe', 1) ON CONFLICT(key) DO UPDATE SET val=val+1`); err != nil {
				t.Errorf("the writer beside the backfill: %v", err)
				return
			}
			if d := time.Since(began); d > worst {
				worst = d
			}
			writes++
			time.Sleep(time.Millisecond)
		}
	}()
	rep := f.run(t, BackfillGitOptions{Batch: 20, Pause: 2 * time.Millisecond})
	close(stop)
	wg.Wait()
	if rep.Recorded != 150 || rep.Batches < 7 || rep.CommitsMatched == 0 {
		t.Fatalf("150 events in several transactions, and matches written beside the writer: %+v", rep)
	}
	// Ohne BEGIN IMMEDIATE scheiterte die Transaktion des Abgleichs an dem Schreiber daneben und
	// wartete eine Wiederholungspause ab (fünf Sekunden und mehr).
	// Counted, not timed: a loaded CI runner makes the wall clock unreliable.
	if rep.BusyRetries != 0 {
		t.Errorf("the run waited for %d retries instead of for the lock (%s)", rep.BusyRetries, rep.Duration)
	}
	if writes == 0 || worst > 2*time.Second {
		t.Errorf("the other writer kept going: %d writes, worst wait %s", writes, worst)
	}
	if rep.LockMax <= 0 || rep.LockP95 > rep.LockMax {
		t.Errorf("lock figures: %+v", rep)
	}
}
