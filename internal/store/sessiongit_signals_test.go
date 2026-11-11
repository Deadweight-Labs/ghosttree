package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitmsg"
)

// Nachricht und Push-Bereich als Signale (IDEA-31, P6): message, pushed und
// message+pushed. Die Regeln der Kaperung (KN-4499) gelten weiter.

// msgEvent ist ein erfolgreicher Commit-Aufruf, der die Nachricht trägt (als Schlüssel).
func msgEvent(s Session, seq int, start, end, branch, msg string) SessionGitEvent {
	e := commitEvent(s, seq, start, end, branch)
	k := gitmsg.Of(msg)
	e.MsgTitle, e.MsgFull = k.Title, k.Full
	return e
}

// pushEvent ist ein Push, der die Refs verändert hat.
func pushEvent(s Session, seq int, start, end string, refs ...PushedRef) SessionGitEvent {
	e := SessionGitEvent{Harness: s.Harness, SessionExternalID: s.ExternalID, Seq: seq, Kind: GitEventPush,
		StartedAt: start, EndedAt: end, OK: true, PushRefs: refs}
	if len(refs) > 0 {
		e.Branch, e.PushOld, e.PushNew = refs[0].Branch, refs[0].Old, refs[0].New
	}
	return e
}

// prCommit ist ein Commit eines PRs mit seiner ganzen Nachricht (wie GitHub sie liefert).
func prCommit(sha, author, at, msg string) PullCommit {
	title, _, _ := strings.Cut(msg, "\n")
	return PullCommit{SHA: sha, Author: author, Title: title, Message: msg, CommittedAt: at}
}

// sourceOf liest Quelle und Schwäche der Zeile einer Session zu einer SHA.
func (f *gitFix) sourceOf(t *testing.T, s Session, sha string) (source string, weak, ok bool) {
	t.Helper()
	var w int
	err := f.st.db.QueryRow(`SELECT source, weak FROM session_commits WHERE session_id=? AND sha=?`, s.ID, sha).Scan(&source, &w)
	if err != nil {
		return "", false, false
	}
	return source, w == 1, true
}

func (f *gitFix) wantSource(t *testing.T, s Session, sha, source string, weak bool) {
	t.Helper()
	got, w, ok := f.sourceOf(t, s, sha)
	if !ok || got != source || w != weak {
		t.Fatalf("%s for %s: source %q weak=%v (row %v), want %q weak=%v; all owners: %v", s.ExternalID, sha[:4], got, w, ok, source, weak, f.owners(t, sha))
	}
}

func (f *gitFix) wantNone(t *testing.T, sha string) {
	t.Helper()
	if got := f.owners(t, sha); len(got) != 0 {
		t.Fatalf("%s must have no session, has %v", sha[:4], got)
	}
}

// ------------------------------------------------------------ Nachricht

func TestAMessageBindsAQuietCommitInEitherOrder(t *testing.T) {
	for _, order := range []string{"event first", "pull request first"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			c := sha40("c1")
			msg := "fix(web): wrap long titles\n\nThe card no longer overflows."
			// Zwei Vorgänge im Fenster des Commits: einer mit passender Nachricht, einer mit einer anderen.
			ev := func() {
				f.record(t, 1,
					msgEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a", msg),
					msgEvent(f.robin2, 5, "2026-11-08T16:06:33.900Z", "2026-11-08T16:06:34.200Z", "feat/a", "chore: something else"))
			}
			pr := func() { f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T16:06:34Z", msg)) }
			if order == "event first" {
				ev()
				f.wantNone(t, c)
				pr()
			} else {
				pr()
				ev()
			}
			// Ohne die Nachricht wäre das Fenster mehrdeutig (zwei Sessions desselben Kontos auf
			// demselben Branch); die Nachricht entscheidet.
			f.wantSource(t, f.robin, c, CommitSourceMessage, false)
			if got := f.owners(t, c); len(got) != 1 {
				t.Fatalf("only the session with the message: %v", got)
			}
			if !f.resolved(t, f.robin, 4) {
				t.Error("the event is answered")
			}
			// Wiederholung: nichts ändert sich.
			f.record(t, 1, msgEvent(f.robin, 4, "2026-11-08T16:06:33.800Z", "2026-11-08T16:06:34.100Z", "feat/a", msg))
			f.commits(t, 7, prCommit(c, robinLogin, "2026-11-08T16:06:34Z", msg))
			var n int
			f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
			if n != 1 {
				t.Fatalf("rows after a repeat: %d", n)
			}
		})
	}
}

