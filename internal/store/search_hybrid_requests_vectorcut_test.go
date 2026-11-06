package store

import (
	"fmt"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// A vector ranking that is cut at its depth loses no hit of the fused search: the
// requests that match the words are in the text ranking, and of the neighbours only
// the best few join the fusion anyway. The fused search must not report "truncated"
// for it, or every ranked search over a big ledger would, and a caller would be
// told to narrow a query that lost nothing. The ranking by meaning alone is cut by
// its own depth, and says so.
func TestRequestSearchDoesNotSayTruncatedWhenOnlyTheVectorRankingWasCut(t *testing.T) {
	old := requestPool
	requestPool = 40
	t.Cleanup(func() { requestPool = old })
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	// Ten requests that match the words of the query and sixty that match its
	// meaning only: the vector ranking holds 70 candidates, the depth is 40.
	for i := range 10 {
		addRequest(t, st, fmt.Sprintf("W%02d", i), fmt.Sprintf("q1 q2 f%d", i))
	}
	for i := range 60 {
		addRequest(t, st, fmt.Sprintf("S%02d", i), fmt.Sprintf("s1 s2 g%d", i))
	}
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})

	f := requestdomain.SearchFilter{Query: "q1 q2", Limit: 10}
	got, _, truncated := pageIDs(t, st, f)
	if truncated {
		t.Fatal("only the vector ranking was cut, and no hit was lost: the search must not say truncated")
	}
	words := 0
	for _, id := range got {
		if id <= 10 {
			words++
		}
	}
	if words != 10 {
		t.Fatalf("all ten requests that match the words are in the answer, found %d of %d hits", words, len(got))
	}

	// The ranking by meaning alone is cut by its own depth: 70 candidates, 40 read.
	var vecHits int
	var vecTruncated bool
	for range 20 {
		page, err := st.SearchRequestsWith(f, SearchVectorOnly)
		if err != nil {
			t.Fatal(err)
		}
		vecHits += len(page.Results)
		if page.NextCursor == "" {
			vecTruncated = page.Truncated
			break
		}
		f.Cursor = page.NextCursor
	}
	if vecHits != requestPool || !vecTruncated {
		t.Fatalf("meaning alone: %d hits (the depth is %d), truncated=%v", vecHits, requestPool, vecTruncated)
	}

	// With a depth that holds all of them, neither is cut.
	requestPool = 100
	f.Cursor = ""
	if _, _, truncated := pageIDs(t, st, f); truncated {
		t.Fatal("whole rankings are not truncated")
	}
}
