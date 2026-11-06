package store

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Ready is how a search learns that its query vector would not be comparable
// with the stored ones. While it says no, the query is not even sent, the answer
// is the full-text answer, and the reason is logged like any other fallback.
func TestSearchFallsBackToFullTextWhileTheEmbedderIsNotReady(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	addK(t, st, "wagen", "wagen kaputt", scope.Axes{}, "", "")
	embedAll(t, st, c)
	sent := srv.Calls()

	var notReady atomic.Bool
	notReady.Store(true)
	var logs lineCounter
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Ready: func() error {
			if notReady.Load() {
				return errors.New("the query server serves another model")
			}
			return nil
		}})

	got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatalf("the caller must not see why the vectors are not used: %v", err)
	}
	if want := []string{"car"}; !slices.Equal(searchTitles(got), want) {
		t.Fatalf("answer %v, want the full-text answer %v", searchTitles(got), want)
	}
	if srv.Calls() != sent {
		t.Fatal("a query vector that cannot be compared is not worth a request")
	}
	if hybrid, fallbacks := st.EmbedSearchStats(); hybrid != 0 || fallbacks != 1 {
		t.Fatalf("stats %d %d", hybrid, fallbacks)
	}
	if !strings.Contains(logs.String(), "embed_search_fallback") || !strings.Contains(logs.String(), "serves another model") {
		t.Fatalf("the reason is logged:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "auto defekt") {
		t.Fatal("the log must not hold the query")
	}

	// The strict modes of the measurement are not answered by the fallback.
	for _, mode := range []SearchMode{SearchHybridStrict, SearchVectorOnly} {
		if _, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: mode}); err == nil || !strings.Contains(err.Error(), "another model") {
			t.Fatalf("mode %d must report it: %v", mode, err)
		}
	}
	// Full text alone never asks.
	if ks, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: SearchFullTextOnly}); err != nil || len(ks) != 1 {
		t.Fatalf("%v %v", ks, err)
	}

	// Once it says yes, the vectors are used again.
	notReady.Store(false)
	got, err = st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"car", "wagen"}; !slices.Equal(searchTitles(got), want) {
		t.Fatalf("answer %v, want the hybrid answer %v", searchTitles(got), want)
	}
	if srv.Calls() != sent+1 {
		t.Fatalf("one query was embedded, %d requests more than before", srv.Calls()-sent)
	}
	if hybrid, _ := st.EmbedSearchStats(); hybrid != 1 {
		t.Fatalf("hybrid searches: %d", hybrid)
	}
}
