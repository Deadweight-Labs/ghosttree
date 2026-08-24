package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Entries written before the default changed carry the branch of whichever
// session happened to write them. They stay invisible from every other branch
// until they are lifted.
func TestUnbindBranchLiftsKnowledgeToItsProject(t *testing.T) {
	s := openTest(t)
	stranded, _ := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "stale binary", Body: "b",
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "v0"}, Confidence: "trusted"})
	deliberate, _ := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "dual write window", Body: "b",
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "feat/migration"}, Confidence: "trusted"})
	machineWide, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "ollama inventory", Body: "b",
		Scope: scope.Axes{Machine: "workstation-a"}, Confidence: "trusted"})

	// A dry run reports and changes nothing.
	preview, err := s.UnbindBranchScope([]int64{stranded}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || preview[0].ID != stranded {
		t.Fatalf("dry run = %+v, want the one requested entry", preview)
	}
	if k, _ := s.KnowledgeByID(stranded); k.Scope.Branch != "v0" {
		t.Error("dry run must not write")
	}

	if _, err := s.UnbindBranchScope([]int64{stranded}, false); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.KnowledgeByID(stranded); k.Scope.Branch != "" || k.Scope.Project != "github.com/x/y" {
		t.Errorf("lifted entry = %+v, want project scope", k.Scope)
	}
	if k, _ := s.KnowledgeByID(deliberate); k.Scope.Branch != "feat/migration" {
		t.Errorf("an entry that was not named must keep its branch, got %+v", k.Scope)
	}
	if k, _ := s.KnowledgeByID(machineWide); k.Scope.Machine != "workstation-a" {
		t.Errorf("machine scope must be untouched, got %+v", k.Scope)
	}
	if got, _ := s.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "main"}); len(got) != 1 {
		t.Errorf("lifted entry must now reach main, got %+v", got)
	}
}

// Listing is what makes the decision reviewable before it is taken.
func TestBranchBoundKnowledgeLists(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "a", Body: "b",
		Scope: scope.Axes{Project: "github.com/x/y", Branch: "v0"}, Confidence: "trusted"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "b", Body: "b",
		Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted"})

	got, err := s.BranchBoundKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "a" {
		t.Fatalf("BranchBoundKnowledge = %+v, want only the branch-bound entry", got)
	}
}
