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

func TestQuarantinedIsInvisibleUntilApproved(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "note", Title: "quarantined finding about private network", Body: "b",
		Origin: "distilled", Confidence: "quarantined"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "staged finding about private network", Body: "b",
		Origin: "distilled", Confidence: "staged"})

	ctx, err := s.KnowledgeForContext(scope.Axes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx) != 1 || ctx[0].Confidence != "staged" {
		t.Errorf("context must show staged but hide quarantined, got %+v", ctx)
	}
	hits, _ := s.SearchKnowledge("private network", scope.Axes{}, 10)
	if len(hits) != 1 {
		t.Errorf("search returned %d hits, want 1 (quarantined excluded)", len(hits))
	}
}

func TestContextOrdersByTrust(t *testing.T) {
	s := openTest(t)
	s.InsertKnowledge(Knowledge{Type: "note", Title: "c-staged", Body: "b", Origin: "distilled", Confidence: "staged"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "b-trusted", Body: "b", Confidence: "trusted"})
	s.InsertKnowledge(Knowledge{Type: "note", Title: "a-verified", Body: "b", Confidence: "verified"})
	got, _ := s.KnowledgeForContext(scope.Axes{})
	var order []string
	for _, k := range got {
		order = append(order, k.Title)
	}
	want := []string{"a-verified", "b-trusted", "c-staged"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestInsertDefaultsByOrigin(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "from an agent", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.KnowledgeByID(id)
	if k.Origin != "agent" || k.Confidence != "trusted" {
		t.Errorf("agent default = %q/%q, want agent/trusted", k.Origin, k.Confidence)
	}

	id, err = s.InsertKnowledge(Knowledge{Type: "note", Title: "from a distiller", Body: "b", Origin: "distilled"})
	if err != nil {
		t.Fatal(err)
	}
	k, _ = s.KnowledgeByID(id)
	if k.Confidence != "quarantined" {
		t.Errorf("distilled default = %q, want quarantined", k.Confidence)
	}
	if k.SupersededBy != 0 {
		t.Errorf("SupersededBy = %d, want 0", k.SupersededBy)
	}
}

func TestInsertRejectsUnknownConfidence(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b", Confidence: "observation"}); err == nil {
		t.Error("the old 'observation' value must be rejected by the CHECK")
	}
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
