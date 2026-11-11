package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const commitProject = "github.com/deadweight-labs/app"

func sha40(n int) string { return fmt.Sprintf("%040x", n) }

// fakeCommits liefert die Commits je PR und hält fest, was gefragt wurde.
type fakeCommitPulls struct {
	fakePulls
	commits map[int64][]forge.PullCommit
	cerr    error
	asked   []string
}

func (f *fakeCommitPulls) PullRequestCommits(_ context.Context, inst int64, repo string, n int64) ([]forge.PullCommit, error) {
	f.asked = append(f.asked, fmt.Sprintf("%d %s#%d", inst, repo, n))
	if f.cerr != nil {
		return nil, f.cerr
	}
	return f.commits[n], nil
}

func prCommitsOf(t *testing.T, st *store.Store, n int64) []store.CommitInfo {
	t.Helper()
	list, err := st.Access(store.Principal{ID: "person:1"}).PullCommits(commitProject, n)
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestPullWebhookRecordsTheCommitsOfAnOpenedAndASynchronizedPullRequest(t *testing.T) {
	st := syncStore(t)
	f := &fakeCommitPulls{commits: map[int64][]forge.PullCommit{
		7: {{SHA: sha40(1), Author: "mia-gh", AuthorName: "Mia", Title: "erste Zeile", CommittedAt: "2026-11-07T10:00:00Z"}},
	}}
	var logged []error
	hook := pullWebhook(st, f, func(err error) { logged = append(logged, err) })
	ev := forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 7, Action: "opened", Title: "A", Head: "feat/a", Base: "main", State: "open", Author: "mia-gh"}
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	got := prCommitsOf(t, st, 7)
	if len(got) != 1 || got[0].SHA != sha40(1) || got[0].Title != "erste Zeile" || got[0].Author != "mia-gh" {
		t.Fatalf("after opened: %+v", got)
	}
	// Ein neuer Push: der Stand von GitHub ersetzt den alten.
	f.commits[7] = []forge.PullCommit{{SHA: sha40(1), Title: "erste Zeile"}, {SHA: sha40(2), Title: "zweite"}}
	ev.Action = "synchronize"
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	if got := prCommitsOf(t, st, 7); len(got) != 2 || got[1].SHA != sha40(2) {
		t.Fatalf("after synchronize: %+v", got)
	}
	// Ein Force-Push lässt einen Commit fallen: er gehört nicht mehr zum PR.
	f.commits[7] = []forge.PullCommit{{SHA: sha40(3), Title: "neu"}}
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	if got := prCommitsOf(t, st, 7); len(got) != 1 || got[0].SHA != sha40(3) {
		t.Fatalf("after the force push: %+v", got)
	}
	// Aktionen ohne neue Commits fragen GitHub nicht.
	f.asked = nil
	for _, action := range []string{"edited", "closed"} {
		ev.Action = action
		if err := hook(ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.asked) != 0 {
		t.Errorf("asked GitHub on %v", f.asked)
	}
	if len(logged) != 0 {
		t.Errorf("logged: %v", logged)
	}
}

// Ein Fehler beim Abholen der Commits darf den Webhook nicht scheitern lassen
// (GitHub würde dasselbe Ereignis erneut schicken); er wird gemeldet.
func TestPullWebhookSurvivesFailingCommitFetchAndReportsIt(t *testing.T) {
	st := syncStore(t)
	for _, failure := range []error{
		&forge.RateLimitError{Reset: time.Now().Add(time.Minute)}, forge.ErrPermissionMissing, errors.New("github answered 502"),
	} {
		f := &fakeCommitPulls{cerr: failure}
		var logged []error
		hook := pullWebhook(st, f, func(err error) { logged = append(logged, err) })
		ev := forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 8, Action: "opened", Title: "B", Head: "feat/b", Base: "main", State: "open"}
		if err := hook(ev); err != nil {
			t.Fatalf("%v: the webhook failed: %v", failure, err)
		}
		if len(logged) != 1 || !errors.Is(logged[0], failure) || !strings.Contains(logged[0].Error(), commitProject+"#8") {
			t.Errorf("%v: logged %v", failure, logged)
		}
	}
	// Die PR-Zeile steht trotzdem.
	if _, err := st.Access(store.Principal{ID: "person:1"}).PullRequestView(commitProject, 8); err != nil {
		t.Fatalf("the pull request was not recorded: %v", err)
	}
	// Ohne GitHub-Zugang (kein commitLister) bleibt es beim PR.
	hook := pullWebhook(st, nil, nil)
	if err := hook(forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 9, Action: "opened", Head: "feat/c", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
}

// Ereignisse, die nicht zu einem Projekt dieser Installation gehören, holen nichts:
// ein Fremder kann den Server nicht dazu bringen, GitHub abzufragen oder zu speichern.
func TestPullWebhookIgnoresAnEventOfAnotherInstallationOrAForkWithoutAsking(t *testing.T) {
	st := syncStore(t)
	f := &fakeCommitPulls{commits: map[int64][]forge.PullCommit{5: {{SHA: sha40(9), Title: "x"}}}}
	hook := pullWebhook(st, f, nil)
	for name, ev := range map[string]forge.PullEvent{
		"other installation": {InstallationID: 1234, Remote: commitProject, HeadRemote: commitProject, Number: 5, Action: "opened", Head: "feat/a", Base: "main", State: "open"},
		"fork":               {InstallationID: 99, Remote: commitProject, HeadRemote: "github.com/someone/fork", Number: 5, Action: "opened", Head: "feat/a", Base: "main", State: "open"},
		"other owner":        {InstallationID: 99, Remote: "github.com/someone-else/app", HeadRemote: "github.com/someone-else/app", Number: 5, Action: "opened", Head: "feat/a", Base: "main", State: "open"},
	} {
		if err := hook(ev); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(f.asked) != 0 {
		t.Fatalf("GitHub was asked for foreign events: %v", f.asked)
	}
}

func TestStoreRefusesCommitsForAnUnknownPullRequestOrInstallation(t *testing.T) {
	st := syncStore(t)
	in := store.PullCommitsChange{InstallationID: 99, Remote: commitProject, Number: 5, Commits: []store.PullCommit{{SHA: sha40(1), Title: "x"}}}
	if res, err := st.RecordPullCommits(in); err != nil || res.Applied {
		t.Fatalf("unknown pull request: %+v %v", res, err)
	}
	if _, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 5, Action: "opened", Head: "feat/a", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	other := in
	other.InstallationID = 1234
	if res, err := st.RecordPullCommits(other); err != nil || res.Applied {
		t.Fatalf("foreign installation: %+v %v", res, err)
	}
	bad := in
	bad.Commits = []store.PullCommit{{SHA: "nothex"}, {SHA: "abc"}, {SHA: sha40(2), Author: "bad\nlogin", AuthorName: "N\x00ame", Title: "t\nu‮g", CommittedAt: "gestern"}}
	res, err := st.RecordPullCommits(bad)
	if err != nil || !res.Applied || res.Recorded != 1 {
		t.Fatalf("only the valid commit is recorded: %+v %v", res, err)
	}
	got := prCommitsOf(t, st, 5)
	if len(got) != 1 || got[0].Author != "" || strings.ContainsAny(got[0].AuthorName+got[0].Title, "\n\x00‮") {
		t.Fatalf("foreign text must be cleaned: %+v", got)
	}
}

func TestSyncPullsFetchesTheCommitsAndStopsAtTheRateLimit(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := &fakeCommitPulls{
		fakePulls: fakePulls{byBranch: map[string][]forge.PullRequest{"feat/a": {{Number: 7, Title: "A", State: "open", HeadRef: "feat/a", BaseRef: "main", Author: "mia-gh"}}}},
		commits:   map[int64][]forge.PullCommit{7: {{SHA: sha40(1), Title: "eins"}, {SHA: sha40(2), Title: "zwei"}}},
	}
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "1 pull requests with their commits fetched") {
		t.Fatalf("summary: %s", out.String())
	}
	if got := prCommitsOf(t, st, 7); len(got) != 2 {
		t.Fatalf("commits: %+v", got)
	}
	d, err := st.Access(store.Principal{ID: "person:1"}).PullRequestView(commitProject, 7)
	if err != nil || d.Author != "mia-gh" {
		t.Fatalf("author from the sync: %+v %v", d, err)
	}
	// Ratenlimit: abbrechen, sagen, wann es wieder geht, und nicht weitermachen.
	know(t, st, "feat/b")
	f.byBranch["feat/b"] = []forge.PullRequest{{Number: 8, Title: "B", State: "open", HeadRef: "feat/b", BaseRef: "main"}}
	f.cerr = &forge.RateLimitError{Reset: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	out.Reset()
	if code := syncPulls(context.Background(), st, f, "", &out); code != 1 || !strings.Contains(out.String(), "rate limit") || !strings.Contains(out.String(), "2026-11-08T12:00:00Z") {
		t.Fatalf("rate limit: %d %s", code, out.String())
	}
	f.cerr = forge.ErrPermissionMissing
	know(t, st, "feat/c")
	f.byBranch["feat/c"] = []forge.PullRequest{{Number: 9, Title: "C", State: "open", HeadRef: "feat/c", BaseRef: "main"}}
	out.Reset()
	if code := syncPulls(context.Background(), st, f, "", &out); code != 1 || !strings.Contains(out.String(), "Pull requests: read") {
		t.Fatalf("permission: %d %s", code, out.String())
	}
}

// sync-prs --commits holt die Commits bekannter PRs nach, zu denen noch keine
// gespeichert sind (PRs von vor dem Speichern der Commits oder mit gescheitertem
// Abruf), und lässt PRs mit Commits in Ruhe.
func TestSyncPullCommitsFetchesWhatIsMissingOnce(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	f := &fakeCommitPulls{
		fakePulls: fakePulls{byBranch: map[string][]forge.PullRequest{"feat/a": {{Number: 7, Title: "A", State: "merged", HeadRef: "feat/a", BaseRef: "main"}}}},
		commits:   map[int64][]forge.PullCommit{7: {{SHA: sha40(1), Title: "eins"}}},
		cerr:      errors.New("timeout"),
	}
	var out bytes.Buffer
	_ = syncPulls(context.Background(), st, f, "", &out)
	if got := prCommitsOf(t, st, 7); len(got) != 0 {
		t.Fatalf("commits despite the failed fetch: %+v", got)
	}
	f.cerr = nil
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 || !strings.Contains(out.String(), "1 pull requests without stored commits fetched") {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if got := prCommitsOf(t, st, 7); len(got) != 1 {
		t.Fatalf("commits after --commits: %+v", got)
	}
	out.Reset()
	if code := syncPullCommits(context.Background(), st, f, "", &out); code != 0 || !strings.Contains(out.String(), "0 pull requests without stored commits fetched") {
		t.Fatalf("second run: %d %s", code, out.String())
	}
}
