package store

import (
	"context"
	"errors"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The count of a search is the number of requests the search would list, rights
// included, not the number of rows of the table: the member of the project counts
// what the project holds for words in it, a stranger counts nothing.
func TestCountRequestMatchesCountsWhatTheSearchFindsForTheViewer(t *testing.T) {
	w := newPainWorld(t)
	for _, title := range []string{"Needle one", "Needle two", "Haystack"} {
		if _, err := w.st.CreateRequest(requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: roleProject}, Person: "mia"},
			Criteria: []string{"it works"}, AuthorID: 3}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(who, query string) int {
		t.Helper()
		pa := w.pa[who]
		f := pa.RequestFilter(requestdomain.SearchFilter{Query: query})
		n, err := w.st.CountRequestMatches(f)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("mia", "Needle"); n != 2 {
		t.Errorf("member counts %d for Needle, want 2", n)
	}
	if n := count("mia", ""); n != 3 {
		t.Errorf("member counts %d without words, want 3", n)
	}
	if n := count("gus", "Needle"); n != 2 {
		t.Errorf("guest of the project counts %d, want 2 (he reads the issues)", n)
	}
	if n := count("nora", "Needle"); n != 0 {
		t.Errorf("a stranger counts %d, want 0", n)
	}
	// The count is the list: the same filter lists the same number.
	pa := w.pa["mia"]
	page, err := w.st.SearchRequests(pa.RequestFilter(requestdomain.SearchFilter{Query: "Needle", Limit: 25}))
	if err != nil || len(page.Results) != 2 {
		t.Errorf("the search lists %d, %v", len(page.Results), err)
	}
}

// The time budget of the live search reaches the count and the search of requests:
// with a context that has ended neither reads on. SearchRequestsWithContext binds
// the whole search, the count of the origins included, to the context of the call.
func TestRequestSearchAndCountEndWithTheirContext(t *testing.T) {
	w := newPainWorld(t)
	if _, err := w.st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: "Needle", Scope: scope.Axes{Project: roleProject}, Person: "mia"},
		Criteria: []string{"it works"}, AuthorID: 3}); err != nil {
		t.Fatal(err)
	}
	f := w.pa["mia"].RequestFilter(requestdomain.SearchFilter{Query: "Needle", Limit: 25})
	// With a live context the context call answers like the plain call.
	page, err := w.st.SearchRequestsWithContext(context.Background(), f, SearchAuto)
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("live context: %d hits, %v", len(page.Results), err)
	}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.st.SearchRequestsWithContext(ended, f, SearchAuto); !errors.Is(err, context.Canceled) {
		t.Errorf("search with an ended context: %v, want context canceled", err)
	}
	if _, err := w.st.WithContext(ended).CountRequestMatches(f); !errors.Is(err, context.Canceled) {
		t.Errorf("count with an ended context: %v, want context canceled", err)
	}
	if _, err := w.st.WithContext(ended).CountRequests(f); !errors.Is(err, context.Canceled) {
		t.Errorf("count of the list with an ended context: %v, want context canceled", err)
	}
	// The view for one call leaves the store as it was.
	if page, err := w.st.SearchRequestsWith(f, SearchAuto); err != nil || len(page.Results) != 1 {
		t.Errorf("the store itself after the view: %d hits, %v", len(page.Results), err)
	}
}

// A long page reads many hits in one query, where every further page of a search
// costs the whole query again. Without LongPage a page stops at 25 and leads on
// with a cursor; with it the page holds what was asked for, up to the maximum,
// and says whether there is more behind it.
func TestRequestSearchLongPageReadsManyHitsInOneQuery(t *testing.T) {
	w := newPainWorld(t)
	for i := 0; i < 30; i++ {
		if _, err := w.st.CreateRequest(requestdomain.CreateInput{
			Request:  requestdomain.Request{Type: "feature", Title: "Needle " + string(rune('a'+i%26)) + string(rune('a'+i/26)), Scope: scope.Axes{Project: roleProject}, Person: "mia"},
			Criteria: []string{"it works"}, AuthorID: 3}); err != nil {
			t.Fatal(err)
		}
	}
	pa := w.pa["mia"]
	search := func(limit int, long bool) requestdomain.SearchPage {
		t.Helper()
		page, err := w.st.SearchRequestsWithContext(context.Background(), pa.RequestFilter(requestdomain.SearchFilter{Query: "Needle", Limit: limit, LongPage: long}), SearchAuto)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := search(100, false); len(page.Results) != 25 || page.NextCursor == "" {
		t.Errorf("a plain page: %d hits, cursor %q, want 25 and a cursor", len(page.Results), page.NextCursor)
	}
	if page := search(100, true); len(page.Results) != 30 || page.NextCursor != "" {
		t.Errorf("a long page of 100: %d hits, cursor %q, want all 30 and no cursor", len(page.Results), page.NextCursor)
	}
	if page := search(28, true); len(page.Results) != 28 || page.NextCursor == "" {
		t.Errorf("a long page of 28: %d hits, cursor %q, want 28 and a cursor", len(page.Results), page.NextCursor)
	}
	if got := requestLimit(requestdomain.SearchFilter{Limit: 10000, LongPage: true}); got != RequestLongPageMax {
		t.Errorf("the largest long page is %d, want %d", got, RequestLongPageMax)
	}
}
