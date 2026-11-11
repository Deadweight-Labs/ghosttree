package server

import (
	"net/url"
	"testing"
	"time"

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

// An area that runs out of its time budget is named as missing and the others
// are answered, whichever it is: the budget is not the sessions' alone. The areas
// run one after the other, so the search as a whole ends at its own limit too.
func TestSearchAreaOutOfTimeIsAGapAndTheOthersAnswer(t *testing.T) {
	f := accessAPI(t, true)
	k := store.Knowledge{Type: "pitfall", Title: "zentrifol hängt", Body: "b", Confidence: "trusted"}
	k.Scope, k.Person = scope.Axes{Project: accProject}, "mia"
	if _, err := f.st.InsertKnowledge(k); err != nil {
		t.Fatal(err)
	}
	old := areaBudgetFor
	t.Cleanup(func() { areaBudgetFor = old })

	for _, slow := range []string{"sessions", "requests"} {
		areaBudgetFor = func(area string) time.Duration {
			if area == slow {
				return -time.Second
			}
			return areaBudget
		}
		// Several kinds named in the line: the qualified path.
		v := url.Values{"q": {"zentrifol kind:knowledge,requests,session"}, "project": {accProject}}
		res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
		if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: slow, Reason: store.GapTimeout}) {
			t.Errorf("qualified, %s out of time: %+v / %+v", slow, res.Knowledge, res.Unavailable)
		}
	}

	// The default search of all kinds: knowledge answers when the requests run out of time.
	areaBudgetFor = func(area string) time.Duration {
		if area == "requests" {
			return -time.Second
		}
		return areaBudget
	}
	v := url.Values{"q": {"zentrifol"}, "project": {accProject}}
	res := decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 1 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "requests", Reason: store.GapTimeout}) {
		t.Errorf("all, requests out of time: %+v / %+v", res.Knowledge, res.Unavailable)
	}
	// Knowledge out of time: the requests are still answered.
	areaBudgetFor = func(area string) time.Duration {
		if area == "knowledge" {
			return -time.Second
		}
		return areaBudget
	}
	res = decode[searchResult](t, f.expect(t, "robin", 200, "GET", "/api/search?"+v.Encode(), nil))
	if len(res.Knowledge) != 0 || len(res.Unavailable) != 1 || res.Unavailable[0] != (store.SearchGap{Area: "knowledge", Reason: store.GapTimeout}) {
		t.Errorf("all, knowledge out of time: %+v / %+v", res.Knowledge, res.Unavailable)
	}
}
