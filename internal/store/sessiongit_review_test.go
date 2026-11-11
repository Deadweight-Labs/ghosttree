package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests zu den Befunden des Reviews von PR #242 (KN-4496): Last, Kaperung,
// fehlgeschlagenes gh pr create, einzeln verworfene Vorgänge, Abfragepläne.

// withLimits setzt die Grenzen der Last für einen Test und stellt sie danach wieder her.
func withLimits(t *testing.T, set func()) {
	t.Helper()
	was := [...]any{maxSessionGitEventsPerSession, maxSessionGitSHAsPerSession, maxSessionGitEventsPerDay,
		maxWindowCandidates, maxWindowCommits, maxResolvePerRun, maxMatchTime}
	t.Cleanup(func() {
		maxSessionGitEventsPerSession, maxSessionGitSHAsPerSession, maxSessionGitEventsPerDay = was[0].(int), was[1].(int), was[2].(int)
		maxWindowCandidates, maxWindowCommits, maxResolvePerRun, maxMatchTime = was[3].(int), was[4].(int), was[5].(int), was[6].(time.Duration)
	})
	set()
}

// linkGitHub verknüpft ein Konto mit einem bewiesenen GitHub-Konto.
func (f *gitFix) linkGitHub(t *testing.T, account string, login string, id int64) {
	t.Helper()
	if err := f.st.SetAccountGitHub(account, login, id); err != nil {
		t.Fatal(err)
	}
}

// weakOf nennt, ob die Zeile der Session zur SHA als schwach gespeichert ist.
func (f *gitFix) weakOf(t *testing.T, s Session, sha string) bool {
	t.Helper()
	var w bool
	if err := f.st.db.QueryRow(`SELECT weak FROM session_commits WHERE session_id=? AND sha=?`, s.ID, sha).Scan(&w); err != nil {
		t.Fatalf("row of %s for %s: %v", s.ExternalID, sha, err)
	}
	return w
}

func quiet(s Session, seq int, start, end string) SessionGitEvent {
	return commitEvent(s, seq, start, end, "feat/a")
}

// ------------------------------------------------------------------ Last

