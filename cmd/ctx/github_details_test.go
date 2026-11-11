package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeDetailPulls liefert zusätzlich Dateien und Prüfstand je Commit und hält fest,
// was gefragt wurde.
type fakeDetailPulls struct {
	fakeCommitPulls
	files    map[string]forge.CommitFiles
	ci       map[string]string
	filesErr error
	ciErr    error
	askedF   []string
	askedCI  []string
	// gone sind Commits, die GitHub nicht (mehr) kennt; ciErrFor lässt die Abfrage des
	// Prüfstands nur für eine Installation scheitern; hold hält CommitFiles an, bis
	// der Kanal geschlossen wird.
	gone      map[string]bool
	ciErrFor  map[int64]error
	hold      chan struct{}
	mu        sync.Mutex
	askedInst []int64
}

func (f *fakeDetailPulls) CommitFiles(_ context.Context, _ int64, _, sha string) (forge.CommitFiles, error) {
	if f.hold != nil {
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askedF = append(f.askedF, sha)
	if f.gone[sha] {
		return forge.CommitFiles{}, forge.ErrCommitUnavailable
	}
	return f.files[sha], f.filesErr
}

func (f *fakeDetailPulls) CommitCI(_ context.Context, inst int64, _, sha string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.askedCI = append(f.askedCI, sha)
	f.askedInst = append(f.askedInst, inst)
	if err := f.ciErrFor[inst]; err != nil {
		return "", err
	}
	if f.gone[sha] {
		return "", forge.ErrCommitUnavailable
	}
	return f.ci[sha], f.ciErr
}

func detailOf(t *testing.T, st *store.Store, sha string) store.CommitDetail {
	t.Helper()
	d, err := st.Access(store.Principal{ID: "person:1"}).CommitView(commitProject, sha)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newDetailPulls() *fakeDetailPulls {
	return &fakeDetailPulls{
		fakeCommitPulls: fakeCommitPulls{
			fakePulls: fakePulls{byBranch: map[string][]forge.PullRequest{"feat/a": {{Number: 7, Title: "A", State: "open", HeadRef: "feat/a", BaseRef: "main"}}}},
			commits: map[int64][]forge.PullCommit{7: {
				{SHA: sha40(1), Title: "eins", Message: "eins\n\nWarum.\n\nCo-authored-by: claude <noreply@anthropic.com>", Parents: []string{sha40(9)}},
				{SHA: sha40(2), Title: "zwei", Message: "zwei", Parents: []string{sha40(1)}},
			}},
		},
		files: map[string]forge.CommitFiles{sha40(1): {Files: []forge.CommitFile{{Path: "a.go", Additions: 3, Deletions: 1}}, Total: 1, Additions: 3, Deletions: 1}},
		ci:    map[string]string{sha40(1): forge.CIFailure, sha40(2): forge.CISuccess},
	}
}

func TestSyncPullsStoresParentsMessageFilesAndChecksOfEachCommit(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("details: %d %s", code, out.String())
	}
	one := detailOf(t, st, sha40(1))
	if len(one.Parents) != 1 || one.Parents[0] != sha40(9) || one.Message != "Warum." || len(one.CoAuthors) != 1 || one.CoAuthors[0] != "claude" {
		t.Fatalf("list data: %+v", one.CommitInfo)
	}
	if !one.FilesKnown || len(one.Files) != 1 || one.Files[0].Path != "a.go" || one.Additions != 3 || one.CI != store.CommitCIFailure {
		t.Fatalf("details: %+v", one.CommitInfo)
	}
	two := detailOf(t, st, sha40(2))
	if two.CI != store.CommitCISuccess || !two.FilesKnown || len(two.Files) != 0 {
		t.Fatalf("a commit without files: %+v", two.CommitInfo)
	}
	// Ein zweiter Lauf fragt nichts mehr: alles ist da und keine Prüfung läuft.
	f.askedF, f.askedCI = nil, nil
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 || len(f.askedF)+len(f.askedCI) != 0 {
		t.Fatalf("second run asked again: %d %v %v %s", code, f.askedF, f.askedCI, out.String())
	}
}

func TestSyncCommitDetailsStopsAskingForARightTheAppLacks(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	f.filesErr = forge.ErrPermissionMissing
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if len(f.askedF) != 1 {
		t.Errorf("the missing right was asked %d times, want once: %v", len(f.askedF), f.askedF)
	}
	if !strings.Contains(out.String(), "Contents: read") {
		t.Errorf("the missing right is not named: %s", out.String())
	}
	d := detailOf(t, st, sha40(1))
	if d.FilesKnown || d.CI != store.CommitCIFailure || d.Message != "Warum." {
		t.Fatalf("what exists must still show: %+v", d.CommitInfo)
	}
}

func TestSyncCommitDetailsStopsAtTheRateLimit(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	var out bytes.Buffer
	_ = syncPulls(context.Background(), st, f, "", &out)
	f.ciErr = &forge.RateLimitError{}
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 1 || !strings.Contains(out.String(), "rate limit") {
		t.Fatalf("rate limit: %d %s", code, out.String())
	}
	if len(f.askedCI) != 1 {
		t.Errorf("the run went on after the limit: %v", f.askedCI)
	}
}

func TestPullWebhookFetchesTheDetailsOfItsCommits(t *testing.T) {
	st := syncStore(t)
	f := newDetailPulls()
	hook, jobs := pullWebhookJobs(st, f, func(err error) { t.Errorf("logged: %v", err) })
	ev := forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 7, Action: "opened", Title: "A", Head: "feat/a", Base: "main", State: "open"}
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	jobs.wait()
	if d := detailOf(t, st, sha40(1)); d.CI != store.CommitCIFailure || len(d.Files) != 1 || d.Message != "Warum." {
		t.Fatalf("after the webhook: %+v", d.CommitInfo)
	}
}

