package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func fileOn(t *testing.T, s *Store, title, branch string) {
	t.Helper()
	ax := scope.Axes{Project: "p"}
	if branch != "" {
		ax.Branch = branch
	}
	if _, err := s.InsertKnowledge(Knowledge{Type: "note", Title: title, Body: "b", Scope: ax}); err != nil {
		t.Fatal(err)
	}
}

func titlesFor(t *testing.T, s *Store, ax scope.Axes) map[string]bool {
	t.Helper()
	ks, err := s.KnowledgeForContext(ax)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, k := range ks {
		out[k.Title] = true
	}
	return out
}

// A branch cut from develop carries develop's files; the ghost tree is supposed
// to grow the same way. Two levels, because one level can be faked by treating
// a single parent as a special case.
func TestABranchReadsWhatItWasCutFrom(t *testing.T) {
	s := openTest(t)
	fileOn(t, s, "projektweit", "")
	fileOn(t, s, "auf main", "main")
	fileOn(t, s, "auf develop", "develop")
	fileOn(t, s, "auf feat", "feat/x")

	got := titlesFor(t, s, scope.Axes{Project: "p", Branch: "feat/x", Lineage: []string{"main", "develop"}})
	for _, want := range []string{"projektweit", "auf main", "auf develop", "auf feat"} {
		if !got[want] {
			t.Errorf("%q is not readable from feat/x: %v", want, got)
		}
	}

	// One level up sees main but not what was written on its own descendant.
	got = titlesFor(t, s, scope.Axes{Project: "p", Branch: "develop", Lineage: []string{"main"}})
	if !got["auf main"] || !got["auf develop"] {
		t.Errorf("develop cannot read its own line: %v", got)
	}
	if got["auf feat"] {
		t.Errorf("develop reads what was written on a branch cut from it: %v", got)
	}
}

// Deliberate branch scope is the exception, and the exception has to hold: a
// migration in flight is not the business of the branch beside it.
func TestASiblingBranchStaysOut(t *testing.T) {
	s := openTest(t)
	fileOn(t, s, "auf feat-a", "feat/a")
	fileOn(t, s, "auf feat-b", "feat/b")

	got := titlesFor(t, s, scope.Axes{Project: "p", Branch: "feat/a", Lineage: []string{"main"}})
	if !got["auf feat-a"] {
		t.Errorf("a branch cannot read its own entry: %v", got)
	}
	if got["auf feat-b"] {
		t.Errorf("a sibling branch leaked in: %v", got)
	}
}

// Without a chain the session reads at project scope, which is what it did
// before lineage existed. Nothing fails, nothing widens.
func TestNoChainFallsBackToProjectScope(t *testing.T) {
	s := openTest(t)
	fileOn(t, s, "projektweit", "")
	fileOn(t, s, "auf develop", "develop")

	got := titlesFor(t, s, scope.Axes{Project: "p", Branch: "feat/x"})
	if !got["projektweit"] {
		t.Errorf("project knowledge is unreachable without a chain: %v", got)
	}
	if got["auf develop"] {
		t.Errorf("an unrelated branch was read without a chain saying so: %v", got)
	}

	// And with no branch at all.
	got = titlesFor(t, s, scope.Axes{Project: "p"})
	if !got["projektweit"] || got["auf develop"] {
		t.Errorf("branchless session read %v", got)
	}
}

// The lineage is read context, never an address: naming an ancestor must not
// change where an entry lands.
func TestLineageDoesNotChangeWhereAnEntryIsFiled(t *testing.T) {
	s := openTest(t)
	id, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "abgelegt", Body: "b",
		Scope: scope.Axes{Project: "p", Branch: "feat/x", Lineage: []string{"main", "develop"}}})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.KnowledgeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if k.Scope.Branch != "feat/x" || len(k.Scope.Lineage) != 0 {
		t.Errorf("stored placement = %+v, want feat/x with no lineage", k.Scope)
	}
	// A sibling must still not see it.
	if titlesFor(t, s, scope.Axes{Project: "p", Branch: "feat/y", Lineage: []string{"main"}})["abgelegt"] {
		t.Error("an entry filed on feat/x reached feat/y")
	}
}

func TestSearchFollowsTheSameChainAsDelivery(t *testing.T) {
	s := openTest(t)
	fileOn(t, s, "Vererbung im Suchindex", "develop")
	hits, err := s.SearchKnowledgeForContext("Vererbung",
		scope.Axes{Project: "p", Branch: "feat/x", Lineage: []string{"develop"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("search from a descendant found %d, want the ancestor's entry", len(hits))
	}
	hits, err = s.SearchKnowledgeForContext("Vererbung",
		scope.Axes{Project: "p", Branch: "feat/y", Lineage: []string{"main"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("search from a sibling found %+v", hits)
	}
}