func TestAMessageNeedsTheTimeOfTheCall(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	msg := "feat: add parser"
	early, late := sha40("e1"), sha40("e2")
	// Der Commit ist älter als der Aufruf: er kann nicht aus ihm stammen.
	f.pull(t, 7, "feat/a", prCommit(early, robinLogin, "2026-11-08T15:00:00Z", msg))
	f.record(t, 1, msgEvent(f.robin, 4, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/a", msg))
	f.wantNone(t, early)
	// Ein späterer Commit mit demselben Titel, ohne dass die Session ihn umgeschrieben hat, auch nicht.
	f.commits(t, 7, prCommit(early, robinLogin, "2026-11-08T15:00:00Z", msg), prCommit(late, robinLogin, "2026-11-08T16:30:00Z", msg))
	f.wantNone(t, late)
	// Ein Rebase der Session um 16:30 schreibt den Commit um: die Zeit liegt hinter dem Aufruf, aber im
	// Fenster eines umschreibenden Aufrufs derselben Session.
	f.record(t, 1, SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 6, Kind: GitEventCommit, Op: "rebase",
		StartedAt: "2026-11-08T16:29:59Z", EndedAt: "2026-11-08T16:30:02Z", OK: true, Branch: "feat/a"})
	f.wantSource(t, f.robin, late, CommitSourceMessage, false)
}

// Ein Commit mit demselben Titel, den ein anderer Aufruf in seinem Fenster hat, ist nicht der
// umgeschriebene Commit einer Session, die ihn früher mit dieser Nachricht gemacht hat.
func TestARewrittenCommitIsNotTakenWhenAnotherCallHasItInItsWindow(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	msg := "ci: retrigger"
	c := sha40("c9")
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T18:00:00Z", msg))
	// robin hat den Titel am Morgen benutzt und nichts umgeschrieben, robin2 committet ihn um 18:00 ohne
	// lesbare Nachricht.
	f.record(t, 1, msgEvent(f.robin, 4, "2026-11-08T09:00:00Z", "2026-11-08T09:00:01Z", "feat/a", msg))
	f.record(t, 1, commitEvent(f.robin2, 1, "2026-11-08T17:59:59Z", "2026-11-08T18:00:01Z", "feat/a"))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin-2"] == "" {
		t.Fatalf("the call whose window holds the commit gets it, not the morning call with the same title: %v", got)
	}
}