func TestEventsAreCappedPerSession(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxSessionGitEventsPerSession = 5 })
	var batch []SessionGitEvent
	for i := 0; i < 8; i++ {
		batch = append(batch, quiet(f.robin, i, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z"))
	}
	res := f.record(t, 1, batch...)
	if res.Recorded != 5 || res.Capped != 3 {
		t.Fatalf("first batch: %+v", res)
	}
	// Ein weiterer Stapel derselben Session nimmt nichts mehr an, eine andere Session schon.
	res = f.record(t, 1, quiet(f.robin, 20, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z"), quiet(f.robin2, 0, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z"))
	if res.Recorded != 1 || res.Capped != 1 {
		t.Fatalf("second batch: %+v", res)
	}
}

func TestEventsAreCappedPerAccountAndDay(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxSessionGitEventsPerDay = 7 })
	mk := func(s Session, from, n int) []SessionGitEvent {
		var b []SessionGitEvent
		for i := from; i < from+n; i++ {
			b = append(b, quiet(s, i, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z"))
		}
		return b
	}
	if res := f.record(t, 1, mk(f.robin, 0, 5)...); res.Recorded != 5 {
		t.Fatalf("first: %+v", res)
	}
	// Zwei Sessions desselben Kontos teilen sich die Grenze.
	res := f.record(t, 1, mk(f.robin2, 0, 5)...)
	if res.Recorded != 2 || res.Capped != 3 {
		t.Fatalf("second: %+v", res)
	}
	if _, err := f.st.RecordSessionGitEvents(1, "", mk(f.robin, 50, 1)); !errors.Is(err, ErrGitEventQuota) {
		t.Fatalf("over the daily limit: %v", err)
	}
	// Ein anderes Konto hat seine eigene Grenze.
	if res := f.record(t, 2, mk(f.anna, 0, 3)...); res.Recorded != 3 {
		t.Fatalf("another account: %+v", res)
	}
	// Eine Wiederholung eines schon gemeldeten Stapels ändert nichts und zählt nicht weiter.
	var n int
	if err := f.st.db.QueryRow(`SELECT events FROM session_git_quota WHERE account_id=1`).Scan(&n); err != nil || n != 7 {
		t.Fatalf("counted %d: %v", n, err)
	}
}

func TestReportedShasAreCappedPerSession(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxSessionGitSHAsPerSession = 25 })
	var batch []SessionGitEvent
	for i := 0; i < 3; i++ {
		var shas []string
		for j := 0; j < 20; j++ {
			shas = append(shas, hexSHA(i*20+j))
		}
		batch = append(batch, commitEvent(f.robin, i, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z", "feat/a", shas...))
	}
	res := f.record(t, 1, batch...)
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM session_commits WHERE session_id=?`, f.robin.ID).Scan(&n); err != nil || n != 25 || res.Capped == 0 {
		t.Fatalf("%d rows, %+v (%v)", n, res, err)
	}
}

// Eine große Zahl SHAs einer Session darf nicht im Quadrat teurer werden:
// putSessionCommit lud je SHA alle SHAs der Session. Gemessen wird das Verhältnis
// der Zeiten bei vierfacher Menge (linear: 4, quadratisch: 16), nicht eine absolute
// Zeit.
func TestPutSessionCommitScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	withLimits(t, func() {
		maxSessionGitSHAsPerSession = 1 << 30
		maxSessionGitEventsPerSession = 1 << 30
		maxSessionGitEventsPerDay = 1 << 30
	})
	run := func(events int) time.Duration {
		f := gitFixture(t)
		batch := make([]SessionGitEvent, events)
		for i := range batch {
			shas := make([]string, 20)
			for j := range shas {
				shas[j] = hexSHA(i*20 + j)
			}
			batch[i] = commitEvent(f.robin, i, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z", "feat/a", shas...)
		}
		start := time.Now()
		for len(batch) > 0 {
			n := min(len(batch), MaxSessionGitEvents)
			f.record(t, 1, batch[:n]...)
			batch = batch[n:]
		}
		return time.Since(start)
	}
	small, large := run(100), run(400)
	t.Logf("2000 SHAs: %v, 8000 SHAs: %v (x%.1f)", small, large, float64(large)/float64(small))
	if float64(large) > 9*float64(small) {
		t.Fatalf("4x the SHAs took %.1fx the time (%v -> %v): not linear", float64(large)/float64(small), small, large)
	}
}

func TestACommitWithMoreOverlappingWindowsThanTheLimitStaysOpen(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxWindowCandidates = 8 })
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	var batch []SessionGitEvent
	for i := 0; i < 9; i++ {
		batch = append(batch, quiet(f.robin, i, "2026-11-08T09:59:50Z", "2026-11-08T10:00:10Z"))
	}
	res := f.record(t, 1, batch...)
	if res.Matched != 0 || len(f.owners(t, c1)) != 0 {
		t.Fatalf("a crowd of windows decided the commit: %+v %v", res, f.owners(t, c1))
	}
	// Bis zur Grenze bleibt der Abgleich, wie er war.
	f2 := gitFixture(t)
	withLimits(t, func() { maxWindowCandidates = 8 })
	f2.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	if res := f2.record(t, 1, batch[:8]...); res.Matched != 1 || f2.owners(t, c1)["s-robin"] != "matched" {
		t.Fatalf("below the limit: %+v %v", res, f2.owners(t, c1))
	}
}

func TestTheMatchOfOneRunIsBoundedAndTheRestFollows(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxResolvePerRun = 2 })
	var commits []PullCommit
	var shas []string
	for i := 0; i < 5; i++ {
		sha := hexSHA(500 + i)
		shas = append(shas, sha)
		commits = append(commits, PullCommit{SHA: sha, CommittedAt: fmt.Sprintf("2026-11-08T10:00:0%dZ", i)})
	}
	f.pull(t, 7, "feat/a", commits...)
	res := f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:50Z", "2026-11-08T10:00:30Z"))
	if res.Matched != 2 {
		t.Fatalf("a run with a work limit of 2 matched %d", res.Matched)
	}
	// Der nächste Sync des PRs setzt fort und hungert die späteren Commits nicht aus.
	for round := 0; round < 3; round++ {
		f.commits(t, 7, commits...)
	}
	for _, sha := range shas {
		if f.owners(t, sha)["s-robin"] != "matched" {
			t.Errorf("%s is still open after three syncs", sha)
		}
	}
}

func TestTheMatchStopsAtItsTimeLimitWithoutAnError(t *testing.T) {
	f := gitFixture(t)
	withLimits(t, func() { maxMatchTime = -time.Second })
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	res := f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	if res.Recorded != 1 || res.Matched != 0 || f.resolved(t, f.robin, 1) {
		t.Fatalf("%+v resolved=%v", res, f.resolved(t, f.robin, 1))
	}
	maxMatchTime = 3 * time.Second
	f.commits(t, 7, PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	if f.owners(t, c1)["s-robin"] != "matched" {
		t.Fatalf("the next sync did not pick it up: %v", f.owners(t, c1))
	}
}

// ---------------------------------------------------------------- Kaperung

const (
	robinLogin = "robin-gh"
	annaLogin  = "anna-gh"
)

func authored(sha, author, at string) PullCommit {
	return PullCommit{SHA: sha, Author: author, CommittedAt: at}
}

func TestAWindowOfAnotherPersonCannotTakeACommitItsAuthorIsKnownFor(t *testing.T) {
	for _, order := range []string{"hijacker first", "author first"} {
		t.Run(order, func(t *testing.T) {
			f := gitFixture(t)
			f.linkGitHub(t, "person:1", robinLogin, 11)
			f.linkGitHub(t, "person:2", annaLogin, 22)
			c1 := sha40("c1")
			f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
			hijack := func() {
				// Anna meldet ein weites Fenster und den Branch des PRs.
				f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a"))
			}
			real := func() {
				f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
			}
			if order == "hijacker first" {
				hijack()
				if got := f.owners(t, c1); len(got) != 0 {
					t.Fatalf("anna took robin's commit: %v", got)
				}
				real()
			} else {
				real()
				hijack()
			}
			got := f.owners(t, c1)
			if len(got) != 1 || got["s-robin"] != "matched" || f.weakOf(t, f.robin, c1) {
				t.Fatalf("owners after both events: %v", got)
			}
		})
	}
}

func TestAWeakAssignmentNeverHoldsAgainstTheAuthorsOwnEvent(t *testing.T) {
	f := gitFixture(t)
	// Robin hat sein GitHub-Konto bewiesen, anna keines verknüpft: ihr Fenster lässt
	// sich nicht gegen den Autor prüfen und bleibt ein schwacher Kandidat.
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a"))
	if got := f.owners(t, c1); got["s-anna"] != "matched" || !f.weakOf(t, f.anna, c1) {
		t.Fatalf("a window nobody can check is a weak candidate: %v", got)
	}
	links, err := f.st.SessionsForCommit(prProject, c1)
	if err != nil || len(links) != 1 || !links[0].Weak {
		t.Fatalf("the read must say weak: %+v %v", links, err)
	}
	// Das echte Ereignis des Autors ersetzt sie (Regel 4: fremdes Konto nimmt nichts
	// zurück, schreibt aber keine erschlichene Zuordnung fest).
	f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	got := f.owners(t, c1)
	if len(got) != 1 || got["s-robin"] != "matched" || f.weakOf(t, f.robin, c1) {
		t.Fatalf("the author's own event did not win: %v", got)
	}
	links, _ = f.st.SessionsForCommit(prProject, c1)
	if len(links) != 1 || links[0].Weak {
		t.Fatalf("the verified assignment must not be weak: %+v", links)
	}
}

func TestWeakAssignmentsOfTwoAccountsStayFirstComeWhenNothingCanBeChecked(t *testing.T) {
	f := gitFixture(t)
	c1 := sha40("c1")
	// Kein Autor bekannt, kein Konto verknüpft: nichts ist prüfbar.
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a"))
	if got := f.owners(t, c1); len(got) != 1 || got["s-robin"] != "matched" || !f.weakOf(t, f.robin, c1) {
		t.Fatalf("%v", got)
	}
}

func TestAWeakClaimDoesNotBlockTheAuthorsQuietEvent(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	// Anna meldet die volle SHA mit einem weiten Fenster: nicht widerlegbar (ihr Konto
	// ist nicht verknüpft), aber auch kein Beleg gegen den Autor.
	f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a", c1))
	f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	got := f.owners(t, c1)
	if got["s-robin"] != "matched" {
		t.Fatalf("robin's event was blocked by a claim nobody could check: %v", got)
	}
}

func TestAReportedShaOfAnotherAuthorIsNotEvidence(t *testing.T) {
	for _, order := range []string{"commit known first", "report first"} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s short=%v", order, short), func(t *testing.T) {
				f := gitFixture(t)
				f.linkGitHub(t, "person:1", robinLogin, 11)
				f.linkGitHub(t, "person:2", annaLogin, 22)
				c1 := sha40("c1")
				reported := c1
				if short {
					reported = c1[:7]
				}
				report := func() {
					f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a", reported))
				}
				known := func() { f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z")) }
				if order == "commit known first" {
					known()
					report()
				} else {
					if _, err := f.st.RecordPullRequest(prEvent(7, "opened", "open", "feat/a")); err != nil {
						t.Fatal(err)
					}
					report()
					known()
				}
				links, err := f.st.SessionsForCommit(prProject, c1)
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range links {
					if l.SessionID == f.anna.ID && l.Via == "" {
						t.Fatalf("anna's reported hash of robin's commit counts as evidence: %+v", links)
					}
				}
				// Das Ereignis des Autors bleibt unberührt.
				f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
				if f.owners(t, c1)["s-robin"] != "matched" {
					t.Fatalf("robin lost his commit: %v", f.owners(t, c1))
				}
			})
		}
	}
}

func TestTheAuthorOfAReportedShaStillCountsAsEvidence(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	f.record(t, 1, commitEvent(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z", "feat/a", c1[:7]))
	links, err := f.st.SessionsForCommit(prProject, c1)
	if err != nil || len(links) != 1 || links[0].SessionID != f.robin.ID || links[0].Sources[0] != "reported" {
		t.Fatalf("%+v %v", links, err)
	}
	if f.owners(t, c1)[f.robin.ExternalID] != "reported" {
		t.Fatalf("the short hash was not made long: %v", f.owners(t, c1))
	}
}

func TestAnAuthorWhoBecomesKnownLaterRemovesAnAssignmentTheyContradict(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	f.linkGitHub(t, "person:2", annaLogin, 22)
	c1 := sha40("c1")
	// GitHub kennt noch keinen Login zum Commit: anna bekommt ihn als schwachen Kandidaten.
	f.pull(t, 7, "feat/a", PullCommit{SHA: c1, CommittedAt: "2026-11-08T10:00:00Z"})
	f.record(t, 2, commitEvent(f.anna, 1, "2026-11-08T09:40:00Z", "2026-11-08T10:10:00Z", "feat/a"))
	if got := f.owners(t, c1); got["s-anna"] != "matched" || !f.weakOf(t, f.anna, c1) {
		t.Fatalf("setup: %v", got)
	}
	f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	// Der Sync liefert jetzt den Autor.
	f.commits(t, 7, authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	got := f.owners(t, c1)
	if len(got) != 1 || got["s-robin"] != "matched" || f.weakOf(t, f.robin, c1) {
		t.Fatalf("after the author became known: %v", got)
	}
}

func TestALoginThatGitHubDidNotProveIsOnlyWeak(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 0)
	c1 := sha40("c1")
	f.pull(t, 7, "feat/a", authored(c1, robinLogin, "2026-11-08T10:00:00Z"))
	f.record(t, 1, quiet(f.robin, 1, "2026-11-08T09:59:59Z", "2026-11-08T10:00:01Z"))
	if got := f.owners(t, c1); got["s-robin"] != "matched" || !f.weakOf(t, f.robin, c1) {
		t.Fatalf("%v", got)
	}
}

// -------------------------------------------- fehlgeschlagenes gh pr create

func TestAFailedPullRequestCallIsNotRecordedAsCreatedOrMerged(t *testing.T) {
	f := gitFixture(t)
	f.pull(t, 12, "feat/a")
	for _, kind := range []string{GitEventPRCreate, GitEventPRMerge} {
		e := SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 5, Kind: kind,
			StartedAt: "2026-11-08T10:00:00Z", EndedAt: "2026-11-08T10:00:03Z", OK: false, PR: 12, PRRemote: prProject}
		f.record(t, 1, e)
	}
	if links, err := f.st.SessionsForPull(prProject, 12); err != nil || len(links) != 0 {
		t.Fatalf("a failed call linked the session: %+v %v", links, err)
	}
	// Der erfolgreiche Aufruf verbindet weiter.
	f.record(t, 1, SessionGitEvent{Harness: f.robin.Harness, SessionExternalID: f.robin.ExternalID, Seq: 6, Kind: GitEventPRCreate,
		StartedAt: "2026-11-08T10:01:00Z", EndedAt: "2026-11-08T10:01:03Z", OK: true, PR: 12, PRRemote: prProject})
	if links, _ := f.st.SessionsForPull(prProject, 12); len(links) != 1 || links[0].Sources[0] != PRSourceCreated {
		t.Fatalf("%+v", links)
	}
}

// --------------------------------------------- ungültige Vorgänge einzeln

func TestAnInvalidEventDoesNotSinkTheRestOfTheBatch(t *testing.T) {
	f := gitFixture(t)
	good1 := quiet(f.robin, 1, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z")
	ahead := quiet(f.robin, 2, time.Now().UTC().Add(2*time.Hour).Format(time.RFC3339), "")
	odd := quiet(f.robin, 3, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z")
	odd.Kind = "reset"
	good2 := quiet(f.robin, 4, "2026-11-08T10:05:00Z", "2026-11-08T10:05:01Z")
	res, err := f.st.RecordSessionGitEvents(1, "", []SessionGitEvent{good1, ahead, odd, good2})
	if err != nil || res.Recorded != 2 || res.Rejected != 2 || res.Reason == "" {
		t.Fatalf("%+v %v", res, err)
	}
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM session_git_events WHERE session_id=?`, f.robin.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d rows: %v", n, err)
	}
	// Dieselben Vorgänge, ohne den fehlerhaften, ändern nichts.
	if res := f.record(t, 1, good1, good2); res.Recorded != 0 || res.Rejected != 0 {
		t.Fatalf("repeat: %+v", res)
	}
}

