package store

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// A search that finds two entries by their words must not come back padded to the
// limit with the nearest neighbours: entries below the minimum similarity stay out,
// and of the rest only a few are shown, marked as found by meaning.
func TestHybridSearchIsNotPaddedToTheLimit(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	addK(t, st, "hit one", "q1 q2 f1", scope.Axes{}, "", "")
	addK(t, st, "hit two", "q1 q2 f2", scope.Axes{}, "", "")
	for i := range 30 {
		// The concepts of the query, none of its words: near by meaning.
		addK(t, st, fmt.Sprintf("near%02d", i), "s1 s2 s3", scope.Axes{}, "", "")
	}
	for i := range 10 {
		addK(t, st, fmt.Sprintf("far%02d", i), fmt.Sprintf("x%d y%d z%d", i, i, i), scope.Axes{}, "", "")
	}
	embedAll(t, st, c)

	// The numbers behind the default minimum: near entries score about 0.71 for this
	// query in the fake's vector space, far ones nothing.
	q, err := c.Query(t.Context(), "q1 q2")
	if err != nil {
		t.Fatal(err)
	}
	score := func(title string) float32 {
		var vec []byte
		var scale float64
		if err := st.db.QueryRow(`SELECT e.vec, e.scale FROM knowledge_embeddings e JOIN knowledge k ON k.id=e.knowledge_id WHERE k.title=?`, title).Scan(&vec, &scale); err != nil {
			t.Fatal(err)
		}
		return embed.Cosine(q, vec, float32(scale))
	}
	if near, far := score("near00"), score("far00"); near < embed.DefaultMinSimilarity+0.05 || far > embed.DefaultMinSimilarity-0.3 {
		t.Fatalf("the fixture does not separate near (%.2f) from far (%.2f) around %.2f", near, far, embed.DefaultMinSimilarity)
	}

	st.SetEmbedder(EmbedSearchConfig{Embedder: c, MinSimilarity: embed.DefaultMinSimilarity})
	got, err := st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	semantic := 0
	for i, k := range got {
		if k.Match == MatchSemantic {
			semantic++
		}
		if len(k.Title) >= 3 && k.Title[:3] == "far" {
			t.Fatalf("a far entry reached the result: %v", searchTitles(got))
		}
		if i < 2 && k.Match != "" {
			t.Fatalf("the two word hits come first: %v", searchTitles(got))
		}
	}
	if len(got) != 2+maxSemanticHits || semantic != maxSemanticHits {
		t.Fatalf("%d hits, %d of them semantic, want 2 and %d: %v", len(got), semantic, maxSemanticHits, searchTitles(got))
	}

	// Half of a short result at most.
	got, err = st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	semantic = 0
	for _, k := range got {
		if k.Match == MatchSemantic {
			semantic++
		}
	}
	if len(got) != 4 || semantic != 2 {
		t.Fatalf("limit 4: %d hits, %d semantic: %v", len(got), semantic, searchTitles(got))
	}
	if got, _ = st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Limit: 2}); !slices.Equal(searchTitles(got), []string{"hit one", "hit two"}) && !slices.Equal(searchTitles(got), []string{"hit two", "hit one"}) {
		t.Fatalf("limit 2: %v", searchTitles(got))
	}

	// The ranking alone, which the measurement uses, is neither floored by the cap
	// nor padded: it lists what is near, up to the limit.
	vec, err := st.SearchKnowledgeWith("q1 q2", KnowledgeSearch{Limit: 20, Mode: SearchVectorOnly})
	if err != nil || len(vec) != 20 {
		t.Fatalf("vector ranking: %d %v", len(vec), err)
	}
}

// With a project filter and the prefilter in the query, a guest of one project
// who can see three entries does not make a search check the stock of every other
// project; without a prefilter the row rule runs at most keepScanCap times.
func TestAGuestWithLittleToSeeDoesNotMakeASearchCheckTheWholeStock(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	for i := range 3 {
		addK(t, st, fmt.Sprintf("V%d", i), "q1 f1 f2", scope.Axes{Project: roleProject}, "trusted", "mia")
	}
	const foreign = 1200
	for i := range foreign {
		addK(t, st, fmt.Sprintf("X%d", i), "s1 s2 s3 s4", scope.Axes{Project: otherProject}, "trusted", "robin")
	}
	embedAll(t, st, c)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c})

	pa := st.Access(Principal{ID: "person:5", Label: "gus"})
	search := func(prefilter bool) (titles []string, calls int) {
		keep := func(k Knowledge) bool { calls++; return keepRule(pa)(k) }
		o := KnowledgeSearch{Limit: 20, Viewer: pa.Viewer(), Keep: keep, Mode: SearchVectorOnly}
		if prefilter {
			o.Prefilter = pa.KnowledgePrefilter()
		}
		got, err := st.SearchKnowledgeWith("q1 q2 q3 q4", o)
		if err != nil {
			t.Fatal(err)
		}
		return searchTitles(got), calls
	}
	titles, calls := search(true)
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"V0", "V1", "V2"}) || calls > 3 {
		t.Fatalf("with the prefilter: %v after %d checks of the row rule", titles, calls)
	}
	if _, calls = search(false); calls > keepScanCap {
		t.Fatalf("without a prefilter the row rule ran %d times, more than the %d the search reads", calls, keepScanCap)
	}
}
