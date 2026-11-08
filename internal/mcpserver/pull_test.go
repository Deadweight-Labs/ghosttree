package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestPackageReviewNamesTheKnowledgeWrittenOnThePullRequest(t *testing.T) {
	ctx := context.Background()
	s, st := pkgServer(t, "feat/pkg", "main")
	org, err := st.CreateOrg("person:1", "Xorg", "xorg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", org.ID, "github", "x", 5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimProject("person:1", pkgProject, "xorg"); err != nil {
		t.Fatal(err)
	}
	reqs := pkgRequests(t, st, "Kern")
	if _, _, err := s.handlePackageUpdate(ctx, nil, PackageUpdateInput{Action: "create", Name: "P", Requests: []PackageRequestRef{{RequestID: reqs[0].Request.ID}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "decision", Title: "Entscheidung auf dem PR", Body: "b", Person: "alice",
		Scope:     scope.Axes{Project: pkgProject, Branch: "feat/pkg"},
		GitAnchor: &gitanchor.Anchor{Commit: "aaaaaaa1111111111111111111111111111111aa", Branch: "feat/pkg"}}); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.handlePackageReview(ctx, nil, PackageGetInput{})
	if err != nil || strings.Contains(text(t, res), "Knowledge written on PR") {
		t.Fatalf("before a PR: %v %s", err, text(t, res))
	}
	if _, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 5, Remote: pkgProject, HeadRemote: pkgProject, Number: 3,
		Action: "opened", Head: "feat/pkg", Base: "main", State: "open"}); err != nil {
		t.Fatal(err)
	}
	res, out, err := s.handlePackageReview(ctx, nil, PackageGetInput{})
	if err != nil || !out.Found || out.Detail.PR != 3 {
		t.Fatalf("review: %+v %v", out, err)
	}
	if got := text(t, res); !strings.Contains(got, "Knowledge written on PR #3 (1 entries") || !strings.Contains(got, "[decision] Entscheidung auf dem PR") {
		t.Fatalf("review text:\n%s", got)
	}
}

func TestDescriptionsGoToTheBranchLayerOnlyOffTheDefaultBranch(t *testing.T) {
	s := &Server{ctxAxes: scope.Axes{Project: pkgProject}}
	if got := s.ghostBranch(); got != "" {
		t.Fatalf("without a live state: %q", got)
	}
	s.SetLive(func() Live { return Live{Branch: "feat/x", Lineage: []string{"main"}} })
	if got := s.ghostBranch(); got != "feat/x" {
		t.Fatalf("feature branch: %q", got)
	}
	s.SetLive(func() Live { return Live{Branch: "main", Trunk: true} })
	if got := s.ghostBranch(); got != "" {
		t.Fatalf("default branch: %q", got)
	}
	s.SetLive(func() Live { return Live{} })
	if got := s.ghostBranch(); got != "" {
		t.Fatalf("detached head: %q", got)
	}
}
