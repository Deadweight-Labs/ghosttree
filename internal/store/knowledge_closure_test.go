package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestKnowledgeClosureNamesTheReasonOfASupersededOrRetiredEntry(t *testing.T) {
	s := openTest(t)
	old, fresh, gone, live := relEntry(t, s, "p", "old"), relEntry(t, s, "p", "new"), relEntry(t, s, "p", "gone"), relEntry(t, s, "p", "live")
	if err := s.SupersedeKnowledge(fresh, []int64{old}, "the state changed", relActor); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireKnowledge(gone, "the thing was removed", "robin"); err != nil {
		t.Fatal(err)
	}
	closure := func(id int64) *KnowledgeClosure {
		t.Helper()
		k, err := s.KnowledgeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.KnowledgeClosure(k)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := closure(old); c == nil || c.Kind != "superseded" || c.Reason != "the state changed" || c.SupersededBy != fresh || c.By != "robin" {
		t.Errorf("superseded = %+v", c)
	} else if got := c.Summary(); !strings.HasPrefix(got, "superseded by #") || !strings.HasSuffix(got, ": the state changed") {
		t.Errorf("summary = %q", got)
	}
	if c := closure(gone); c == nil || c.Kind != "retired" || c.Reason != "the thing was removed" {
		t.Errorf("retired = %+v", c)
	}
	for _, id := range []int64{fresh, live} {
		if c := closure(id); c != nil {
			t.Errorf("#%d is in use but has a closure: %+v", id, c)
		}
	}
}

func TestSupersedeNeedsAReplacementThatIsDeliverable(t *testing.T) {
	s := openTest(t)
	old := relEntry(t, s, "p", "old")
	staged, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "staged", Body: "b", Scope: scope.Axes{Project: "p"}, Person: "robin", Confidence: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeKnowledge(staged, []int64{old}, "r", relActor); !errors.Is(err, ErrNotDeliverable) {
		t.Fatalf("staged replacement: %v", err)
	}
	if k, _ := s.KnowledgeByID(old); k.Status != "active" {
		t.Errorf("the old entry changed: %+v", k)
	}
}