func TestAPatchIdBindsARewrittenCommitAfterTheSessionEnded(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	msg := "feat: add parser"
	rebased := sha40("a2")
	f.record(t, 1, msgEvent(f.robin, 4, "2026-11-08T16:06:33Z", "2026-11-08T16:06:34Z", "feat/a", msg))
	// Die Session hat den Commit beobachtet (Reflog, Patch-ID); eine andere Session hat ihn
	// später rebaset und die neue SHA mit derselben Patch-ID beobachtet.
	patch := strings.Repeat("9", 40)
	for _, row := range []struct {
		session int64
		sha     string
	}{{f.robin.ID, sha40("a1")}, {f.anna.ID, rebased}} {
		if _, err := f.st.db.Exec(`INSERT INTO session_commits(session_id,project,sha,source,op,chunk_seq,patch_id,recorded_at) VALUES(?,?,?,?,?,?,?,?)`,
			row.session, prProject, row.sha, CommitSourceObserved, "rebase", 99, patch, "2026-11-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	f.pull(t, 7, "feat/a", prCommit(rebased, robinLogin, "2026-11-09T10:00:00Z", msg))
	f.wantSource(t, f.robin, rebased, CommitSourceMessage, false)
	// Ohne die Patch-ID derselben Änderung nicht.
	other := sha40("a3")
	f.commits(t, 7, prCommit(rebased, robinLogin, "2026-11-09T10:00:00Z", msg), prCommit(other, robinLogin, "2026-11-09T11:00:00Z", msg))
	f.wantNone(t, other)
}

func TestTheSameTitleWithAnotherBodyIsAnotherCommit(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := sha40("c1")
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T16:06:34Z", "fix: thing\n\nthe body of the commit"))
	f.record(t, 1, msgEvent(f.robin, 4, "2026-11-08T16:06:33Z", "2026-11-08T16:06:35Z", "feat/a", "fix: thing\n\na different body"))
	f.wantNone(t, c)
	// Dieselbe Nachricht trifft.
	f.record(t, 1, msgEvent(f.robin, 5, "2026-11-08T16:06:33Z", "2026-11-08T16:06:35Z", "feat/a", "fix: thing\n\nthe body of the commit\n\nCo-authored-by: A <a@b.c>"))
	f.wantSource(t, f.robin, c, CommitSourceMessage, false)
}

func TestATitleOnlyEventMatchesACommitWithABody(t *testing.T) {
	// git hängt bei --signoff eine Zeile an: der Collector meldet dann nur den Titel.
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := sha40("c1")
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T16:06:34Z", "fix: thing\n\nbody\n\nSigned-off-by: Robin <r@x.y>"))
	ev := msgEvent(f.robin, 4, "2026-11-08T16:06:33Z", "2026-11-08T16:06:35Z", "feat/a", "fix: thing\n\nbody")
	ev.MsgFull = ""
	f.record(t, 1, ev)
	f.wantSource(t, f.robin, c, CommitSourceMessage, false)
}

// Ein Aufruf, dessen Nachricht der des Commits widerspricht, tritt im Zeitfenster nicht an: sein
// eigener Commit trägt seine Nachricht. Zwei Sessions desselben Kontos im selben Fenster sind so
// nicht mehr mehrdeutig.
func TestAWindowEventWhoseMessageContradictsTheCommitDoesNotCompete(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := sha40("c1")
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T10:00:00Z", "fix: the commit of the worker"))
	f.record(t, 1, msgEvent(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat/a", "chore: a different commit"))
	f.wantNone(t, c)
	// Ohne Nachricht tritt der zweite Aufruf an, und sein Fenster gilt.
	f.record(t, 1, commitEvent(f.robin2, 1, "2026-11-08T09:59:58Z", "2026-11-08T10:00:02Z", "feat/a"))
	f.wantSource(t, f.robin2, c, CommitSourceMatched, false)
	if got := f.owners(t, c); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

// ------------------------------------------------------------ Autor und Kaperung

func TestAMessageOfAnotherPersonCannotTakeACommitItsAuthorIsKnownFor(t *testing.T) {
	for _, order := range []string{"hijacker first", "author first"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			f.linkGitHub(t, "person:2", annaLogin, 22)
			c := sha40("c1")
			msg := "fix: the real change"
			f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T10:00:00Z", msg))
			// Anna kennt die Nachricht (sie steht im PR) und meldet sie mit einem weiten Fenster.
			hijack := func() {
				f.record(t, 2, msgEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a", msg))
			}
			real := func() {
				f.record(t, 1, msgEvent(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat/a", msg))
			}
			if order == "hijacker first" {
				hijack()
				f.wantNone(t, c)
				real()
			} else {
				real()
				hijack()
			}
			f.wantSource(t, f.robin, c, CommitSourceMessage, false)
			if got := f.owners(t, c); len(got) != 1 {
				t.Fatalf("anna took part of robin's commit: %v", got)
			}
		})
	}
}

func TestAMessageOfAnUncheckableAuthorIsWeakAndNeverHoldsAgainstTheAuthor(t *testing.T) {
	f := gitFixture(t)
	// Robin hat sein GitHub-Konto bewiesen, Anna keines verknüpft.
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := sha40("c1")
	msg := "fix: the real change"
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T10:00:00Z", msg))
	f.record(t, 2, msgEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a", msg))
	f.wantSource(t, f.anna, c, CommitSourceMessage, true)
	links, err := f.st.SessionsForCommit(prProject, c)
	if err != nil || len(links) != 1 || !links[0].Weak || len(links[0].Sources) != 1 || links[0].Sources[0] != "message" {
		t.Fatalf("a candidate, not a proof: %+v %v", links, err)
	}
	// Das echte Ereignis des Autors ersetzt sie.
	f.record(t, 1, msgEvent(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat/a", msg))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceMessage || f.weakOf(t, f.robin, c) {
		t.Fatalf("the author's own event did not replace the weak one: %v", got)
	}
}

func TestAMessageNeverDeletesAForeignAssignmentOfThePushRange(t *testing.T) {
	f := gitFixture(t)
	// Nichts ist prüfbar (kein Autor, keine Verknüpfung): alles bleibt schwach.
	c0, c1 := sha40("b0"), sha40("c1")
	msg := "fix: shared words"
	f.pull(t, 7, "feat/a",
		prCommit(c0, "", "2026-11-08T10:00:00Z", "chore: base"),
		prCommit(c1, "", "2026-11-08T10:05:00Z", msg))
	// Robin pusht den Bereich c0..c1 (ein Aufruf, zeitlich passend): pushed.
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:04:59Z", "2026-11-08T10:05:01Z", "feat/a"),
		pushEvent(f.robin, 2, "2026-11-08T10:06:00Z", "2026-11-08T10:06:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]}))
	f.wantSource(t, f.robin, c1, CommitSourcePushed, true)
	// Anna meldet dieselbe Nachricht in einem weiten Fenster (anderes Konto): ihre Zeile steht
	// daneben, die von Robin bleibt.
	f.record(t, 2, msgEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a", msg))
	if _, _, ok := f.sourceOf(t, f.robin, c1); !ok {
		t.Fatalf("a foreign message deleted robin's assignment: %v", f.owners(t, c1))
	}
}

// ------------------------------------------------------------ Push-Bereich

// pushFix: ein PR mit einem Basis-Commit und drei Commits, alle ohne Nachricht verwertbar.
func pushFix(t *testing.T) (*gitFix, []string) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	shas := []string{sha40("b0"), sha40("b1"), sha40("b2"), sha40("b3")}
	f.pull(t, 7, "feat/a",
		prCommit(shas[0], robinLogin, "2026-11-08T10:00:00Z", "chore: base"),
		prCommit(shas[1], robinLogin, "2026-11-08T10:10:00Z", "wip one"),
		prCommit(shas[2], robinLogin, "2026-11-08T10:20:00Z", "wip two"),
		prCommit(shas[3], robinLogin, "2026-11-08T10:30:00Z", "wip three"))
	return f, shas
}

func TestAPushedRangeBindsItsCommitsInOrderWhenTheCountsAgree(t *testing.T) {
	f, c := pushFix(t)
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		commitEvent(f.robin, 2, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", "feat/a"),
		commitEvent(f.robin, 3, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[3][:7]}))
	for i := 1; i <= 3; i++ {
		f.wantSource(t, f.robin, c[i], CommitSourcePushed, false)
		var seq int
		f.st.db.QueryRow(`SELECT chunk_seq FROM session_commits WHERE session_id=? AND sha=?`, f.robin.ID, c[i]).Scan(&seq)
		if seq != i {
			t.Errorf("commit %d is bound to the call at line %d, want %d", i, seq, i)
		}
	}
	f.wantNone(t, c[0])
	// Eine Wiederholung ändert nichts.
	f.record(t, 1, pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[3][:7]}))
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
	if n != 3 {
		t.Fatalf("rows: %d", n)
	}
}

// Der PR wird erst synchronisiert, nachdem Aufrufe und Push gemeldet sind (der übliche Weg:
// der Webhook kommt nach dem Push): der Sync bindet die Commits.
func TestAPushedRangeIsPlacedWhenThePullRequestArrivesLater(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := []string{sha40("b0"), sha40("b1"), sha40("b2")}
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		commitEvent(f.robin, 2, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", "feat/a"),
		pushEvent(f.robin, 3, "2026-11-08T10:21:00Z", "2026-11-08T10:21:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[2][:7]}))
	f.pull(t, 7, "feat/a",
		prCommit(c[0], robinLogin, "2026-11-08T10:00:00Z", "chore: base"),
		prCommit(c[1], robinLogin, "2026-11-08T10:10:00Z", "wip one"),
		prCommit(c[2], robinLogin, "2026-11-08T10:20:00Z", "wip two"))
	f.wantSource(t, f.robin, c[1], CommitSourcePushed, false)
	f.wantSource(t, f.robin, c[2], CommitSourcePushed, false)
	f.wantNone(t, c[0])
}

// Zwei leere Schlüssel sind kein Treffer: ein Aufruf ohne Nachricht und ein Commit ohne Titel
// geben der pushed-Zeile nicht den Vorrang der Nachricht.
func TestNoMessageIsNotAMessageWhenThePushedRowYieldsToAnother(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c0, c1 := sha40("b0"), sha40("b1")
	f.pull(t, 7, "feat/a", prCommit(c0, robinLogin, "2026-11-08T10:00:00Z", "chore: base"), prCommit(c1, robinLogin, "2026-11-08T10:10:00Z", "feat: the one"))
	// Eine andere Session des Kontos hat den Commit über seine Nachricht.
	f.record(t, 1, msgEvent(f.robin2, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a", "feat: the one"))
	f.wantSource(t, f.robin2, c1, CommitSourceMessage, false)
	// robin hat einen Aufruf ohne Nachricht im selben Fenster und pusht den Bereich: robin2 hat Vorrang.
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:02Z", "feat/a"),
		pushEvent(f.robin, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]}))
	if got := f.owners(t, c1); len(got) != 1 || got["s-robin-2"] != CommitSourceMessage {
		t.Fatalf("the message wins over the range: %v", got)
	}
}

func TestAPushedRangeIsRefusedWhenTheCountsDisagree(t *testing.T) {
	f, c := pushFix(t)
	// Nur zwei Aufrufe für drei Commits: ein dritter stammt von jemand anderem.
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		commitEvent(f.robin, 3, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[3][:7]}))
	// Die Zeitfenster (die Vorgänge ohne SHA) binden trotzdem, was in ihr Fenster fällt; der
	// Push-Bereich trägt nichts bei.
	for i := 1; i <= 3; i++ {
		if src, _, ok := f.sourceOf(t, f.robin, c[i]); ok && src == CommitSourcePushed {
			t.Errorf("commit %d bound by an range that does not add up", i)
		}
	}
}

func TestAPushedRangeNeedsTheCallsInTheirWindows(t *testing.T) {
	f, c := pushFix(t)
	// Drei Aufrufe, aber die Zeiten passen nicht zu den Commits (ein anderes Repo).
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T12:00:00Z", "2026-11-08T12:00:01Z", "feat/a"),
		commitEvent(f.robin, 2, "2026-11-08T12:10:00Z", "2026-11-08T12:10:01Z", "feat/a"),
		commitEvent(f.robin, 3, "2026-11-08T12:20:00Z", "2026-11-08T12:20:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T12:31:00Z", "2026-11-08T12:31:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[3][:7]}))
	for i := 1; i <= 3; i++ {
		f.wantNone(t, c[i])
	}
}

func TestAPushedRangeCountsOnlyTheCallsSinceTheLastPush(t *testing.T) {
	f, c := pushFix(t)
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		pushEvent(f.robin, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[1][:7]}),
		commitEvent(f.robin, 3, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", "feat/a"),
		commitEvent(f.robin, 4, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 5, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[1][:7], New: c[3][:7]}))
	for i := 1; i <= 3; i++ {
		f.wantSource(t, f.robin, c[i], CommitSourcePushed, false)
	}
}

func TestAnAmendReplacesTheCommitBeforeItInTheCount(t *testing.T) {
	f, c := pushFix(t)
	// Commit um 10:10, Amend um 10:20 (der Commit trägt die Zeit des Amend), ein weiterer um 10:30.
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 2, Kind: GitEventCommit, Op: "amend",
			StartedAt: "2026-11-08T10:19:59Z", EndedAt: "2026-11-08T10:20:01Z", OK: true, Branch: "feat/a"},
		commitEvent(f.robin, 3, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[1][:7], New: c[3][:7]}))
	// Der Bereich c[1]..c[3] hat c[2] und c[3]: zwei Commits, nach dem Amend zwei wirksame Aufrufe.
	f.wantSource(t, f.robin, c[2], CommitSourcePushed, false)
	f.wantSource(t, f.robin, c[3], CommitSourcePushed, false)
}

func TestARebaseInTheWindowMakesTheCountUnusable(t *testing.T) {
	f, c := pushFix(t)
	rebase := SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 2, Kind: GitEventCommit, Op: "rebase",
		StartedAt: "2026-11-08T10:19:59Z", EndedAt: "2026-11-08T10:20:01Z", OK: true, Branch: "feat/a"}
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		rebase,
		commitEvent(f.robin, 3, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: c[0][:7], New: c[3][:7]}))
	for i := 1; i <= 3; i++ {
		if src, _, ok := f.sourceOf(t, f.robin, c[i]); ok && src == CommitSourcePushed {
			t.Errorf("a rebase changes the number of commits; commit %d must not be bound by order", i)
		}
	}
}

