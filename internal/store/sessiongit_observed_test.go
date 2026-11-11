package store

import (
	"errors"
	"strings"
	"testing"
)

// observed (IDEA-31, P2): der Collector hat die volle SHA im Reflog gelesen. Für die
// Rechte ist das eine Behauptung wie eine gemeldete SHA; der Server prüft sie gegen
// Zeitfenster und Autor, sobald er die Commits des PRs kennt (KN-4499).

func observedEvent(s Session, seq int, start, end string, obs ...ObservedCommit) SessionGitEvent {
	e := commitEvent(s, seq, start, end, "feat/a")
	e.Observed = obs
	return e
}

func obs(sha string) ObservedCommit {
	return ObservedCommit{SHA: sha, Op: "commit", PatchID: strings.Repeat("ab", 20), CommittedAt: "2026-11-08T10:00:00Z"}
}

func TestAnObservedCommitIsStoredWithItsSourcePatchIDAndKind(t *testing.T) {
	f := gitFixture(t)
	c := sha40("c1")
	res := f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z",
		ObservedCommit{SHA: strings.ToUpper(c), Op: "amend", PatchID: strings.Repeat("CD", 20), CommittedAt: "2026-11-08T10:00:00+00:00"}))
	if res.Recorded != 1 {
		t.Fatalf("one event, got %+v", res)
	}
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceObserved {
		t.Fatalf("the commit is observed: %v", got)
	}
	rows, err := f.st.SessionCommits(f.robin.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	if r := rows[0]; r.Op != "amend" || r.PatchID != strings.Repeat("cd", 20) || r.CommittedAt != "2026-11-08T10:00:00Z" {
		t.Errorf("kind, patch id and time are kept in their clean form: %+v", r)
	}
	if !f.resolved(t, f.robin, 4) {
		t.Error("an event with an observed commit is resolved")
	}
	links, err := f.st.SessionsForCommit(prProject, c)
	if err != nil || len(links) != 1 || links[0].SessionID != f.robin.ID || links[0].Sources[0] != CommitSourceObserved || links[0].Via != "" {
		t.Fatalf("the commit leads to the session as observed: %+v %v", links, err)
	}
	// Ein zweites Senden ändert nichts.
	f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", obs(c)))
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
	if n != 1 {
		t.Fatalf("idempotent, got %d rows", n)
	}
}

func TestAnObservedCommitIsNotContestedByTheTimeWindowOfAnotherSession(t *testing.T) {
	f := gitFixture(t)
	c := sha40("c2")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T10:00:00Z"})
	f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", obs(c)))
	f.record(t, 1, quiet(f.robin2, 5, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	if got := f.owners(t, c); len(got) != 1 || got["s-robin"] != CommitSourceObserved {
		t.Fatalf("an observed commit has its answer, the other window gets nothing: %v", got)
	}
}

func TestObservedCommitsAreCleanedAndBounded(t *testing.T) {
	f := gitFixture(t)
	var list []ObservedCommit
	list = append(list,
		ObservedCommit{SHA: "abc1234"},               // zu kurz: der Reflog liefert nur volle SHAs
		ObservedCommit{SHA: strings.Repeat("g", 40)}, // kein Hex
		ObservedCommit{SHA: sha40("d1") + "0"},       // 41 Zeichen
		ObservedCommit{SHA: sha40("d2"), Op: "wat", PatchID: "not-a-patch-id", CommittedAt: "yesterday"},
		ObservedCommit{SHA: sha40("d2"), Op: "commit"}, // Dublette
	)
	for i := 0; i < 40; i++ {
		list = append(list, ObservedCommit{SHA: strings.Repeat("e", 38) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])})
	}
	f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", list...))
	rows, err := f.st.SessionCommits(f.robin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != maxGitEventSHAs {
		t.Fatalf("at most %d commits per event, got %d", maxGitEventSHAs, len(rows))
	}
	for _, r := range rows {
		if len(r.SHA) != 40 {
			t.Errorf("only full hashes: %q", r.SHA)
		}
		// Eine unbrauchbare Zeit entfällt; ohne sie gilt das Ende des Aufrufs.
		if r.SHA == sha40("d2") && (r.Op != "commit" || r.PatchID != "" || r.CommittedAt != "2026-11-08T10:00:01Z") {
			t.Errorf("an unknown kind becomes commit, a bad patch id and time are dropped: %+v", r)
		}
	}
}

func TestObservedCommitsOnlyBelongToASuccessfulCommitCall(t *testing.T) {
	f := gitFixture(t)
	failed := observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", obs(sha40("a1")))
	failed.OK = false
	push := SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 5, Kind: GitEventPush, OK: true,
		StartedAt: "2026-11-08T10:00:00Z", Observed: []ObservedCommit{obs(sha40("a2"))}}
	f.record(t, 1, failed, push)
	if rows, _ := f.st.SessionCommits(f.robin.ID); len(rows) != 0 {
		t.Fatalf("a failed call and a push observe no commit: %+v", rows)
	}
}

