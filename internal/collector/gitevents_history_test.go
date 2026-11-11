package collector

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/blobstrip"
	"github.com/Deadweight-Labs/ghosttree/internal/redact"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/slim"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// stored macht aus einer Originalzeile, was die Datenbank speichert (wie watch.go sie
// schlankt). keep ist false für eine Zeile, die wegfällt; ihre Nummer bleibt vergeben.
func stored(line string) (string, bool) {
	s, _ := blobstrip.Strip(line)
	return slim.Line(redact.RedactJSON(s))
}

func historyEvents(harness, id string, lines []string) []store.SessionGitEvent {
	h := NewHistoryScan(harness, id)
	for seq, l := range lines {
		if s, keep := stored(l); keep {
			h.Line(seq, s)
		}
	}
	return h.Events()
}

// Die Erkennung auf den gespeicherten (geschlankten) Zeilen findet dieselben Vorgänge wie
// die des Collectors auf den Originalzeilen.
func TestHistoryScanFindsTheSameEventsInTheStoredLines(t *testing.T) {
	for _, c := range []struct{ harness, id, file string }{
		{"claude-code", "11111111-2222-4333-8444-555555555555", "git_claude.jsonl"},
		{"codex", "019a0000-0000-7000-8000-000000000001", "git_codex.jsonl"},
	} {
		t.Run(c.harness, func(t *testing.T) {
			lines := readLines(t, c.file)
			live := scanAll(t, c.harness, c.id, c.file)
			if len(live) == 0 {
				t.Fatal("the fixture has events")
			}
			got := historyEvents(c.harness, c.id, lines)
			if !reflect.DeepEqual(got, live) {
				for i := range got {
					t.Logf("history %d: %+v", i, got[i])
				}
				for i := range live {
					t.Logf("live    %d: %+v", i, live[i])
				}
				t.Fatalf("history found %d events, the live scan %d", len(got), len(live))
			}
		})
	}
}

// Ein wartender Aufruf verfällt live nach sechs Stunden. In einem alten Transkript ist
// jeder Aufruf älter, und mehrere Aufrufe, die vor ihren Ergebnissen stehen (parallele
// Werkzeugaufrufe), dürfen sich nicht verdrängen.
func TestHistoryScanKeepsParallelOldCallsUntilTheirResultsArrive(t *testing.T) {
	var lines []string
	for i := 0; i < 3; i++ {
		id := "toolu_" + string(rune('A'+i))
		lines = append(lines, claudeCall(id, "git commit -qm x", "2024-03-01T10:00:0"+string(rune('0'+i))+".000Z", "/tmp/nowhere"))
	}
	for i := 0; i < 3; i++ {
		id := "toolu_" + string(rune('A'+i))
		lines = append(lines, claudeResult(id, "2024-03-01T10:00:1"+string(rune('0'+i))+".000Z", "/tmp/nowhere"))
	}
	if got := historyEvents("claude-code", "s", lines); len(got) != 3 {
		t.Fatalf("all three old calls are events, got %d", len(got))
	}
	live := newGitScan("claude-code", "s")
	for seq, l := range lines {
		live.line(seq, l)
	}
	if len(live.queue) == 3 {
		t.Error("the live scan lets a call older than its time to live go; this test pins the difference")
	}
}

func TestHistoryScanReadsNoRepo(t *testing.T) {
	repo := newRepo(t)
	commitFile(t, repo, t0+10, "a")
	lines := []string{
		claudeCall("toolu_1", "git commit -qm a", stamp(t0+10), repo),
		claudeResult("toolu_1", stamp(t0+10), repo),
	}
	got := historyEvents("claude-code", "s", lines)
	if len(got) != 1 || len(got[0].Observed) != 0 {
		t.Fatalf("a history run has no reflog: %+v", got)
	}
}

