package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakePulls struct {
	byBranch map[string][]forge.PullRequest
	err      error
	asked    []string
}

func (f *fakePulls) PullRequests(_ context.Context, inst int64, repo, branch string) ([]forge.PullRequest, error) {
	f.asked = append(f.asked, repo+"@"+branch)
	if f.err != nil {
		return nil, f.err
	}
	return f.byBranch[branch], nil
}

func syncStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	org, err := st.CreateOrg("person:1", "Deadweight", "dw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "deadweight-labs", 99, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", "github.com/deadweight-labs/app", "dw"); err != nil {
		t.Fatal(err)
	}
	return st
}

func know(t *testing.T, st *store.Store, branch string) int64 {
	t.Helper()
	id, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "auf " + branch, Body: "text", Person: "robin",
		Scope:     scope.Axes{Project: "github.com/deadweight-labs/app", Branch: branch},
		GitAnchor: &gitanchor.Anchor{Commit: "aaaaaaa1111111111111111111111111111111aa", Branch: branch}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSyncPullsTagsExistingBranchesAndNamesTheNumbers(t *testing.T) {
	st := syncStore(t)
	onA, onB := know(t, st, "feat/a"), know(t, st, "feat/b")
	f := &fakePulls{byBranch: map[string][]forge.PullRequest{
		"feat/a": {{Number: 7, Title: "A", State: "open", HeadRef: "feat/a", BaseRef: "main"}},
	}}
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, f, "", &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "2 branches asked, 1 pull requests recorded, 1 anchors tagged, 0 failed") {
		t.Fatalf("summary: %s", out.String())
	}
	brief, err := st.PullRequestBriefOf("github.com/deadweight-labs/app", 7)
	if err != nil || len(brief.Knowledge) != 1 || brief.Knowledge[0].ID != onA {
		t.Fatalf("brief: %+v %v", brief, err)
	}
	if other, _ := st.PullRequestBriefOf("github.com/deadweight-labs/app", 8); len(other.Knowledge) != 0 {
		t.Fatalf("feat/b has no PR: %+v (%d)", other, onB)
	}
	// Läuft wieder: nur noch feat/b ist ohne Nummer.
	f.asked = nil
	out.Reset()
	syncPulls(context.Background(), st, f, "github.com/deadweight-labs/app", &out)
	if len(f.asked) != 1 || f.asked[0] != "github.com/deadweight-labs/app@feat/b" {
		t.Fatalf("asked again: %v", f.asked)
	}
}

func TestSyncPullsStopsWithoutThePullRequestPermission(t *testing.T) {
	st := syncStore(t)
	know(t, st, "feat/a")
	var out bytes.Buffer
	if code := syncPulls(context.Background(), st, &fakePulls{err: forge.ErrPermissionMissing}, "", &out); code != 1 || !strings.Contains(out.String(), "Pull requests: read") {
		t.Fatalf("code/out: %s", out.String())
	}
}

func TestGithubCommandNeedsTheDatabaseAndAnApp(t *testing.T) {
	var out bytes.Buffer
	if code := cmdGithub([]string{"sync-prs"}, &out); code != 2 || !strings.Contains(out.String(), "usage: ctx github sync-prs --db") {
		t.Fatalf("no db: %d %s", code, out.String())
	}
	t.Setenv(forge.EnvAppID, "")
	out.Reset()
	if code := cmdGithub([]string{"sync-prs", "--db", t.TempDir() + "/x.db"}, &out); code != 1 || !strings.Contains(out.String(), "not set up") {
		t.Fatalf("no app: %d %s", code, out.String())
	}
}