func TestAnotherAccountCannotReportObservedCommitsForASession(t *testing.T) {
	f := gitFixture(t)
	_, err := f.st.RecordSessionGitEvents(1, "", []SessionGitEvent{
		observedEvent(f.anna, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", obs(sha40("a3")))})
	if !errors.Is(err, ErrGitEventSession) {
		t.Fatalf("a foreign session is refused: %v", err)
	}
	var n int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits`).Scan(&n)
	if n != 0 {
		t.Fatalf("nothing is written, got %d rows", n)
	}
}

// Dieselben Fälle wie bei einer gemeldeten SHA (TestAReportedShaOfAnotherAuthorIsNotEvidence),
// für eine beobachtete: der Autor des Commits entscheidet, in beiden Reihenfolgen.
func TestAnObservedShaOfAnotherAuthorIsNotEvidence(t *testing.T) {
	for _, order := range []string{"commit known first", "observation first"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			f.linkGitHub(t, "person:2", annaLogin, 22)
			c1 := sha40("c1")
			observe := func() {
				f.record(t, 2, observedEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", obs(c1)))
			}
			known := func() { f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z")) }
			if order == "commit known first" {
				known()
				observe()
			} else {
				if _, err := f.st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
					t.Fatal(err)
				}
				observe()
				known()
			}
			if got := f.owners(t, c1); got["s-anna"] != "" {
				t.Fatalf("anna's observation of robin's commit is dropped: %v", got)
			}
			// Der Autor bekommt seinen Commit über sein eigenes Ereignis.
			f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
			if got := f.owners(t, c1); got["s-robin"] != CommitSourceMatched || len(got) != 1 {
				t.Fatalf("robin has his commit: %v", got)
			}
		})
	}
}

func TestTheAuthorsOwnObservationStaysEvidence(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	f.record(t, 1, observedEvent(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", obs(c1)))
	links, err := f.st.SessionsForCommit(prProject, c1)
	if err != nil || len(links) != 1 || links[0].SessionID != f.robin.ID || links[0].Sources[0] != CommitSourceObserved || links[0].Weak {
		t.Fatalf("%+v %v", links, err)
	}
}

// Eine Beobachtung, deren Zeit nicht zum Fenster ihres Aufrufs passt, ist nicht aus diesem
// Aufruf: der Commit entstand Stunden davor.
func TestAnObservationThatContradictsTheCommitTimeIsDropped(t *testing.T) {
	f := gitFixture(t)
	c := sha40("c3")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c, CommittedAt: "2026-11-08T16:06:34Z"})
	f.record(t, 2, observedEvent(f.anna, 2, "2026-11-08T12:00:00Z", "2026-11-08T12:00:01Z", obs(c)))
	if got := f.owners(t, c); len(got) != 0 {
		t.Fatalf("a contradicted observation is dropped: %v", got)
	}
}

// Beobachtet und gemeldet: die kurze gemeldete SHA wird zur vollen beobachteten, die
// Patch-ID der Beobachtung bleibt, es gibt eine Zeile.
func TestAnObservedShaExtendsAShortReportedOne(t *testing.T) {
	f := gitFixture(t)
	full := "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z", "feat/a", full[:7]))
	f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z", obs(full)))
	rows, err := f.st.SessionCommits(f.robin.ID)
	if err != nil || len(rows) != 1 || rows[0].SHA != full || rows[0].PatchID == "" {
		t.Fatalf("one full row with the patch id: %+v %v", rows, err)
	}
	if got := f.owners(t, full); got["s-robin"] != CommitSourceReported {
		t.Errorf("the source stays reported (confirmed by the reflog): %v", got)
	}
	// Und umgekehrt: eine kürzere gemeldete SHA nach der vollen legt nichts an.
	f.record(t, 1, commitEvent(f.robin, 4, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z", "feat/a", full[:9]))
	if rows, _ := f.st.SessionCommits(f.robin.ID); len(rows) != 1 {
		t.Errorf("no second row: %+v", rows)
	}
}

func TestObservedCommitsAreCappedPerSession(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxSessionGitSHAsPerSession = 3 })
	var list []ObservedCommit
	for i := 0; i < 8; i++ {
		list = append(list, obs(sha40("b"+string(rune('0'+i)))))
	}
	res := f.record(t, 1, observedEvent(f.robin, 4, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", list...))
	if rows, _ := f.st.SessionCommits(f.robin.ID); len(rows) != 3 || res.Capped == 0 {
		t.Fatalf("the per-session limit holds: %d rows, %+v", len(rows), res)
	}
}
