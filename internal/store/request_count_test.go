package store

import (
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
