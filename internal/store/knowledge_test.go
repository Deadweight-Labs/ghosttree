package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestKnowledgeContextUnion(t *testing.T) {
	s := openTest(t)
	mk := func(title string, ax scope.Axes) {
		_, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Scope: ax})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("global", scope.Axes{})
	mk("machine-only", scope.Axes{Machine: "workstation-a"})
	mk("project-only", scope.Axes{Project: "github.com/x/y"})
	mk("proj-branch", scope.Axes{Project: "github.com/x/y", Branch: "feat"})
	mk("proj-machine", scope.Axes{Project: "github.com/x/y", Machine: "workstation-a"})
	mk("other-machine", scope.Axes{Machine: "workstation-b"})
	mk("other-branch", scope.Axes{Project: "github.com/x/y", Branch: "main"})

	got, err := s.KnowledgeForContext(scope.Axes{Project: "github.com/x/y", Branch: "feat", Machine: "workstation-a"})
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, k := range got {
		titles[k.Title] = true
	}
	for _, want := range []string{"global", "machine-only", "project-only", "proj-branch", "proj-machine"} {
		if !titles[want] {
			t.Errorf("missing %q in context result", want)
		}
	}
	if titles["other-machine"] || titles["other-branch"] {
		t.Errorf("got out-of-scope entries: %v", titles)
	}
}

func TestKnowledgeOrderingAndStatus(t *testing.T) {
	s := openTest(t)
	id1, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "obs", Body: "b"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "ver", Body: "b", Confidence: "verified"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "gone", Body: "b"})
	if err := s.UpdateKnowledge(3, map[string]string{"status": "deprecated"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.KnowledgeForContext(scope.Axes{})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (deprecated filtered)", len(got))
	}
	if got[0].Title != "ver" {
		t.Errorf("verified must sort first, got %q", got[0].Title)
	}
	_ = id1
}

func TestSearchKnowledge(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "ufw drops LAN", Body: "ssh only via private network", Scope: scope.Axes{Machine: "workstation-b"}})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "unrelated", Body: "nothing"})
	got, err := s.SearchKnowledge("private network", scope.Axes{}, 10)
	if err != nil || len(got) != 1 || got[0].Scope.Machine != "workstation-b" {
		t.Fatalf("got %v err %v", got, err)
	}
	got, _ = s.SearchKnowledge("private network", scope.Axes{Machine: "workstation-a"}, 10)
	if len(got) != 0 {
		t.Errorf("machine filter must exclude workstation-b entry")
	}
}
