package store

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestMinimumSimilarityKeepsFarEntriesOutOfTheEmbeddingRanking(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	addK(t, st, "wagen", "wagen kaputt", scope.Axes{}, "", "")
	addK(t, st, "boat", "segel mast", scope.Axes{}, "", "")
	embedAll(t, st, c)

	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	if got, _ := st.SearchKnowledge("auto defekt", scope.Axes{}, 10); len(got) != 3 {
		t.Fatalf("without a minimum every entry is a neighbour: %v", searchTitles(got))
	}
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, MinSimilarity: 0.8})
	got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"car", "wagen"}; !slices.Equal(searchTitles(got), want) {
		t.Fatalf("with a minimum only the near entry joins the word hit: %v, want %v", searchTitles(got), want)
	}
	if got[0].Match != "" || got[1].Match != MatchSemantic {
		t.Fatalf("markers %q %q", got[0].Match, got[1].Match)
	}
}

// Vectors of another length than the query vector cannot be compared. The
// search leaves them out and says so, once a minute, instead of silently never
// finding those entries again.
func TestVectorsOfAnotherDimensionAreLeftOutAndReported(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	odd := addK(t, st, "odd", "auto defekt", scope.Axes{}, "", "")
	embedAll(t, st, c)
	if err := st.UpsertKnowledgeEmbedding(KnowledgeEmbedding{KnowledgeID: odd, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
		TextHash: "x", Dim: 3, Scale: 0.1, Vec: []byte{1, 2, 3}, ReadAt: "2026-10-20T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var logs lineCounter
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	for range 3 {
		got, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: SearchVectorOnly, Limit: 10})
		if err != nil || !slices.Equal(searchTitles(got), []string{"car"}) {
			t.Fatalf("%v %v", searchTitles(got), err)
		}
	}
	if n := logs.count("embed_vector_dimension"); n != 1 {
		t.Fatalf("%d log lines, want 1:\n%s", n, logs.String())
	}
}

// The vectors are an addition. Whatever is wrong with them, the caller gets what
// the full-text search answers; only the measurement asks to hear about it.
func TestAFailingVectorRankingFallsBackToFullText(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	addK(t, st, "car", "auto defekt ersatzteil", scope.Axes{}, "", "")
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})
	if _, err := st.db.Exec(`DROP TABLE knowledge_embeddings`); err != nil {
		t.Fatal(err)
	}
	got, err := st.SearchKnowledge("auto defekt", scope.Axes{}, 10)
	if err != nil || !slices.Equal(searchTitles(got), []string{"car"}) {
		t.Fatalf("%v %v", searchTitles(got), err)
	}
	if _, err := st.SearchKnowledgeWith("auto defekt", KnowledgeSearch{Mode: SearchHybridStrict}); err == nil {
		t.Fatal("the strict mode must report the broken vectors")
	}
	if _, fallbacks := st.EmbedSearchStats(); fallbacks != 1 {
		t.Fatalf("fallbacks %d", fallbacks)
	}
}
