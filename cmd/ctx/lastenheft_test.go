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
}

func (f *fakeChecks) PublishCheckRun(_ context.Context, _ int64, repo, sha string, run forge.CheckRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.runs, f.shas = append(f.runs, run), append(f.shas, sha)
	return nil
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

func lhOpen(sha string) forge.PullEvent {
	return forge.PullEvent{InstallationID: 99, Remote: lhProject, HeadRemote: lhProject, Number: 7, Action: "opened", Title: "feat: etwas", Head: "feat/a", Base: "main", State: "open", HeadSHA: sha}
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
	if run := checks.last(t); run.Conclusion != forge.CheckNeutral || run.Name != "ghosttree Lastenheft" || !strings.Contains(run.Title, "No mandatory request") {
		t.Fatalf("empty package: %+v", run)
	}
	if checks.shas[0] != lhSHA1 {
		t.Fatalf("the check run hangs at the head commit: %v", checks.shas)
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
