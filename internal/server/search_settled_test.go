package server

import (
	"net/url"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Agent search answers with settled knowledge. A staged entry and the gardener's
// note about an entry share its words, and without a filter they would stand
// in front of it; asked for by is:staged or author:gardener they come back.
func TestSearchLeavesOutStagedAndGardenerUnlessAsked(t *testing.T) {
	f := accessAPI(t, true)
	for _, k := range []store.Knowledge{
		{Type: "pitfall", Title: "flarnix braucht Neustart", Body: "flarnix hängt nach dem Update", Confidence: "trusted"},
		{Type: "pitfall", Title: "flarnix Vermutung", Body: "flarnix vielleicht kaputt", Confidence: "staged"},
		{Type: "note", Title: "[Gärtner] Veraltet? flarnix", Body: "Vorschlag des Gärtners zu flarnix", Confidence: "staged",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:1", Origin: "distilled"},
		{Type: "note", Title: "[Gärtner] bestätigt flarnix", Body: "bestätigte Notiz zu flarnix", Confidence: "trusted",
			Harness: store.GardenerHarness, SessionRef: store.GardenerRefPrefix + "check-knowledge:2", Origin: "distilled"},
	} {
		k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
		if _, err := f.st.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(q string) map[string]bool {
		t.Helper()
		v := url.Values{"q": {q}, "kind": {"knowledge"}, "project": {accProject}}
		res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
		out := map[string]bool{}
		for _, k := range res.Knowledge {
			out[k.Title] = true
		}
		return out
	}
	settled := "flarnix braucht Neustart"
	staged := "flarnix Vermutung"
	proposal := "[Gärtner] Veraltet? flarnix"
	confirmedNote := "[Gärtner] bestätigt flarnix"

	for _, q := range []string{"flarnix", "flarnix type:pitfall,note"} {
		got := titles(q)
		if !got[settled] || got[staged] || got[proposal] || got[confirmedNote] {
			t.Errorf("%q: want only the settled entry, got %v", q, got)
		}
	}
	if got := titles("flarnix is:staged"); !got[staged] || !got[proposal] || got[settled] {
		t.Errorf("is:staged: %v", got)
	}
	for _, q := range []string{"flarnix author:gardener", "flarnix harness:gardener"} {
		if got := titles(q); !got[proposal] || !got[confirmedNote] || got[staged] || got[settled] {
			t.Errorf("%q: %v", q, got)
		}
	}
	// Negated, the question is not asked: the default stays.
	if got := titles("flarnix -author:gardener"); !got[settled] || got[staged] || got[proposal] {
		t.Errorf("-author:gardener: %v", got)
	}
}

// One request for every area, as ctx search sends it: a failed area becomes a
// gap and the hits of the others still come back. Asked for that area alone,
// its failure is the answer.
func TestSearchAllAnswersWhenOneAreaFails(t *testing.T) {
	f := accessAPI(t, true)
	k := store.Knowledge{Type: "pitfall", Title: "zentrifol hängt", Body: "b", Confidence: "trusted"}
	k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
	if _, err := f.st.InsertKnowledge(k); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`DROP TABLE chunk_fts`); err != nil {
		t.Fatal(err)
	}
	v := url.Values{"q": {"zentrifol"}, "project": {accProject}}
	// The default search does not ask the sessions: their index failing costs nothing.
	res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 0 {
		t.Errorf("kind=all without sessions: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	// Another area failing is a gap in it, and the hits of the others still come back.
	if _, err := f.st.DB().Exec(`DROP TABLE search_documents_fts`); err != nil {
		t.Fatal(err)
	}
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "requests", Reason: store.GapFailed}) {
		t.Errorf("kind=all: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	v.Set("q", "zentrifol kind:knowledge,session")
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0].Area != "sessions" {
		t.Errorf("qualified: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	v.Set("q", "zentrifol")
	v.Set("kind", "sessions")
	f.expect(t, "robin", 500, "GET", "/api/search?"+v.Encode(), nil)
}