// GitHub gibt nach zehn Sekunden auf: der Webhook antwortet, sobald die Commits des
// PRs stehen, und holt Dateien und Prüfstand erst danach.
func TestPullWebhookAnswersBeforeItFetchesTheDetails(t *testing.T) {
	st := syncStore(t)
	f := newDetailPulls()
	f.hold = make(chan struct{})
	hook, jobs := pullWebhookJobs(st, f, func(err error) { t.Errorf("logged: %v", err) })
	ev := forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 7, Action: "opened", Title: "A", Head: "feat/a", Base: "main", State: "open"}
	returned := make(chan error, 1)
	go func() { returned <- hook(ev) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook waited for the commit details")
	}
	if got := prCommitsOf(t, st, 7); len(got) != 2 {
		t.Fatalf("the commits of the pull request must be there when the webhook answers: %+v", got)
	}
	if d := detailOf(t, st, sha40(1)); d.FilesKnown {
		t.Fatalf("the details arrived before they were released: %+v", d.CommitInfo)
	}
	// Ein zweites Ereignis, solange der Abruf läuft, holt nichts doppelt.
	ev.Action = "synchronize"
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	close(f.hold)
	jobs.wait()
	if d := detailOf(t, st, sha40(1)); !d.FilesKnown || d.CI != store.CommitCIFailure {
		t.Fatalf("after the background fetch: %+v", d.CommitInfo)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]int{}
	for _, sha := range f.askedF {
		seen[sha]++
	}
	for sha, n := range seen {
		if n > 1 {
			t.Errorf("%s fetched %d times by overlapping jobs", sha[:7], n)
		}
	}
}

// Fehlt der App nur "Checks: read", bleiben alle Commits für immer ohne Prüfstand.
// Früher stand dann jeder Lauf bei denselben neuesten Commits fest und die älteren
// kamen nie dran; jetzt füllt sich die Warteschlange nur noch nach den Dateien.
func TestSyncCommitDetailsDoesNotStickOnCommitsLackingOnlyTheChecksRight(t *testing.T) {
	old := detailsPerRun
	detailsPerRun = 2
	t.Cleanup(func() { detailsPerRun = old })
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	f.commits[7] = nil
	f.files = map[string]forge.CommitFiles{}
	for i := 1; i <= 6; i++ {
		f.commits[7] = append(f.commits[7], forge.PullCommit{SHA: sha40(i), Title: fmt.Sprintf("c%d", i), CommittedAt: fmt.Sprintf("2026-11-07T10:%02d:00Z", i)})
		f.files[sha40(i)] = forge.CommitFiles{Files: []forge.CommitFile{{Path: fmt.Sprintf("f%d.go", i), Additions: 1}}, Total: 1, Additions: 1}
	}
	f.ciErr = forge.ErrPermissionMissing
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatal(out.String())
	}
	withFiles := func() int {
		n := 0
		for i := 1; i <= 6; i++ {
			if detailOf(t, st, sha40(i)).FilesKnown {
				n++
			}
		}
		return n
	}
	for run := 1; run <= 3; run++ {
		before := withFiles()
		out.Reset()
		if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 {
			t.Fatalf("run %d: %d %s", run, code, out.String())
		}
		if got := withFiles(); got < before+2 && got < 6 {
			t.Fatalf("run %d got stuck: %d commits with files before, %d after (limit 2 per run)", run, before, got)
		}
	}
	if withFiles() != 6 {
		t.Fatalf("after three runs of two the oldest commits still have no files: %d of 6", withFiles())
	}
	if !strings.Contains(out.String(), "Checks: read") {
		t.Errorf("the missing right is not named: %s", out.String())
	}
	// Ist alles geholt, bleibt vom Lauf nur eine Probe: der neueste Commit fragt nach
	// dem Prüfstand, damit ein später erteiltes Recht auffällt (ein Marker täte das
	// nie). Die anderen Commits werden nicht mehr angefasst.
	f.askedF, f.askedCI = nil, nil
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 || len(f.askedCI) != 1 || len(f.askedF) > 1 {
		t.Fatalf("a finished queue must cost one probe: %d %v %v %s", code, f.askedF, f.askedCI, out.String())
	}
}

