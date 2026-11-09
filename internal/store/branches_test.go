package store

import (
	"reflect"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestProjectBranchesCollectsEverySourceOnceAndStaysInTheProject(t *testing.T) {
	s := openTest(t)
	const p = "github.com/o/r"
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "a", Body: "b", Scope: scope.Axes{Project: p, Branch: "feat/knowledge"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "other", Body: "b", Scope: scope.Axes{Project: "github.com/o/other", Branch: "feat/elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "no branch", Body: "b", Scope: scope.Axes{Project: p}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "e1", Scope: scope.Axes{Project: p, Branch: "feat/session"}, StartedAt: now(), LastSeenAt: now()}); err != nil {
		t.Fatal(err)
	}
	// Ein Git-Anker an einer Beschreibung nennt seinen Branch, auch ohne Wissen.
	if _, err := s.PutGhostFile(GhostFile{Project: p, Path: "internal/a.go", Description: "d",
		GitAnchor: &gitanchor.Anchor{Commit: "abcdef1", Branch: "feat/knowledge"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGhostFile(GhostFile{Project: p, Path: "internal/b.go", Description: "d",
		GitAnchor: &gitanchor.Anchor{Commit: "abcdef2", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectBranches(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"feat/knowledge", "main"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("branches = %v, want %v (sorted, once each, no other project, no empty name, no session branch)", got, want)
	}
	none, err := s.ProjectBranches("github.com/o/unknown")
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown project: %v, %v", none, err)
	}
}
