package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	lhProject = "github.com/deadweight-labs/app"
	lhSHA1    = "1111111111111111111111111111111111111111"
	lhSHA2    = "2222222222222222222222222222222222222222"
)

type fakeChecks struct {
	mu    sync.Mutex
	runs  []forge.CheckRun
	shas  []string
	err   error
	calls int
	// existing sind die Nummern, die der Aufrufer als vorhandenen Check-Run übergab;
	// ids die, die dieser Ersatz vergab.
	existing []int64
	ids      []int64
	// gate hält jeden Aufruf an, bis er geschlossen wird (nil: sofort).
	gate chan struct{}
}

func (f *fakeChecks) PublishCheckRun(_ context.Context, _ int64, repo, sha string, run forge.CheckRun, existingID int64) (int64, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	id := existingID
	if id == 0 {
		id = int64(1000 + len(f.runs))
	}
	f.runs, f.shas = append(f.runs, run), append(f.shas, sha)
	f.existing, f.ids = append(f.existing, existingID), append(f.ids, id)
	return id, nil
}

func (f *fakeChecks) last(t *testing.T) forge.CheckRun {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == 0 {
		t.Fatal("no check run was written")
	}
	return f.runs[len(f.runs)-1]
}

func (f *fakeChecks) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

// lhPackage legt vor dem PR ein Paket mit einem Pflichtrequest für den Branch an; der
// PR bindet es beim Öffnen. Ein leeres Paket bekommt keinen Check-Run.
func lhPackage(t *testing.T, st *store.Store, branch string) {
	t.Helper()
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Pflicht " + branch, Scope: scope.Axes{Project: lhProject}, Person: "robin"},
		Criteria: []string{"eins"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePackage(requestdomain.PackageInput{Project: lhProject, Branch: branch, Name: "P " + branch, Person: "robin",
		Members: []requestdomain.PackageMember{{RequestID: d.Request.ID}}}); err != nil {
		t.Fatal(err)
	}
}

func lhOpen(sha string) forge.PullEvent {
	private := true
	return forge.PullEvent{RepoPrivate: &private, InstallationID: 99, Remote: lhProject, HeadRemote: lhProject, Number: 7, Action: "opened", Title: "feat: etwas", Head: "feat/a", Base: "main", State: "open", HeadSHA: sha}
}

// Der Check-Run folgt dem Paket: neutral, solange nichts Pflicht ist, rot mit offenen
// Pflichtkriterien, grün, wenn alle erfüllt sind; er wird nur geschrieben, wenn sich
// der Commit oder der Stand ändert, und zeigt nur Nummern und Zahlen.
func TestCheckRunFollowsThePackageAndWritesOnlyOnChange(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{}
	var errs []error
	lh := newLastenheftPublisher(st, checks, "https://ghosttree.example", func(err error) { errs = append(errs, err) })
	hook, jobs := pullWebhookWith(st, nil, lh, nil)

	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	lh.sweep(context.Background())
	if checks.count() != 0 {
		t.Fatalf("an empty package has nothing to show, no check run: %+v", checks.runs)
	}

	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "GEHEIMER-TITEL", Scope: scope.Axes{Project: lhProject}, Person: "robin"},
		Criteria: []string{"GEHEIMES-KRITERIUM eins", "zwei"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := st.PackageForPR(lhProject, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPackageRequests(pkg.ID, []requestdomain.PackageMember{{RequestID: d.Request.ID}}, "", "robin"); err != nil {
		t.Fatal(err)
	}
	lh.sweep(context.Background())
	run := checks.last(t)
	if run.Name != "ghosttree Lastenheft" || checks.shas[0] != lhSHA1 {
		t.Fatalf("the check run hangs at the head commit: %+v %v", run, checks.shas)
	}
	if run.Conclusion != forge.CheckFailure || run.Title != "2 of 2 mandatory criteria still open" {
		t.Fatalf("open criteria: %+v", run)
	}
	if strings.Contains(run.Summary, "GEHEIM") || strings.Contains(run.Title, "GEHEIM") {
		t.Errorf("the check run is public on GitHub and must not carry titles or criteria: %s", run.Summary)
	}
	if !strings.Contains(run.Summary, "[REQ-1](https://ghosttree.example/ui/deadweight-labs/app/issues/1)") || !strings.Contains(run.DetailsURL, "/ui/deadweight-labs/app/packages/") {
		t.Errorf("the links lead to ghosttree: %s | %s", run.Summary, run.DetailsURL)
	}

	// Nichts geändert: nichts geschrieben.
	before := checks.count()
	lh.sweep(context.Background())
	if checks.count() != before {
		t.Fatalf("unchanged state was written again: %d -> %d", before, checks.count())
	}

	for i, c := range d.Criteria {
		if err := st.SetCriterionState(c.ID, "met", requestdomain.Evidence{RequestID: d.Request.ID, Kind: "test", Ref: "t", Person: "robin"}); err != nil {
			t.Fatal(err)
		}
		lh.sweep(context.Background())
		want := forge.CheckFailure
		if i == len(d.Criteria)-1 {
			want = forge.CheckSuccess
		}
		if got := checks.last(t); got.Conclusion != want {
			t.Fatalf("after criterion %d: %+v", i+1, got)
		}
	}
	if !strings.Contains(checks.last(t).Title, "All 2 mandatory criteria met") {
		t.Errorf("title: %q", checks.last(t).Title)
	}
	// Dieselbe Prüfung am selben Commit wird aktualisiert, nicht neu angelegt: der
	// erste Run bekam eine Nummer, jede Änderung danach übergab sie.
	if checks.existing[0] != 0 || checks.existing[1] != checks.ids[0] || checks.existing[len(checks.existing)-1] != checks.ids[0] {
		t.Errorf("updates must reuse the check run: existing %v ids %v", checks.existing, checks.ids)
	}

	// Ein neuer Commit (synchronize) bekommt seinen eigenen Check-Run.
	ev := lhOpen(lhSHA2)
	ev.Action = "synchronize"
	before = checks.count()
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if checks.count() != before+1 || checks.shas[len(checks.shas)-1] != lhSHA2 {
		t.Fatalf("the new head commit: %v", checks.shas)
	}
	if checks.existing[len(checks.existing)-1] != 0 {
		t.Errorf("a new commit gets a new check run, not an update of the old one: %v", checks.existing)
	}
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	// Ein geschlossener PR und eine Bearbeitung des Titels schreiben nichts.
	before = checks.count()
	closed := lhOpen(lhSHA2)
	closed.Action, closed.State = "closed", "closed"
	edited := lhOpen(lhSHA2)
	edited.Action = "edited"
	for _, e := range []forge.PullEvent{closed, edited} {
		if err := hook(e); err != nil {
			t.Fatal(err)
		}
	}
	jobs.wait()
	lh.sweep(context.Background())
	if checks.count() != before {
		t.Fatalf("closed or edited wrote %d check runs", checks.count()-before)
	}
}

// Ohne das Recht „Checks: write“ tut der Server nichts: kein Fehler, kein Log, und
// die Installation bleibt eine Stunde in Ruhe; danach wirkt ein erteiltes Recht.
func TestCheckRunWithoutTheRightDoesNothingAndTriesAgainLater(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{err: forge.ErrPermissionMissing}
	var errs []error
	lh := newLastenheftPublisher(st, checks, "", func(err error) { errs = append(errs, err) })
	clock := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	lh.now = func() time.Time { return clock }
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")

	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatalf("the webhook must not fail without the right: %v", err)
	}
	jobs.wait()
	lh.sweep(context.Background())
	lh.sweep(context.Background())
	if checks.calls != 1 || len(errs) != 0 {
		t.Fatalf("calls %d errors %v: one try, then rest", checks.calls, errs)
	}
	if sha, digest, _ := st.LastenheftPublished(lhProject, 7); sha != "" || digest != "" {
		t.Fatalf("nothing was published, nothing may be marked: %q %q", sha, digest)
	}

	// Eine Stunde später, mit dem Recht: es wird geschrieben.
	checks.err = nil
	clock = clock.Add(lastenheftRetryNoRight + time.Minute)
	lh.sweep(context.Background())
	if checks.count() != 1 {
		t.Fatalf("after the hour with the right: %d runs, %d calls", checks.count(), checks.calls)
	}
}

// Andere Fehler kommen zum Log, und der Stand wird nicht als geschrieben vermerkt.
func TestCheckRunErrorIsReportedAndRetried(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{err: context.DeadlineExceeded}
	var errs []error
	lh := newLastenheftPublisher(st, checks, "", func(err error) { errs = append(errs, err) })
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")
	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	checks.err = nil
	lh.sweep(context.Background())
	if checks.count() != 1 {
		t.Fatalf("the failed one is written by the next sweep: %d", checks.count())
	}
}

// Ein öffentliches Repo bekommt keinen Check-Run, auch wenn das ghosttree-Projekt privat
// ist und das Paket Requests trägt: der Check-Run stünde für jeden lesbar auf GitHub
// (Request-Nummern, Fortschritt, Paketnummer). Dasselbe gilt, wenn GitHub die
// Sichtbarkeit nicht gemeldet hat.
func TestCheckRunIsNotWrittenForAPublicOrUnknownRepo(t *testing.T) {
	for name, private := range map[string]*bool{"public": ptr(false), "unknown": nil} {
		t.Run(name, func(t *testing.T) {
			st := syncStore(t)
			checks := &fakeChecks{}
			lh := newLastenheftPublisher(st, checks, "https://ghosttree.example", nil)
			hook, jobs := pullWebhookWith(st, nil, lh, nil)
			lhPackage(t, st, "feat/a")
			ev := lhOpen(lhSHA1)
			ev.RepoPrivate = private
			if err := hook(ev); err != nil {
				t.Fatal(err)
			}
			jobs.wait()
			lh.sweep(context.Background())
			ev.Action, ev.HeadSHA = "synchronize", lhSHA2
			if err := hook(ev); err != nil {
				t.Fatal(err)
			}
			jobs.wait()
			lh.sweep(context.Background())
			if checks.calls != 0 {
				t.Fatalf("PublishCheckRun was called %d times for a %s repo", checks.calls, name)
			}
			// Sobald GitHub das Repo als privat meldet, wird geschrieben.
			ev.RepoPrivate = ptr(true)
			ev.Action = "edited"
			if err := hook(ev); err != nil {
				t.Fatal(err)
			}
			lh.sweep(context.Background())
			if checks.count() != 1 {
				t.Fatalf("a private repo gets its check run: %d", checks.count())
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// Ohne eine eigens gesetzte Adresse steht kein Link im Check-Run und keine Adresse des
// Servers; mit ihr wie bisher.
func TestCheckRunHasNoLinksWithoutAPublicAddress(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{}
	lh := newLastenheftPublisher(st, checks, "", nil)
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")
	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	run := checks.last(t)
	if run.DetailsURL != "" || strings.Contains(run.Summary, "http") || strings.Contains(run.Summary, "](") || strings.Contains(run.Summary, "package page") {
		t.Fatalf("no address, no link: %q | %q", run.DetailsURL, run.Summary)
	}
	if !strings.Contains(run.Summary, "REQ-1") {
		t.Fatalf("the numbers stay: %s", run.Summary)
	}
}

// Kennt GitHub den Commit nicht (Force-Push), ist er bis zum nächsten Head-Commit
// erledigt: kein Versuch bei jedem Durchlauf.
func TestCheckRunForAnUnknownCommitIsDoneUntilTheNextHead(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{err: forge.ErrCommitUnavailable}
	var errs []error
	lh := newLastenheftPublisher(st, checks, "", func(err error) { errs = append(errs, err) })
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")
	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	for i := 0; i < 3; i++ {
		lh.sweep(context.Background())
	}
	if checks.calls != 1 || len(errs) != 0 {
		t.Fatalf("calls %d errors %v: one try for the commit, then done", checks.calls, errs)
	}
	ev := lhOpen(lhSHA2)
	ev.Action = "synchronize"
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if checks.calls != 2 {
		t.Fatalf("the next head commit is tried again: %d calls", checks.calls)
	}
}

// Im Ratenlimit ruht die Installation bis zum Reset, auch für die Webhook-Aufträge;
// danach wird weitergeschrieben.
func TestCheckRunPausesUntilTheRateLimitResets(t *testing.T) {
	st := syncStore(t)
	clock := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	reset := clock.Add(20 * time.Minute)
	checks := &fakeChecks{err: &forge.RateLimitError{Reset: reset}}
	var errs []error
	lh := newLastenheftPublisher(st, checks, "", func(err error) { errs = append(errs, err) })
	lh.now = func() time.Time { return clock }
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")
	if err := hook(lhOpen(lhSHA1)); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if checks.calls != 1 || len(errs) != 1 {
		t.Fatalf("calls %d errors %v", checks.calls, errs)
	}
	clock = clock.Add(19 * time.Minute)
	lh.sweep(context.Background())
	ev := lhOpen(lhSHA2)
	ev.Action = "synchronize"
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if checks.calls != 1 {
		t.Fatalf("before the reset nothing may be asked: %d calls", checks.calls)
	}
	checks.err = nil
	clock = clock.Add(2 * time.Minute)
	lh.sweep(context.Background())
	if checks.count() != 1 || checks.shas[0] != lhSHA2 {
		t.Fatalf("after the reset the current head is written: %v", checks.shas)
	}
}

// Die Aufträge heißen wie der PR: ein langsamer PR hält die anderen des Projekts nicht
// auf und verschluckt ihren Check-Run nicht.
func TestCheckRunJobsOfDifferentPullRequestsDoNotBlockEachOther(t *testing.T) {
	st := syncStore(t)
	checks := &fakeChecks{gate: make(chan struct{})}
	lh := newLastenheftPublisher(st, checks, "", nil)
	hook, jobs := pullWebhookWith(st, nil, lh, nil)
	lhPackage(t, st, "feat/a")
	lhPackage(t, st, "feat/b")
	first, second := lhOpen(lhSHA1), lhOpen(lhSHA2)
	second.Number, second.Head = 8, "feat/b"
	if err := hook(first); err != nil {
		t.Fatal(err)
	}
	if err := hook(second); err != nil { // der Auftrag des ersten PRs hängt noch am Gate
		t.Fatal(err)
	}
	close(checks.gate)
	jobs.wait()
	if checks.count() != 2 {
		t.Fatalf("both pull requests get their check run, got %d", checks.count())
	}
}
