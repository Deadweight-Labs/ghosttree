package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitmsg"
)

// fixedScanner meldet die Vorgänge, die der Test für die Session vorbereitet hat.
type fixedScanner struct{ events []SessionGitEvent }

func (s *fixedScanner) Line(int, string)          {}
func (s *fixedScanner) Events() []SessionGitEvent { return s.events }

// prWithKeys legt einen PR mit Commits an, die Titel und Schlüssel der Nachricht tragen, wie
// RecordPullCommits sie schreibt.
func (f *bfFix) prWithKeys(t *testing.T, number int64, state, head string, commits ...[3]string) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO git_pull_requests(project,number,state,head_ref,opened_at,updated_at) VALUES(?,?,?,?,?,?)`,
		bfProject, number, state, head, "2026-11-08T00:00:00Z", "2026-11-08T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for i, c := range commits {
		k := gitmsg.Of(c[2])
		if _, err := f.st.db.Exec(`INSERT INTO git_pr_commits(project,pr,sha,author,title,committed_at,position,recorded_at,title_key,msg_key) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			bfProject, number, c[0], "", c[2], c[1], i, "2026-11-08T00:00:00Z", k.Title, k.Full); err != nil {
			t.Fatal(err)
		}
	}
}

// Der Bestandslauf trägt Nachricht und Push-Bereich an Vorgängen nach, die ein früherer Lauf
// ohne sie geschrieben hat, legt sie über die PRs und bleibt dabei idempotent.
func TestBackfillAddsMessagesAndRangesToKnownEventsAndMatchesThem(t *testing.T) {
	f := bfFixture(t, map[string][]string{"s1": {"line"}})
	f.prWithKeys(t, 7, "open", "feat/a",
		[3]string{shaA, "2026-11-08T09:00:00Z", "chore: base"},
		[3]string{shaB, "2026-11-08T10:00:00Z", "feat: one"},
		[3]string{shaC, "2026-11-08T10:10:00Z", "feat: two"},
		[3]string{shaD, "2026-11-08T10:20:00Z", "wip"})
	call := func(seq int, from, to, msg string) SessionGitEvent {
		e := SessionGitEvent{Seq: seq, Kind: GitEventCommit, Op: "commit", StartedAt: from, EndedAt: to, OK: true, Branch: "feat/a"}
		if msg != "" {
			k := gitmsg.Of(msg)
			e.MsgTitle, e.MsgFull = k.Title, k.Full
		}
		return e
	}
	push := SessionGitEvent{Seq: 5, Kind: GitEventPush, StartedAt: "2026-11-08T10:21:00Z", EndedAt: "2026-11-08T10:21:03Z", OK: true, Branch: "feat/a",
		PushOld: shaA[:7], PushNew: shaD[:7], PushRefs: []PushedRef{{Branch: "feat/a", Old: shaA[:7], New: shaD[:7]}}}
	old := []SessionGitEvent{
		call(1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", ""),
		call(2, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", ""),
		call(3, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", ""),
		{Seq: 5, Kind: GitEventPush, StartedAt: push.StartedAt, EndedAt: push.EndedAt, OK: true, Branch: "feat/a"},
	}
	fresh := []SessionGitEvent{
		call(1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat: one"),
		call(2, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat: two"),
		call(3, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", ""),
		push,
	}
	scan := func(events []SessionGitEvent) func(string, string) GitLineScanner {
		return func(string, string) GitLineScanner { return &fixedScanner{events: events} }
	}

	// Der frühere Lauf kannte weder Nachricht noch Bereich: die Zeitfenster binden die drei Commits.
	first := f.run(t, BackfillGitOptions{Scan: scan(old)})
	if first.Recorded != 4 || first.Enriched != 0 || first.EvidenceAdded != 0 || first.CommitsMatched != 3 {
		t.Fatalf("first run: %+v", first)
	}
	for _, sha := range []string{shaB, shaC, shaD} {
		if got := f.source(t, "s1", sha); got != CommitSourceMatched {
			t.Fatalf("%s before: %q", sha[:4], got)
		}
	}

	// Der Probelauf legt nichts in die Datei und meldet, was der echte Lauf täte.
	dry := f.run(t, BackfillGitOptions{Scan: scan(fresh), DryRun: true})
	if got := f.source(t, "s1", shaB); got != CommitSourceMatched {
		t.Fatalf("a dry run wrote to the file: %q", got)
	}

	second := f.run(t, BackfillGitOptions{Scan: scan(fresh)})
	if second.Recorded != 0 || second.Enriched != 3 || second.Known != 4 {
		t.Fatalf("second run: %+v", second)
	}
	if dry.Enriched != second.Enriched || dry.EvidenceUpgraded != second.EvidenceUpgraded || dry.Coverage != second.Coverage {
		t.Fatalf("dry run %+v\nreal run %+v", dry, second)
	}
	// Die drei Zeilen der Zeitfenster (matched) wurden zu message+pushed, message+pushed und pushed.
	if second.EvidenceCommits != 2 || second.EvidencePushes != 1 || second.EvidenceAdded != 0 || second.EvidenceUpgraded < 3 {
		t.Fatalf("evidence checked: %+v", second)
	}
	// Die beiden Commits mit Nachricht sind message+pushed, der dritte (Aufruf ohne Nachricht, einziger
	// Rest der Gegenprobe) pushed. Die Autoren sind unbekannt: alles bleibt schwach.
	for sha, want := range map[string]string{shaB: CommitSourceMessagePushed, shaC: CommitSourceMessagePushed, shaD: CommitSourcePushed} {
		if got := f.source(t, "s1", sha); got != want {
			t.Errorf("%s: %q, want %q", sha[:4], got, want)
		}
	}
	if got := f.source(t, "s1", shaA); got != "" {
		t.Errorf("the base commit is not the session's: %q", got)
	}
	c := second.Coverage
	if c.CommitsBy.MessagePushed != 2 || c.CommitsBy.Pushed != 1 || c.CommitsBy.SignalWeak != 3 || c.CommitsBy.Matched != 0 || c.CommitsBy.None != 1 {
		t.Errorf("commits in PRs by source: %+v", c.CommitsBy)
	}
	if c.CommitCalls.MessagePushed != 2 || c.CommitCalls.Pushed != 1 {
		t.Errorf("commit calls by source: %+v", c.CommitCalls)
	}
	if c.PRsBy.MessagePushed != 1 {
		t.Errorf("the pull request by source: %+v", c.PRsBy)
	}

	// Ein dritter Lauf schreibt nichts: weder Vorgänge noch Zuordnungen.
	third := f.run(t, BackfillGitOptions{Scan: scan(fresh)})
	if third.Recorded != 0 || third.Enriched != 0 || third.EvidenceAdded != 0 || third.EvidenceUpgraded != 0 || third.CommitsMatched != 0 {
		t.Fatalf("third run: %+v", third)
	}
	if third.Coverage != second.Coverage {
		t.Fatalf("the coverage moved: %+v vs %+v", third.Coverage, second.Coverage)
	}
}