// Dasselbe, wenn nur ein einzelner Lauf die Warteschlange nach dem Recht umstellt:
// das Projekt hat mehr Commits als ein Lauf schafft, und der Commit, an dem das Recht
// auffiel, verbraucht nichts vom Vorrat.
func TestFetchProjectDetailsRequeriesWithoutTheGroupItLearnedIsOff(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	f.commits[7] = nil
	f.files = map[string]forge.CommitFiles{}
	for i := 1; i <= 4; i++ {
		f.commits[7] = append(f.commits[7], forge.PullCommit{SHA: sha40(i), CommittedAt: fmt.Sprintf("2026-11-07T10:%02d:00Z", i)})
		f.files[sha40(i)] = forge.CommitFiles{Total: 0}
	}
	f.ciErr = forge.ErrPermissionMissing
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatal(out.String())
	}
	plan := newDetailPlan()
	done, err := fetchProjectDetails(context.Background(), st, f, plan, 99, commitProject, 2, time.Second, func(sha string, err error) bool { t.Errorf("%s: %v", sha, err); return true })
	if err != nil {
		t.Fatal(err)
	}
	if plan.ci || !plan.files {
		t.Fatalf("plan after the run: %+v", plan)
	}
	if done != 3 {
		t.Errorf("the probe plus the two commits of the limit: %d done, want 3", done)
	}
	if len(f.askedCI) != 1 {
		t.Errorf("the missing right must be asked only once: %v", f.askedCI)
	}
}

// Der Plan gilt je Installation: fehlt einer das Recht, fragt die andere weiter.
func TestDetailPlansAreKeptPerInstallation(t *testing.T) {
	st := syncStore(t)
	f := newDetailPulls()
	f.ciErrFor = map[int64]error{1: forge.ErrPermissionMissing}
	plans := detailPlans{}
	for _, inst := range []int64{1, 2, 1, 2} {
		if err := recordCommitDetails(context.Background(), st, f, plans.of(inst), inst, commitProject, sha40(1)); err != nil {
			t.Fatal(err)
		}
	}
	if plans.of(1).ci || !plans.of(1).files {
		t.Errorf("installation 1 lacks the checks right only: %+v", plans.of(1))
	}
	if !plans.of(2).ci || !plans.of(2).files {
		t.Errorf("installation 2 must keep both groups: %+v", plans.of(2))
	}
	asked := map[int64]int{}
	for _, inst := range f.askedInst {
		asked[inst]++
	}
	if asked[1] != 1 || asked[2] != 2 {
		t.Errorf("checks asked per installation: %v, want 1 for the one lacking the right and 2 for the other", asked)
	}
}

// Ein Commit, den GitHub nicht mehr kennt (Force-Push), wird allein übersprungen:
// weder gilt das als fehlendes Recht noch als Fehler des Laufs.
func TestSyncCommitDetailsSkipsOnlyTheCommitGitHubDoesNotKnow(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := newDetailPulls()
	f.gone = map[string]bool{sha40(2): true}
	f.files[sha40(2)] = forge.CommitFiles{}
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("a vanished commit failed the run: %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "does not know the commit") || !strings.Contains(out.String(), "1 skipped, 0 failed") {
		t.Errorf("the skipped commit is not reported: %s", out.String())
	}
	if strings.Contains(out.String(), "lacks") {
		t.Errorf("a vanished commit must not read as a missing right: %s", out.String())
	}
	if d := detailOf(t, st, sha40(1)); !d.FilesKnown || d.CI != store.CommitCIFailure {
		t.Fatalf("the commit next to it must still be fetched: %+v", d.CommitInfo)
	}
	if len(f.askedCI) < 1 {
		t.Errorf("the checks group was switched off for everyone: %v", f.askedCI)
	}
}
