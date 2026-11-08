package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Content a stranger put into an unclaimed project stays readable for the
// instance admin, but it never reaches the context of the admin's agents.
func TestInstanceAdminGetsNoDeliveryFromUnclaimedProjects(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetAccessMode(AccessMode{Enforce: true})
	for i, name := range []string{"robin", "anna"} {
		if _, err := st.AddAccount(name, "", i == 0); err != nil {
			t.Fatal(err)
		}
	}
	const project = "example.test/free/room"
	admin := func() *ProjectAccess { return st.Access(Principal{ID: "person:1", Label: "robin"}) }
	anna := func() *ProjectAccess { return st.Access(Principal{ID: "person:2", Label: "anna"}) }
	if !admin().IsAdmin() {
		t.Fatal("robin must be the instance admin")
	}
	k := Knowledge{Type: "instruction", Title: "t", Body: "b", Person: "anna", AuthorID: 2, Scope: scope.Axes{Project: project}}
	g := GhostFile{Project: project, Path: "a.go", Kind: "file", Person: "anna"}
	f := feature.Feature{Project: project, Person: "anna", VersionPerson: "anna"}
	if !anna().CanDeliverKnowledge(k) || !anna().CanDeliverGhost(g) || !anna().CanDeliverFeature(f) {
		t.Error("the author no longer gets her own entries in an unclaimed project")
	}
	if admin().CanDeliverKnowledge(k) {
		t.Error("knowledge of a stranger is delivered to the admin's agents")
	}
	if admin().CanDeliverGhost(g) {
		t.Error("a ghost description of a stranger is delivered to the admin's agents")
	}
	if admin().CanDeliverFeature(f) {
		t.Error("a feature of a stranger is delivered to the admin's agents")
	}
	own := Knowledge{Type: "instruction", Title: "t", Body: "b", Person: "robin", AuthorID: 1, Scope: scope.Axes{Project: project}}
	if !admin().CanDeliverKnowledge(own) {
		t.Error("the admin lost delivery of his own entry")
	}
}

func TestKnowledgeViewHidesTheMachineFromGuestsOnly(t *testing.T) {
	w := newPainWorld(t)
	k := Knowledge{Type: "instruction", Title: "t", Body: "b", Scope: scope.Axes{Project: roleProject, Machine: "some-box"}}
	for who, want := range map[string]string{"gus": "", "rex": "some-box", "mia": "some-box"} {
		if got := w.pa[who].KnowledgeView(k).Scope.Machine; got != want {
			t.Errorf("%s sees machine %q, want %q", who, got, want)
		}
	}
}
