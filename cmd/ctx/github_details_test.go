package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
}

func (f *fakeDetailPulls) CommitFiles(_ context.Context, _ int64, _, sha string) (forge.CommitFiles, error) {
	f.askedF = append(f.askedF, sha)
	return f.files[sha], f.filesErr
}

func (f *fakeDetailPulls) CommitCI(_ context.Context, _ int64, _, sha string) (string, error) {
	f.askedCI = append(f.askedCI, sha)
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
	hook := pullWebhook(st, f, func(err error) { t.Errorf("logged: %v", err) })
	ev := forge.PullEvent{InstallationID: 99, Remote: commitProject, HeadRemote: commitProject, Number: 7, Action: "opened", Title: "A", Head: "feat/a", Base: "main", State: "open"}
	if err := hook(ev); err != nil {
		t.Fatal(err)
	}
	if d := detailOf(t, st, sha40(1)); d.CI != store.CommitCIFailure || len(d.Files) != 1 || d.Message != "Warum." {
		t.Fatalf("after the webhook: %+v", d.CommitInfo)
	}
}