func TestATokenBoundToAnotherMachineCannotReportForTheSession(t *testing.T) {
	f := gitFixture(t)
	on := addSession(t, f.st, "s-on-desk", 1, prProject, "desk", VisPrivate, nil)
	e := quiet(on, 1, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z")
	if _, err := f.st.RecordSessionGitEvents(1, "laptop", []SessionGitEvent{e}); !errors.Is(err, ErrGitEventMachine) {
		t.Fatalf("another machine: %v", err)
	}
	if res, err := f.st.RecordSessionGitEvents(1, "DESK", []SessionGitEvent{e}); err != nil || res.Recorded != 1 {
		t.Fatalf("own machine: %+v %v", res, err)
	}
	// Ein Token ohne Bindung und eine Session ohne Maschine bleiben frei.
	if res, err := f.st.RecordSessionGitEvents(1, "", []SessionGitEvent{quiet(on, 2, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z")}); err != nil || res.Recorded != 1 {
		t.Fatalf("unbound token: %+v %v", res, err)
	}
	if res, err := f.st.RecordSessionGitEvents(1, "laptop", []SessionGitEvent{quiet(f.robin, 9, "2026-11-08T10:00:00Z", "2026-11-08T10:00:01Z")}); err != nil || res.Recorded != 1 {
		t.Fatalf("session without machine: %+v %v", res, err)
	}
}

// ------------------------------------------------------ Schema und Pläne

func TestSessionGitTablesOfAnEarlierStateGainTheirColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "earlier.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE session_git_events`, `DROP TABLE session_commits`, `DROP TABLE session_git_quota`,
		`CREATE TABLE session_git_events(id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL, project TEXT NOT NULL DEFAULT '', seq INTEGER NOT NULL,
			kind TEXT NOT NULL, op TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, ended_at TEXT NOT NULL DEFAULT '', ok INTEGER NOT NULL DEFAULT 1,
			branch TEXT NOT NULL DEFAULT '', resolved INTEGER NOT NULL DEFAULT 0, UNIQUE(session_id, seq, kind))`,
		`CREATE INDEX session_git_events_window ON session_git_events(project, kind, started_at) WHERE ok = 1`,
		`CREATE TABLE session_commits(session_id INTEGER NOT NULL, project TEXT NOT NULL, sha TEXT NOT NULL, source TEXT NOT NULL, op TEXT NOT NULL DEFAULT 'commit',
			chunk_seq INTEGER NOT NULL DEFAULT -1, patch_id TEXT NOT NULL DEFAULT '', committed_at TEXT NOT NULL DEFAULT '', recorded_at TEXT NOT NULL,
			PRIMARY KEY(session_id, sha)) WITHOUT ROWID`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	st.Close()
	for round := 1; round <= 2; round++ {
		st, err = Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", round, err)
		}
		for _, q := range []string{`SELECT account_id FROM session_git_events`, `SELECT weak FROM session_commits`, `SELECT events FROM session_git_quota`} {
			rows, err := st.DB().Query(q)
			if err != nil {
				t.Fatalf("open %d: %s: %v", round, q, err)
			}
			rows.Close()
		}
		st.Close()
	}
}

// plan gibt die Zeilen von EXPLAIN QUERY PLAN zurück.
func (f *gitFix) plan(t *testing.T, query string, args ...any) string {
	t.Helper()
	rows, err := f.st.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, query)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// Jede Abfrage, die eine SHA oder ein Fenster gegen eine Tabelle legt, muss mit
// dem Index auf der SHA bzw. der Zeit suchen, nicht nur auf dem Projekt. Der OR aus
// IN und Bereich las dagegen alle Zeilen des Projekts ("SEARCH ... (project=?)"):
// das war der Plan von SessionsForCommit Stufe 1, pullsWithMerge und pullsCarrying.
func TestTheQueriesOfTheMatchUseIndexes(t *testing.T) {
	f := gitFixture(t)
	sha := sha40("ab")
	pref := jsonStrings(shaPrefixes(sha))
	win := []any{prProject, "2026-11-08T09:30:00Z", "2026-11-08T10:00:02Z", "2026-11-08T09:59:58Z", 65}
	for name, q := range map[string]struct {
		sql  string
		args []any
		// want: Teile des Plans, die vorkommen müssen (die Schlüssel der Suche).
		want []string
	}{
		"session_commits exact":    {sqlSessionCommitsExact, []any{prProject, pref}, []string{"session_commits_sha (project=? AND sha=?)"}},
		"session_commits longer":   {sqlSessionCommitsLonger, []any{prProject, sha, sha + "g"}, []string{"session_commits_sha (project=? AND sha>? AND sha<?)"}},
		"pull merge exact":         {sqlPullsMergeExact, []any{prProject, pref}, []string{"git_pull_requests_merge (project=? AND merge_sha=?)"}},
		"pull merge longer":        {sqlPullsMergeLonger, []any{prProject, sha, sha + "g"}, []string{"git_pull_requests_merge (project=? AND merge_sha>? AND merge_sha<?)"}},
		"pull carries exact":       {sqlPullsCarryExact, []any{pref, prProject}, []string{"git_pr_commits_sha (project=? AND sha=?"}},
		"pull carries longer":      {sqlPullsCarryLonger, []any{prProject, sha, sha + "g"}, []string{"git_pr_commits_sha (project=? AND sha>? AND sha<?)"}},
		"own shorter sha":          {sqlOwnShorterSHA, []any{1, pref}, []string{"PRIMARY KEY (session_id=? AND sha=?)"}},
		"own longer sha":           {sqlOwnLongerSHA, []any{1, sha, sha + "g"}, []string{"PRIMARY KEY (session_id=? AND sha>? AND sha<?)"}},
		"pull commits with prefix": {sqlPullCommitsWithPrefix, []any{prProject, sha, sha + "g"}, []string{"git_pr_commits_sha (project=? AND sha>? AND sha<?)"}},
		"commits in a window":      {sqlCommitsInWindow, []any{prProject, "2026-11-08T10:00:00Z", "2026-11-08T10:30:00Z", 300}, []string{"git_pr_commits_time (project=? AND committed_at>? AND committed_at<?)"}},
		"claimants":                {sqlClaimants, []any{1, prProject, pref, "2026-11-08T10:00:02Z", "2026-11-08T09:59:58Z", 65}, []string{"session_commits_sha (project=? AND sha=?)"}},
		"held matches":             {sqlHeldMatches, []any{1, prProject, sha}, []string{"session_commits_sha (project=? AND sha=?)"}},
		"heads of the pull requests": {sqlPullHeads, []any{prProject, sha}, []string{
			"SEARCH k USING COVERING INDEX git_pr_commits_sha (project=? AND sha=?)", "SEARCH p USING"}},
		"pull sessions": {sqlPullSessionCommits, []any{prProject, 7}, []string{
			"SEARCH k USING", "(project=? AND pr=?)", "SEARCH sc USING INDEX session_commits_sha (project=? AND sha=?)"}},
		"events in a window":    {windowEventsSQL(false), append([]any{1}, win...), []string{"session_git_events_span (project=? AND kind=? AND started_at>? AND started_at<?)"}},
		"events of one account": {windowEventsSQL(true), []any{1, prProject, 2, "2026-11-08T09:30:00Z", "2026-11-08T10:00:02Z", "2026-11-08T09:59:58Z", 65}, []string{"session_git_events_account_span (project=? AND kind=? AND account_id=? AND started_at>? AND started_at<?)"}},
	} {
		plan := f.plan(t, q.sql, q.args...)
		for _, w := range q.want {
			if !strings.Contains(plan, w) {
				t.Errorf("%s: the plan lacks %q:\n%s", name, w, plan)
			}
		}
		for _, line := range strings.Split(plan, "\n") {
			l := strings.TrimSpace(line)
			// Eine Zeile "SCAN t" ohne Index liest die Tabelle ganz; "SCAN json_each" ist
			// die Liste der Präfixe.
			if strings.HasPrefix(l, "SCAN ") && !strings.Contains(l, "VIRTUAL TABLE") && !strings.Contains(l, "USING") {
				t.Errorf("%s reads a whole table:\n%s", name, plan)
			}
		}
	}
	// Der Index auf merge_sha ist da.
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('git_pull_requests') WHERE name='git_pull_requests_merge'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index on git_pull_requests(project, merge_sha): %d %v", n, err)
	}
}