func TestANewBranchPushBindsTheCommitsUpToIt(t *testing.T) {
	f, c := pushFix(t)
	// Der Branch war neu: kein Bereich. Die Commits bis zum Push sind die Commits des PRs
	// (c[0] gehört einem anderen Aufruf, den es hier nicht gibt, darum vier Commits für drei Aufrufe).
	f.record(t, 1,
		commitEvent(f.robin, 0, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat/a"),
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		commitEvent(f.robin, 2, "2026-11-08T10:19:59Z", "2026-11-08T10:20:01Z", "feat/a"),
		commitEvent(f.robin, 3, "2026-11-08T10:29:59Z", "2026-11-08T10:30:01Z", "feat/a"),
		pushEvent(f.robin, 4, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", NewBranch: true}))
	for i := 0; i <= 3; i++ {
		f.wantSource(t, f.robin, c[i], CommitSourcePushed, false)
	}
}

func TestAForcedPushBindsTheHeadToTheCallThatMadeIt(t *testing.T) {
	f, c := pushFix(t)
	// Ein Amend um 10:30 (der Kopf trägt diese Zeit) und der Force-Push danach.
	f.record(t, 1,
		SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 1, Kind: GitEventCommit, Op: "amend",
			StartedAt: "2026-11-08T10:29:59Z", EndedAt: "2026-11-08T10:30:01Z", OK: true, Branch: "feat/a"},
		pushEvent(f.robin, 2, "2026-11-08T10:31:00Z", "2026-11-08T10:31:03Z", PushedRef{Branch: "feat/a", Old: sha40("ee")[:7], New: c[3][:7], Forced: true}))
	f.wantSource(t, f.robin, c[3], CommitSourcePushed, false)
	f.wantNone(t, c[2])
}

func TestAPushOfAnotherPersonsRangeBindsNothing(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	f.linkGitHub(t, "person:2", annaLogin, 22)
	c0, c1 := sha40("b0"), sha40("b1")
	f.pull(t, 7, "feat/a",
		prCommit(c0, annaLogin, "2026-11-08T10:00:00Z", "chore: base"),
		prCommit(c1, annaLogin, "2026-11-08T10:10:00Z", "feat: annas work"))
	// Robin meldet einen Push dieses Bereichs mit einem passenden Aufruf: der Autor ist Anna.
	f.record(t, 1,
		commitEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a"),
		pushEvent(f.robin, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]}))
	f.wantNone(t, c1)
}

