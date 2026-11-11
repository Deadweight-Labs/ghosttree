package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// PAIN-68: context_search for an agent does not fill its hit list with the
// gardener's questions about other entries or with unconfirmed guesses. It
// leaves out what the active delivery leaves out (staged, quarantined, harness
// gardener) and gives it back only when the line asks: is:staged, author:gardener.
func TestContextSearchLeavesOutStagedAndGardenerUnlessAsked(t *testing.T) {
	c, st := newTestClient(t)
	const project = "github.com/x/y"
	for _, k := range []store.Knowledge{
		{Type: "decision", Title: "Positionierung quellwerk", Body: "quellwerk ist ein Wissensspeicher für Agenten", Confidence: "trusted"},
		{Type: "note", Title: "Positionierung quellwerk Vermutung", Body: "quellwerk vielleicht anders", Confidence: "staged"},
		{Type: "note", Title: "[Gärtner] Beschreibung? Positionierung quellwerk", Body: "Rückfrage des Gärtners zu quellwerk", Confidence: "staged",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:1", Origin: "distilled"},
		{Type: "note", Title: "[Gärtner] Erfüllt? quellwerk", Body: "bestätigte Rückfrage zu quellwerk", Confidence: "trusted",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:2", Origin: "distilled"},
		{Type: "pitfall", Title: "quellwerk quarantäne", Body: "quellwerk Eintrag unter Quarantäne", Confidence: "quarantined"},
	} {
		k.Scope, k.Person = scope.Axes{Project: project}, "alice"
		if _, err := st.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: project}}
	titles := func(in SearchInput) string {
		t.Helper()
		res, _, err := s.handleSearch(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		return text(t, res)
	}
	const settled = "Positionierung quellwerk"
	notWanted := []string{"Vermutung", "[Gärtner] Beschreibung?", "[Gärtner] Erfüllt?", "quarantäne"}

	// No qualifier, with and without kind: only the settled entry.
	for _, in := range []SearchInput{
		{Query: "quellwerk"},
		{Query: "quellwerk", Kind: "knowledge"},
		{Query: "quellwerk type:decision,note,pitfall"},
	} {
		got := titles(in)
		if !strings.Contains(got, settled) {
			t.Errorf("%+v: the settled entry is missing:\n%s", in, got)
		}
		for _, bad := range notWanted {
			if strings.Contains(got, bad) {
				t.Errorf("%+v: %q is in the hit list:\n%s", in, bad, got)
			}
		}
	}

	// Asked for, they come back and the settled entry steps back.
	got := titles(SearchInput{Query: "quellwerk is:staged", Kind: "knowledge"})
	for _, want := range []string{"Vermutung", "[Gärtner] Beschreibung?"} {
		if !strings.Contains(got, want) {
			t.Errorf("is:staged lacks %q:\n%s", want, got)
		}
	}
	got = titles(SearchInput{Query: "quellwerk author:gardener", Kind: "knowledge"})
	for _, want := range []string{"[Gärtner] Beschreibung?", "[Gärtner] Erfüllt?"} {
		if !strings.Contains(got, want) {
			t.Errorf("author:gardener lacks %q:\n%s", want, got)
		}
	}
}