// "created" ist eine Behauptung des Collectors: der Autor des PRs, soweit der Server
// ihn kennt, wird gegen das GitHub-Konto der Session gelegt.
func TestACreatedPullRequestMustFitTheAuthorOfThePullRequest(t *testing.T) {
	f := gitFixture(t)
	f.linkGitHub(t, "person:1", robinLogin, 11)
	f.linkGitHub(t, "person:2", annaLogin, 22)
	ev := prEvent(12, "opened", "open", "feat/a")
	ev.Author = annaLogin
	if _, err := f.st.RecordPullRequest(ev); err != nil {
		t.Fatal(err)
	}
	created := func(s Session, seq int) SessionGitEvent {
		return SessionGitEvent{Harness: s.Harness, SessionExternalID: s.ExternalID, Seq: seq, Kind: GitEventPRCreate,
			StartedAt: "2026-11-08T10:00:00Z", EndedAt: "2026-11-08T10:00:03Z", OK: true, PR: 12, PRRemote: prProject}
	}
	// robin meldet die Adresse von annas PR als seine: keine Verbindung.
	f.record(t, 1, created(f.robin, 1))
	if links, _ := f.st.SessionsForPull(prProject, 12); len(links) != 0 {
		t.Fatalf("a forged created link shows: %+v", links)
	}
	// anna hat ihn angelegt: stark.
	f.record(t, 2, created(f.anna, 1))
	links, _ := f.st.SessionsForPull(prProject, 12)
	if len(links) != 1 || links[0].SessionID != f.anna.ID || links[0].Weak {
		t.Fatalf("%+v", links)
	}
}