// ------------------------------------------------------------ Nachricht und Push-Bereich

func TestMessageAndPushRangeTogetherAreTheStrongestSource(t *testing.T) {
	for _, order := range []string{"message then push", "push then message"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			c0, c1 := sha40("b0"), sha40("b1")
			msg := "feat: the one commit"
			f.pull(t, 7, "feat/a", prCommit(c0, robinLogin, "2026-11-08T10:00:00Z", "chore: base"), prCommit(c1, robinLogin, "2026-11-08T10:10:00Z", msg))
			call := msgEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a", msg)
			push := pushEvent(f.robin, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]})
			if order == "message then push" {
				f.record(t, 1, call)
				f.wantSource(t, f.robin, c1, CommitSourceMessage, false)
				f.record(t, 1, push)
			} else {
				f.record(t, 1, push)
				f.record(t, 1, call)
			}
			f.wantSource(t, f.robin, c1, CommitSourceMessagePushed, false)
			links, err := f.st.SessionsForCommit(prProject, c1)
			if err != nil || len(links) != 1 || strings.Join(links[0].Sources, ",") != "message,pushed" || links[0].Weak {
				t.Fatalf("both sources are listed: %+v %v", links, err)
			}
		})
	}
}

func TestBothSignalsStayWeakWhenTheAuthorCannotBeChecked(t *testing.T) {
	// Beide Signale kommen aus dem Client, und wer den PR liest, kennt beide: sie ersetzen den
	// Autor-Abgleich nicht. Die Quelle ist die stärkste, die Zuordnung bleibt ein Kandidat.
	f := gitFixture(t)
	c0, c1 := sha40("b0"), sha40("b1")
	msg := "feat: the one commit"
	f.pull(t, 7, "feat/a", prCommit(c0, "", "2026-11-08T10:00:00Z", "chore: base"), prCommit(c1, "", "2026-11-08T10:10:00Z", msg))
	f.record(t, 1,
		msgEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a", msg),
		pushEvent(f.robin, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]}))
	f.wantSource(t, f.robin, c1, CommitSourceMessagePushed, true)
}

