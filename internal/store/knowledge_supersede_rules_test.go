package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func countEntries(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInsertSupersedingIsOneTransaction(t *testing.T) {
	s := openTest(t)
	old := relEntry(t, s, "p", "old state")
	base := countEntries(t, s)
	next := func(k Knowledge) Knowledge {
		k.Type, k.Title, k.Body, k.Scope = "note", "new state", "b", scope.Axes{Project: "p"}
		return k
	}

	// Effective confidence decides, not the field that was sent.
	for _, k := range []Knowledge{{Origin: "distilled"}, {Confidence: "staged"}, {Confidence: "quarantined"}} {
		if _, err := s.InsertKnowledgeSuperseding(next(k), []int64{old}, "r", relActor); !errors.Is(err, ErrNotDeliverable) {
			t.Errorf("%+v: %v", k, err)
		}
	}
	// A missing old entry rolls the insert back.
	if _, err := s.InsertKnowledgeSuperseding(next(Knowledge{}), []int64{old, 9999}, "r", relActor); err == nil {
		t.Error("unknown old entry accepted")
	}
	if countEntries(t, s) != base {
		t.Fatal("a failed call left an entry behind")
	}
	if k, _ := s.KnowledgeByID(old); k.Status != "active" {
		t.Fatalf("old entry changed: %+v", k)
	}

	// Status and superseded_by from the caller are ignored.
	id, err := s.InsertKnowledgeSuperseding(next(Knowledge{Status: "stale", SupersededBy: 77}), []int64{old}, "r", relActor)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := s.KnowledgeByID(id); k.Status != "active" || k.SupersededBy != 0 {
		t.Errorf("new entry = %+v", k)
	}
	if k, _ := s.KnowledgeByID(old); k.Status != "superseded" || k.SupersededBy != id {
		t.Errorf("old entry = %+v", k)
	}
	// The second writer for the same old entry loses and leaves nothing.
	base = countEntries(t, s)
	if _, err := s.InsertKnowledgeSuperseding(next(Knowledge{}), []int64{old}, "r", relActor); !errors.Is(err, ErrNotCurrent) {
		t.Errorf("already superseded: %v", err)
	}
	if countEntries(t, s) != base {
		t.Error("the loser left an entry behind")
	}
}

func TestSupersedeNeedsExactlyTheSameScope(t *testing.T) {
	s := openTest(t)
	add := func(title string, ax scope.Axes) int64 {
		id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Scope: ax})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	project := add("project wide", scope.Axes{Project: "p"})
	branch := add("branch only", scope.Axes{Project: "p", Branch: "feat"})
	machine := add("machine", scope.Axes{Machine: "box"})
	global := add("global", scope.Axes{})
	for _, c := range []struct {
		name         string
		newID, oldID int64
	}{
		{"branch over project", branch, project},
		{"project over branch", project, branch},
		{"global over machine", global, machine},
		{"machine over global", machine, global},
	} {
		if err := s.SupersedeKnowledge(c.newID, []int64{c.oldID}, "r", relActor); !errors.Is(err, ErrScopeMismatch) {
			t.Errorf("%s: %v", c.name, err)
		}
		if k, _ := s.KnowledgeByID(c.oldID); k.Status != "active" {
			t.Errorf("%s hid the old entry", c.name)
		}
	}
	same := add("project again", scope.Axes{Project: "p"})
	if err := s.SupersedeKnowledge(same, []int64{project}, "r", relActor); err != nil {
		t.Errorf("same scope: %v", err)
	}
}

func TestRetireRefusesWhatOthersWereSupersededBy(t *testing.T) {
	s := openTest(t)
	a, b := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b")
	if err := s.SupersedeKnowledge(b, []int64{a}, "r", relActor); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireKnowledge(b, "gone", "robin"); !errors.Is(err, ErrHasPredecessors) {
		t.Fatalf("retire head: %v", err)
	}
	if k, _ := s.KnowledgeByID(b); k.Status != "active" {
		t.Errorf("head changed: %+v", k)
	}
	// Superseding the head again moves the chain on; its new head is refused too.
	c := relEntry(t, s, "p", "c")
	if err := s.SupersedeKnowledge(c, []int64{b}, "r", relActor); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireKnowledge(c, "gone", "robin"); !errors.Is(err, ErrHasPredecessors) {
		t.Errorf("the new head still has predecessors: %v", err)
	}
}
