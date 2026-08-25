package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func seedSearchCorpus(t *testing.T, s *Store) {
	t.Helper()
	entries := []Knowledge{
		{Type: "decision", Title: "Ghost Tree ist ein Parallelbaum in der Form des Repos",
			Body: "Die Branch-Achse ist eine Hierarchie mit Vererbung, keine Etikettierung."},
		{Type: "decision", Title: "Auslöser sind der Hauptkanal, der Bootstrap ist der Rückfallweg",
			Body: "Was eine Harness auslösen kann, wird ausgelöst; der Rest bleibt im Bootstrap."},
		{Type: "pitfall", Title: "Binary-Update wirkt nicht überall sofort",
			Body: "Laufende Prozesse behalten ihren alten Inode."},
	}
	for _, k := range entries {
		k.Confidence = "trusted"
		if _, err := s.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
}

// The terms were joined with AND, so a query naming two entries found neither.
// Measured on 2026-08-24: "Ghost Tree Parallelbaum Bootstrap Auslöser
// Rückfallweg" returned nothing, while "Parallelbaum" alone returned the entry.
// An empty result is indistinguishable from "there is nothing", so an agent
// concludes the tree is empty and stops asking.
func TestSearchFindsEntriesThatMatchPartOfTheQuery(t *testing.T) {
	s := openTest(t)
	seedSearchCorpus(t, s)

	hits, err := s.SearchKnowledge("Ghost Tree Parallelbaum Bootstrap Auslöser Rückfallweg", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %d, want both entries the query names: %v", len(hits), knowledgeTitles(hits))
	}
	// Both named entries come back. Which of the two ranks first is bm25's
	// business and not asserted here: the query names both equally, so either
	// order is a correct answer.
	var found []string
	for _, k := range hits {
		found = append(found, k.Title)
	}
	joined := strings.Join(found, " | ")
	for _, want := range []string{"Parallelbaum", "Rückfallweg"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q missing from %v", want, found)
		}
	}
	// The entry the query does not name must not be dragged in.
	if strings.Contains(joined, "Binary-Update") {
		t.Errorf("unrelated entry matched: %v", found)
	}
}

// A single term must keep working exactly as before.
func TestSearchStillFindsASingleTerm(t *testing.T) {
	s := openTest(t)
	seedSearchCorpus(t, s)
	hits, _ := s.SearchKnowledge("Parallelbaum", scope.Axes{}, 10)
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want exactly the one entry", knowledgeTitles(hits))
	}
}

// Ordinary words carry no intent. Left in an OR query they match everything and
// turn the ranking into noise.
func TestSearchIgnoresWordsThatSayNothing(t *testing.T) {
	s := openTest(t)
	seedSearchCorpus(t, s)
	hits, err := s.SearchKnowledge("der die das ist ein", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("a query of only common words matched %v, want nothing", knowledgeTitles(hits))
	}
}

// A question with no content word is a request to see everything, the way
// `gh issue list` with no argument is.
func TestQueryWithoutContentWordsIsAListing(t *testing.T) {
	if !isListingQuery("was ist noch zu tun") {
		t.Error(`"was ist noch zu tun" must be treated as a listing`)
	}
	if !isListingQuery("") {
		t.Error("an empty query must be treated as a listing")
	}
	if isListingQuery("Parallelbaum") {
		t.Error("a query with a content word must not be a listing")
	}
}

// Die Stichprobe besteht aus Anfragen, die in den Prüfläufen zu #732 und
// #1432 tatsächlich gestellt wurden, ergänzt um ihre englischen Entsprechungen.
// Vierzehn von vierzehn müssen an der beabsichtigten Rolle landen; in der
// ursprünglichen Zwölferstichprobe erreichte die alte Grenze nur zehn von zwölf
// (83,3 %). Zwei Negativfälle sichern zusätzlich gegen Überklassifikation ab.
func TestSearchIntentMeasuredAgainstRealQueries(t *testing.T) {
	tests := []struct {
		query string
		want  SearchIntent
	}{
		{"zuletzt gearbeitet nicht fertig", SearchInterrupted},
		{"woran wurde hier zuletzt gearbeitet, ohne dass es zu Ende gebracht wurde?", SearchInterrupted},
		{"what work was left unfinished?", SearchInterrupted},
		{"where did we leave off?", SearchInterrupted},
		{"was ist noch zu tun", SearchInventory},
		{"what is left to do", SearchInventory},
		{"zeig mir die offenen Aufgaben", SearchInventory},
		{"list the open work", SearchInventory},
		{"Parallelbaum", SearchFullText},
		{"Warum behält ein laufender Prozess den alten Inode?", SearchFullText},
		{"Ghost Tree Parallelbaum Bootstrap Auslöser Rückfallweg", SearchFullText},
		{"ctx mcp per Shell-Pipe testen", SearchFullText},
		{"Warum wurde die Netzwerkverbindung unterbrochen?", SearchFullText},
		{"Wie wird ein fertiger Request abgeschlossen?", SearchFullText},
	}
	correct := 0
	for _, tt := range tests {
		if got := ClassifySearch(tt.query); got != tt.want {
			t.Errorf("ClassifySearch(%q) = %q, want %q (terms=%v)", tt.query, got, tt.want, searchTerms(tt.query))
		} else {
			correct++
		}
	}
	t.Logf("Trefferquote der Intent-Grenze: %d/%d = %.1f%%", correct, len(tests), 100*float64(correct)/float64(len(tests)))
}

func knowledgeTitles(ks []Knowledge) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Title
	}
	return out
}