func TestTheMessageWinsOverThePushRangeAmongSessionsOnOneBranch(t *testing.T) {
	for _, order := range []string{"push first", "message first"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			c0, c1 := sha40("b0"), sha40("b1")
			msg := "feat: the one commit"
			f.pull(t, 7, "feat/a", prCommit(c0, robinLogin, "2026-11-08T10:00:00Z", "chore: base"), prCommit(c1, robinLogin, "2026-11-08T10:10:00Z", msg))
			// robin (Worker) committet mit dieser Nachricht; robin2 (Orchestrator, dasselbe Konto, derselbe
			// Branch) hat einen Aufruf in demselben Fenster ohne Nachricht und pusht den Bereich.
			worker := msgEvent(f.robin, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:01Z", "feat/a", msg)
			orch := commitEvent(f.robin2, 1, "2026-11-08T10:09:59Z", "2026-11-08T10:10:02Z", "feat/a")
			push := pushEvent(f.robin2, 2, "2026-11-08T10:11:00Z", "2026-11-08T10:11:03Z", PushedRef{Branch: "feat/a", Old: c0[:7], New: c1[:7]})
			if order == "push first" {
				f.record(t, 1, orch, push)
				f.record(t, 1, worker)
			} else {
				f.record(t, 1, worker)
				f.record(t, 1, orch, push)
			}
			got := f.owners(t, c1)
			if len(got) != 1 || got["s-robin"] != CommitSourceMessage {
				t.Fatalf("the session with the message gets the commit, the one with the range does not: %v", got)
			}
		})
	}
}

