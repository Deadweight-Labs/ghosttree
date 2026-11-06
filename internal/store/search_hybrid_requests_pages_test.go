package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// pageIDs follows the cursors of a ranked search to its end and returns the ids of
// the requests on every page, the number of pages and whether the last page says
// that the rankings were cut.
func pageIDs(t *testing.T, st *Store, f requestdomain.SearchFilter) (ids []int64, pages int, truncated bool) {
	t.Helper()
	for range 400 {
		page, err := st.SearchRequestsWith(f, SearchAuto)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, h := range page.Results {
			ids = append(ids, h.Request.ID)
		}
		if page.Truncated && page.NextCursor != "" {
			t.Fatalf("page %d is cut but has a cursor", pages)
		}
		if page.NextCursor == "" {
			return ids, pages, page.Truncated
		}
		f.Cursor = page.NextCursor
	}
	t.Fatal("the cursors never end")
	return nil, 0, false
}

// The rankings are read to a depth that does not grow with the page, so the pages
// of a search with more than a hundred hits (the old depth of a first page) still
// add up to one fusion: nothing falls between two pages, nothing shows on two.
func TestRequestSearchPagesPastTheFirstHundredAddUpToOneFusion(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	for i := range 130 {
		addRequest(t, st, fmt.Sprintf("W%03d", i), fmt.Sprintf("q1 q2 f%d", i))
	}
	for i := range 20 {
		addRequest(t, st, fmt.Sprintf("S%02d", i), fmt.Sprintf("s1 s2 g%d", i))
	}
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})

	f := requestdomain.SearchFilter{Query: "q1 q2", Limit: 10}
	got, pages, truncated := pageIDs(t, st, f)
	if truncated {
		t.Fatal("nothing was cut: 150 requests are far below the depth")
	}
	if room := maxSemanticOnly(10); len(got) != 130+room || pages != (130+room+9)/10 {
		t.Fatalf("%d hits on %d pages, want %d hits (every word hit and %d neighbours)", len(got), pages, 130+room, room)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("request %d is on two pages", id)
		}
		seen[id] = true
	}
	// Exactly the fusion of the two whole rankings, in its order.
	qvec, err := c.Query(context.Background(), "q1 q2")
	if err != nil {
		t.Fatal(err)
	}
	fused, cut, err := st.fuseRequests(f, SearchAuto, qvec, st.embedShared().cfg.Load())
	if err != nil || cut {
		t.Fatalf("%v cut=%v", err, cut)
	}
	var want []int64
	for _, h := range fused {
		want = append(want, h.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the pages are not the fusion:\n got %v\nwant %v", got, want)
	}
	// Another page size with the same room for neighbours gives the same sequence.
	f.Limit = 20
	if again, _, _ := pageIDs(t, st, f); !slices.Equal(again, got) {
		t.Fatalf("pages of 20 are another sequence than pages of 10")
	}
}

// A ranking that is cut at its depth says so on the last page; one that is not,
// does not.
func TestRequestSearchSaysWhenItsRankingsWereCut(t *testing.T) {
	old := requestPool
	requestPool = 40
	t.Cleanup(func() { requestPool = old })
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	for i := range 60 {
		addRequest(t, st, fmt.Sprintf("W%02d", i), fmt.Sprintf("q1 q2 f%d", i))
	}
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})

	f := requestdomain.SearchFilter{Query: "q1 q2", Limit: 10}
	got, pages, truncated := pageIDs(t, st, f)
	if !truncated {
		t.Fatalf("60 requests match and the rankings hold %d: the last page must say that more match", requestPool)
	}
	if len(got) < requestPool || len(got) > 2*requestPool {
		t.Fatalf("%d hits on %d pages", len(got), pages)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("request %d is on two pages", id)
		}
		seen[id] = true
	}
	// A depth that holds all 60 makes the same search whole.
	requestPool = 60
	if _, _, truncated := pageIDs(t, st, f); truncated {
		t.Fatal("a ranking that holds all 60 is not cut")
	}
}

// A cursor that is an id continues the list by ids, whether or not the embedding
// is in use; only a cursor ~N continues a ranking. A page that was answered from
// the list (the embedding server failed) is therefore followed by the list, not by
// a ranking that starts over.
func TestRequestSearchCursorDecidesTheModeNotTheEmbedding(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	for i := range 30 {
		addRequest(t, st, fmt.Sprintf("W%02d", i), fmt.Sprintf("q1 q2 f%d", i))
	}
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})

	f := requestdomain.SearchFilter{Query: "q1 q2", Limit: 10}
	// Page 1 from the list, as it is answered while the embedding server is away.
	srv.Fail.Store(true)
	first, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.ParseInt(first.NextCursor, 10, 64); err != nil {
		t.Fatalf("the list pages by id: %q", first.NextCursor)
	}
	// The server is back. The id cursor still means the list: page 2 is the list's.
	srv.Fail.Store(false)
	f.Cursor = first.NextCursor
	want, err := st.SearchRequests(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("page 2 after an id cursor: %+v %v, want the list's %+v", got, err, want)
	}
	for _, h := range got.Results {
		if h.Match != "" || h.MatchReason != "full-text match" {
			t.Fatalf("a hit of the list carries %q %q", h.Match, h.MatchReason)
		}
	}
	// A cursor ~N continues a ranking.
	f.Cursor = HybridCursor(10)
	ranked, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil || len(ranked.Results) != 10 || ranked.NextCursor != HybridCursor(20) {
		t.Fatalf("%+v %v", ranked, err)
	}
	// No cursor starts a ranking while the server is up.
	f.Cursor = ""
	top, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil || top.NextCursor != HybridCursor(10) {
		t.Fatalf("%+v %v", top, err)
	}
}
