package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestSupersedeHidesOldEntriesButKeepsThemReachable(t *testing.T) {
	s := openTest(t)
	ax := scope.Axes{Project: "p"}
	a, b := relEntry(t, s, "p", "share created"), relEntry(t, s, "p", "share created again")
	n := relEntry(t, s, "p", "share removed")

	if err := s.SupersedeKnowledge(n, []int64{a, b}, "the share was removed", relActor); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		k, err := s.KnowledgeByID(id)
		if err != nil || k.Status != "superseded" || k.SupersededBy != n {
			t.Fatalf("old entry %d = %+v, %v", id, k, err)
		}
		hist, err := s.KnowledgeHistory(id)
		if err != nil || len(hist) != 1 || hist[0].Title == "" {
			t.Fatalf("history of %d = %+v, %v", id, hist, err)
		}
		rels, err := s.RelationsOf(id)
		if err != nil || len(rels) != 1 || rels[0].Kind != RelSupersedes || rels[0].FromID != n || rels[0].Reason != "the share was removed" || rels[0].State != RelActive {
			t.Fatalf("relations of %d = %+v, %v", id, rels, err)
		}
	}
	if k, _ := s.KnowledgeByID(n); k.Status != "active" {
		t.Errorf("the new entry must stay active: %+v", k)
	}

	hits, err := s.SearchKnowledgeForContext("share", ax, 10)
	if err != nil || len(hits) != 1 || hits[0].ID != n {
		t.Errorf("search = %v, %v; want only the new entry", titles(hits), err)
	}
	ctx, err := s.KnowledgeForContext(ax)
	if err != nil || len(ctx) != 1 || ctx[0].ID != n {
		t.Errorf("briefing = %v, %v; want only the new entry", titles(ctx), err)
	}
	listed, err := s.BrowseKnowledge("p", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range listed {
		if k.ID == a || k.ID == b {
			t.Errorf("the web list still shows #%d", k.ID)
		}
	}
}

func TestSupersedeRejectsWhatCannotBeSuperseded(t *testing.T) {
	s := openTest(t)
	a, n := relEntry(t, s, "p", "old"), relEntry(t, s, "p", "new")
	other := relEntry(t, s, "q", "foreign")
	cases := []struct {
		name   string
		newID  int64
		olds   []int64
		reason string
	}{
		{"no reason", n, []int64{a}, "  "},
		{"nothing to supersede", n, nil, "why"},
		{"itself", n, []int64{n}, "why"},
		{"other project", n, []int64{other}, "why"},
		{"unknown entry", n, []int64{9999}, "why"},
	}
	for _, c := range cases {
		if err := s.SupersedeKnowledge(c.newID, c.olds, c.reason, relActor); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if k, _ := s.KnowledgeByID(a); k.Status != "active" {
		t.Errorf("a refused call must change nothing: %+v", k)
	}
	if err := s.RetireKnowledge(a, "gone", "robin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeKnowledge(n, []int64{a}, "why", relActor); !errors.Is(err, ErrNotCurrent) {
		t.Errorf("retired entry: %v", err)
	}
}

func TestSupersedeIsAllOrNothing(t *testing.T) {
	s := openTest(t)
	a, b, n := relEntry(t, s, "p", "a"), relEntry(t, s, "p", "b"), relEntry(t, s, "p", "n")
	if err := s.RetireKnowledge(b, "gone", "robin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeKnowledge(n, []int64{a, b}, "why", relActor); err == nil {
		t.Fatal("expected failure on the retired entry")
	}
	if k, _ := s.KnowledgeByID(a); k.Status != "active" || k.SupersededBy != 0 {
		t.Errorf("a was changed although the call failed: %+v", k)
	}
	if rels, _ := s.RelationsOf(a); len(rels) != 0 {
		t.Errorf("relation left behind: %+v", rels)
	}
}

func TestSupersedeRepointsEarlierGenerations(t *testing.T) {
	s := openTest(t)
	a, b, c := relEntry(t, s, "p", "v1"), relEntry(t, s, "p", "v2"), relEntry(t, s, "p", "v3")
	if err := s.SupersedeKnowledge(b, []int64{a}, "v2", relActor); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeKnowledge(c, []int64{b}, "v3", relActor); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.KnowledgeByID(a); k.Status != "superseded" || k.SupersededBy != c {
		t.Errorf("v1 must point at the current head: %+v", k)
	}
	if err := s.SupersedeKnowledge(a, []int64{c}, "cycle", relActor); err == nil {
		t.Error("a superseded entry cannot supersede its successor")
	}
}

func TestRetireNeedsReasonAndHidesTheEntry(t *testing.T) {
	s := openTest(t)
	ax := scope.Axes{Project: "p"}
	a := relEntry(t, s, "p", "temporary share")
	if err := s.RetireKnowledge(a, " ", "robin"); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("no reason: %v", err)
	}
	if err := s.RetireKnowledge(a, "the share no longer exists", "robin"); err != nil {
		t.Fatal(err)
	}
	k, err := s.KnowledgeByID(a)
	if err != nil || k.Status != "deprecated" || k.LastModifiedBy != "robin" {
		t.Fatalf("entry = %+v, %v", k, err)
	}
	if hits, _ := s.SearchKnowledgeForContext("share", ax, 10); len(hits) != 0 {
		t.Errorf("search still finds it: %v", titles(hits))
	}
	if ctx, _ := s.KnowledgeForContext(ax); len(ctx) != 0 {
		t.Errorf("briefing still carries it: %v", titles(ctx))
	}
	if hist, _ := s.KnowledgeHistory(a); len(hist) != 1 {
		t.Errorf("history = %+v", hist)
	}
	rs, err := s.KnowledgeRetirements(a)
	if err != nil || len(rs) != 1 || rs[0].Reason != "the share no longer exists" || rs[0].RetiredBy != "robin" {
		t.Errorf("retirements = %+v, %v", rs, err)
	}
	if err := s.RetireKnowledge(a, "again", "robin"); !errors.Is(err, ErrNotCurrent) {
		t.Errorf("second retire: %v", err)
	}
}