func TestEnrichingAKnownEventStartsTheMatch(t *testing.T) {
	// Ein Lauf von vor P6 kannte die Nachricht nicht; ein Bestandslauf trägt sie am bekannten
	// Vorgang nach, und der Abgleich findet den Commit.
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c := sha40("c1")
	msg := "fix: late knowledge"
	f.pull(t, 7, "feat/a", prCommit(c, robinLogin, "2026-11-08T16:06:34Z", msg))
	plain := commitEvent(f.robin, 4, "2026-11-08T16:06:30Z", "2026-11-08T16:06:35Z", "feat/a")
	if res := f.record(t, 1, plain); res.Recorded != 1 {
		t.Fatalf("first: %+v", res)
	}
	f.wantSource(t, f.robin, c, CommitSourceMatched, false) // nur das Zeitfenster
	enriched := msgEvent(f.robin, 4, "2026-11-08T16:06:30Z", "2026-11-08T16:06:35Z", "feat/a", msg)
	res := f.record(t, 1, enriched)
	if res.Recorded != 0 || res.Enriched != 1 {
		t.Fatalf("the known event is enriched, not recorded: %+v", res)
	}
	f.wantSource(t, f.robin, c, CommitSourceMessage, false)
	// Ein zweites Mal ändert nichts.
	if res := f.record(t, 1, enriched); res.Enriched != 0 || res.Recorded != 0 {
		t.Fatalf("repeat: %+v", res)
	}
	var title string
	f.st.db.QueryRow(`SELECT msg_title FROM session_git_events WHERE session_id=? AND seq=4`, f.robin.ID).Scan(&title)
	if title != gitmsg.Of(msg).Title {
		t.Fatalf("stored title key %q", title)
	}
	// Ein zweiter Wert ersetzt den ersten nie.
	other := msgEvent(f.robin, 4, "2026-11-08T16:06:30Z", "2026-11-08T16:06:35Z", "feat/a", "feat: something else entirely")
	if res := f.record(t, 1, other); res.Enriched != 0 {
		t.Fatalf("nothing is replaced: %+v", res)
	}
}

// ------------------------------------------------------------ Schema