// Von den Zeilen in einer Datei bis zur Abdeckung: der leise Commit liegt im Fenster eines
// PR-Commits (matched), der gemeldete SHA bleibt (reported), der PR, den die Session
// angelegt hat, steht an ihr, und ein zweiter Lauf schreibt nichts.
func TestBackfillWithTheRealScanOverStoredTranscripts(t *testing.T) {
	const (
		project = "github.com/example-org/example-repo"
		quiet   = "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"
		other   = "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"
	)
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const ext = "11111111-2222-4333-8444-555555555555"
	id, err := st.UpsertSession(store.Session{Harness: "claude-code", ExternalID: ext, AccountID: 1,
		Scope: scope.Axes{Project: project, Branch: "feat/example"}, StartedAt: "2026-11-08T16:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []store.Chunk
	for seq, l := range readLines(t, "git_claude.jsonl") {
		if s, keep := stored(l); keep {
			chunks = append(chunks, store.Chunk{Seq: seq, Raw: s})
		}
	}
	if err := st.AppendChunks(id, chunks); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO git_pull_requests(project,number,state,head_ref,opened_at,updated_at) VALUES('` + project + `',12,'open','feat/example','2026-11-08T16:00:00Z','2026-11-08T16:00:00Z')`,
		// Der leise Commit des Transkripts (Zeile 1, 16:06:33.9) hat die Zeit :34.
		`INSERT INTO git_pr_commits(project,pr,sha,author,committed_at,position,recorded_at) VALUES('` + project + `',12,'` + quiet + `','','2026-11-08T16:06:34Z',0,'2026-11-08T16:07:00Z')`,
		// Ein Commit, den kein Fenster der Session trifft.
		`INSERT INTO git_pr_commits(project,pr,sha,author,committed_at,position,recorded_at) VALUES('` + project + `',12,'` + other + `','','2026-11-08T20:00:00Z',1,'2026-11-08T16:07:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dry bool) store.BackfillGitReport {
		rep, err := store.BackfillSessionGit(context.Background(), path, store.BackfillGitOptions{
			DryRun: dry, Pause: time.Millisecond,
			Scan: func(h, e string) store.GitLineScanner { return NewHistoryScan(h, e) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	dry := run(true)
	if rows, _ := st.SessionCommits(id); len(rows) != 0 {
		t.Fatalf("a dry run writes nothing: %+v", rows)
	}
	rep := run(false)
	if rep.Found["commit"] != 4 || rep.Found["push"] != 1 || rep.Found["pr_create"] != 1 || rep.Found["pr_merge"] != 1 {
		t.Fatalf("events found: %+v", rep.Found)
	}
	if dry.Recorded != rep.Recorded || dry.CommitsMatched != rep.CommitsMatched || dry.Coverage != rep.Coverage {
		t.Errorf("the dry run predicts the run:\n dry  %+v\n real %+v", dry, rep)
	}
	rows, err := st.SessionCommits(id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.SHA] = r.Source
	}
	if got[quiet] != store.CommitSourceMatched || got["1a2b3c4"] != store.CommitSourceReported || len(got) != 2 {
		t.Fatalf("commits of the session: %v", got)
	}
	links, err := st.SessionsForPull(project, 12)
	if err != nil || len(links) != 1 || links[0].SessionID != id {
		t.Fatalf("the pull request leads to the session: %+v %v", links, err)
	}
	if src := strings.Join(links[0].Sources, ","); !strings.Contains(src, "created") || !strings.Contains(src, "matched") {
		t.Errorf("created and matched: %s", src)
	}
	c := rep.Coverage
	// Vier Commit-Aufrufe, einer davon fehlgeschlagen: zählen drei; der Heredoc-Commit
	// liegt in keinem Fenster eines PR-Commits.
	if c.CommitCalls.Matched != 1 || c.CommitCalls.Reported != 1 || c.CommitCalls.None != 1 {
		t.Errorf("commit calls: %+v", c.CommitCalls)
	}
	if c.Commits != 2 || c.CommitsBy.Matched != 1 || c.CommitsBy.None != 1 || c.PRs != 1 || c.PRsBy.With() != 1 {
		t.Errorf("coverage: %+v", c)
	}
	// Wiederholt: nichts Neues.
	again := run(false)
	if again.Recorded != 0 || again.ReportedAdded != 0 || again.CommitsMatched != 0 {
		t.Errorf("a second run adds nothing: %+v", again)
	}
}