func TestStoredEventsHoldHashesNeverTheMessage(t *testing.T) {
	f := gitFixture(t)
	msg := "fix: secret product launch codename"
	f.record(t, 1, msgEvent(f.robin, 1, "2026-11-08T16:06:30Z", "2026-11-08T16:06:35Z", "feat/a", msg))
	rows, err := f.st.db.Query(`SELECT msg_title, msg_full, branch, op FROM session_git_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{a, b} {
			if strings.Contains(strings.ToLower(v), "secret") || (v != "" && !gitmsg.Valid(v)) {
				t.Errorf("stored value %q", v)
			}
		}
	}
}

func TestInvalidKeysAndRefsAreDroppedAndTheEventStays(t *testing.T) {
	f := gitFixture(t)
	bad := msgEvent(f.robin, 1, "2026-11-08T16:06:30Z", "2026-11-08T16:06:35Z", "feat/a", "x")
	bad.MsgTitle, bad.MsgFull = "not a key", strings.Repeat("Z", 32)
	push := pushEvent(f.robin, 2, "2026-11-08T16:07:00Z", "2026-11-08T16:07:03Z",
		PushedRef{Branch: "feat/a", Old: "zzzzzzz", New: "abcdef0123"},
		PushedRef{Branch: "", New: "abcdef0"},
		PushedRef{Branch: "feat/b", New: ""},
		PushedRef{Branch: "feat/c", NewBranch: true})
	res := f.record(t, 1, bad, push)
	if res.Recorded != 2 || res.Rejected != 0 {
		t.Fatalf("%+v", res)
	}
	var title string
	f.st.db.QueryRow(`SELECT msg_title FROM session_git_events WHERE seq=1`).Scan(&title)
	if title != "" {
		t.Errorf("an invalid key is dropped: %q", title)
	}
	rows, err := f.st.db.Query(`SELECT branch, old, new, new_branch FROM session_git_pushes ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var b, o, n string
		var nb int
		rows.Scan(&b, &o, &n, &nb)
		got = append(got, b+"|"+o+"|"+n+"|"+string(rune('0'+nb)))
	}
	want := []string{"feat/a||abcdef0123|0", "feat/c|||1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("refs: %v, want %v", got, want)
	}
}

// Eine Datenbank, die session_commits noch mit den drei alten Quellen hat, wird beim Öffnen
// umgebaut: die Zeilen bleiben, die neuen Quellen gehen, und ein zweites Öffnen ändert nichts.
func TestOpeningAnOldDatabaseRebuildsTheSourcesOfSessionCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertSession(Session{Harness: "claude-code", ExternalID: "s1", AccountID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE session_commits`,
		`CREATE TABLE session_commits(
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
		  weak INTEGER NOT NULL DEFAULT 0 CHECK(weak IN (0,1)),
		  PRIMARY KEY(session_id, sha)) WITHOUT ROWID`,
		`CREATE INDEX session_commits_sha ON session_commits(project, sha)`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := st.DB().Exec(`INSERT INTO session_commits(session_id,project,sha,source,recorded_at,weak,patch_id) VALUES(?,?,?,?,?,1,?)`,
		id, "github.com/x/y", sha40("aa"), "matched", "2026-11-08T00:00:00Z", strings.Repeat("7", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO session_commits(session_id,project,sha,source,recorded_at) VALUES(?,?,?,?,?)`,
		id, "github.com/x/y", sha40("bb"), "message", "2026-11-08T00:00:00Z"); err == nil {
		t.Fatal("the old table must refuse the new source")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		var src string
		var weak int
		var patch string
		if err := st.DB().QueryRow(`SELECT source, weak, patch_id FROM session_commits WHERE sha=?`, sha40("aa")).Scan(&src, &weak, &patch); err != nil || src != "matched" || weak != 1 || patch != strings.Repeat("7", 40) {
			t.Fatalf("open %d: the row survives: %q %d %q %v", round, src, weak, patch, err)
		}
		for _, source := range []string{"message", "pushed", "message+pushed"} {
			if _, err := st.DB().Exec(`INSERT OR REPLACE INTO session_commits(session_id,project,sha,source,recorded_at) VALUES(?,?,?,?,?)`,
				id, "github.com/x/y", sha40("c"+source[:1]), source, "2026-11-08T00:00:00Z"); err != nil {
				t.Fatalf("open %d: source %s: %v", round, source, err)
			}
		}
		var idx int
		st.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'session_commits_%'`).Scan(&idx)
		if idx != 3 {
			t.Fatalf("open %d: the three indexes are back: %d", round, idx)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpeningAnOldDatabaseAddsTheKeysToThePullCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO git_pr_commits(project,pr,sha,title,committed_at,position,recorded_at) VALUES(?,?,?,?,?,0,?)`,
		"github.com/x/y", 5, sha40("aa"), "fix: old row", "2026-11-08T00:00:00Z", "2026-11-08T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO git_commit_meta(project,sha,message,recorded_at) VALUES(?,?,?,?)`,
		"github.com/x/y", sha40("aa"), "the body", "2026-11-08T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var title, full string
	if err := st.DB().QueryRow(`SELECT title_key, msg_key FROM git_pr_commits WHERE sha=?`, sha40("aa")).Scan(&title, &full); err != nil {
		t.Fatal(err)
	}
	want := gitmsg.OfParts("fix: old row", "the body")
	if title != want.Title || full != want.Full || full == "" {
		t.Fatalf("keys %q %q, want %+v", title, full, want)
	}
}
