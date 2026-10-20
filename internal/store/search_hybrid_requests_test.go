package store

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func addRequest(t *testing.T, st *Store, title, description string, criteria ...string) int64 {
	t.Helper()
	d, err := st.CreateRequest(requestdomain.CreateInput{
		Request:  requestdomain.Request{Type: "feature", Title: title, Description: description, Scope: scope.Axes{Project: roleProject}, Person: "mia"},
		Criteria: criteria,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.Request.ID
}

// requestPages follows the cursors of a query to the end and returns the titles of
// every page.
func requestPages(t *testing.T, st *Store, f requestdomain.SearchFilter, mode SearchMode) (pages [][]string, cursors []string) {
	t.Helper()
	for range 50 {
		page, err := st.SearchRequestsWith(f, mode)
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, h := range page.Results {
			titles = append(titles, h.Request.Title)
		}
		pages = append(pages, titles)
		cursors = append(cursors, page.NextCursor)
		if page.NextCursor == "" {
			return pages, cursors
		}
		f.Cursor = page.NextCursor
	}
	t.Fatal("the cursors never end")
	return nil, nil
}

// A ranked search has no id to resume after; its pages are positions in one
// fusion, and together they are that fusion.
func TestRequestSearchPagesOfARankedSearchAddUp(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	for i := range 30 {
		addRequest(t, st, fmt.Sprintf("W%02d", i), fmt.Sprintf("q1 q2 f%d", i))
	}
	for i := range 12 {
		addRequest(t, st, fmt.Sprintf("S%02d", i), fmt.Sprintf("s1 s2 g%d", i))
	}
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})

	f := requestdomain.SearchFilter{Query: "q1 q2", Scope: scope.Axes{Project: roleProject}, Limit: 7}
	pages, cursors := requestPages(t, st, f, SearchAuto)
	var all []string
	for i, p := range pages {
		if last := i == len(pages)-1; !last && len(p) != 7 {
			t.Fatalf("page %d has %d hits: %v", i, len(p), p)
		}
		all = append(all, p...)
	}
	for i, cur := range cursors[:len(cursors)-1] {
		if cur != HybridCursor(7*(i+1)) {
			t.Fatalf("cursor %d is %q", i, cur)
		}
	}
	seen := map[string]bool{}
	semantic := 0
	for _, title := range all {
		if seen[title] {
			t.Fatalf("%s is on two pages: %v", title, pages)
		}
		seen[title] = true
		if strings.HasPrefix(title, "S") {
			semantic++
		}
	}
	// Of 42 requests, 30 match by words; the rest are neighbours, of which a result
	// of this page size shows four in all, not four per page.
	if room := maxSemanticOnly(7); len(all) != 30+room || semantic != room {
		t.Fatalf("%d hits, %d of them found by meaning alone; want %d and %d", len(all), semantic, 30+room, room)
	}
	// The pages are one fusion, cut up: a page as long as the whole result starts
	// the same way.
	f.Limit = 25
	long, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range long.Results {
		if h.Request.Title != all[i] {
			t.Fatalf("position %d: %s on a page of 25, %s in pages of 7", i, h.Request.Title, all[i])
		}
	}
	// A hit that only the meaning found says so; one the words found does not.
	for _, h := range long.Results {
		isSemantic := strings.HasPrefix(h.Request.Title, "S")
		if (h.Match == MatchSemantic) != isSemantic || (h.MatchReason == "semantic match") != isSemantic {
			t.Fatalf("%s: match %q reason %q", h.Request.Title, h.Match, h.MatchReason)
		}
	}
	// The state filter is in both rankings.
	f.State = "done"
	if page, err := st.SearchRequestsWith(f, SearchAuto); err != nil || len(page.Results) != 0 {
		t.Fatalf("no request is done: %d %v", len(page.Results), err)
	}
}

// Without a query the list is the list it always was, byte for byte, with its
// cursor of ids, whether or not embeddings are in use.
func TestRequestSearchWithoutAQueryIsTheListItAlwaysWas(t *testing.T) {
	st := openTest(t)
	_, c := newFakeEmbedder(t)
	for i := range 14 {
		addRequest(t, st, fmt.Sprintf("R%02d", i), "q1 q2 f"+strconv.Itoa(i))
	}
	embedKindAll(t, st, c, KindRequest)
	for _, f := range []requestdomain.SearchFilter{
		{Limit: 5},
		{Limit: 5, State: "open", Type: "feature"},
		// A question made of ordinary words asks for the list of the backlog.
		{Query: "was ist noch zu tun", Limit: 5},
		{Query: "", Scope: scope.Axes{Project: roleProject}, Limit: 25, FullDescription: true},
	} {
		want, err := st.SearchRequests(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []SearchMode{SearchAuto, SearchFullTextOnly} {
			// Not configured, and configured and switched on for requests.
			st.SetEmbedder(EmbedSearchConfig{})
			got, err := st.SearchRequestsWith(f, mode)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("filter %+v, mode %d, no server: %+v %v, want %+v", f, mode, got, err, want)
			}
			st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})
			got, err = st.SearchRequestsWith(f, mode)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("filter %+v, mode %d: %+v %v, want %+v", f, mode, got, err, want)
			}
		}
	}
	// The cursor is an id.
	page, _ := st.SearchRequests(requestdomain.SearchFilter{Limit: 5})
	if _, err := strconv.ParseInt(page.NextCursor, 10, 64); err != nil {
		t.Fatalf("cursor %q of the plain list is not an id", page.NextCursor)
	}
	// A query with words, and no embedding in use: the old cursor, too.
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindDocument}})
	page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Query: "q1 q2", Limit: 5}, SearchAuto)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.ParseInt(page.NextCursor, 10, 64); err != nil {
		t.Fatalf("cursor %q of the full-text search is not an id", page.NextCursor)
	}
}

// A ranked cursor that reaches a search which has fallen back to the words (the
// embedding server failed between two pages) is read as a position in the list.
func TestRequestSearchRankedCursorStillWorksWhenTheFallbackAnswers(t *testing.T) {
	st := openTest(t)
	for i := range 12 {
		addRequest(t, st, fmt.Sprintf("R%02d", i), "q1 q2 f"+strconv.Itoa(i))
	}
	f := requestdomain.SearchFilter{Query: "q1 q2", Limit: 5}
	first, err := st.SearchRequests(f)
	if err != nil {
		t.Fatal(err)
	}
	f.Limit = 25
	whole, err := st.SearchRequests(f)
	if err != nil || len(whole.Results) != 12 {
		t.Fatalf("%d %v", len(whole.Results), err)
	}
	f.Limit, f.Cursor = 5, HybridCursor(5)
	second, err := st.SearchRequestsWith(f, SearchAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 5 || len(second.Results) != 5 || second.Results[0].Request.ID != whole.Results[5].Request.ID {
		t.Fatalf("the page after position 5 starts with %v", second.Results)
	}
	if second.NextCursor != HybridCursor(10) {
		t.Fatalf("next cursor %q", second.NextCursor)
	}
	if !ValidRequestCursor("") || !ValidRequestCursor("12") || !ValidRequestCursor("~3") || ValidRequestCursor("~x") || ValidRequestCursor("abc") || ValidRequestCursor("~-1") {
		t.Fatal("ValidRequestCursor")
	}
}

// The text of a request holds its criteria: one that says what the request is for
// finds it by meaning, and adding one makes the vector out of date.
func TestRequestVectorsHoldTheCriteriaAndFollowThem(t *testing.T) {
	st := openTest(t)
	srv, c := newFakeEmbedder(t)
	id := addRequest(t, st, "Dienst", "segel mast", "wagen kaputt")
	embedKindAll(t, st, c, KindRequest)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})
	page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Query: "auto defekt", Limit: 5}, SearchVectorOnly)
	if err != nil || len(page.Results) != 1 || page.Results[0].Request.ID != id {
		t.Fatalf("a criterion is part of the text: %+v %v", page.Results, err)
	}
	if got := srv.Requests(); len(got) == 0 || !strings.Contains(strings.Join(got[0], " "), "- wagen kaputt") {
		t.Fatalf("the criterion is not in the embedded text: %v", got)
	}
	m := modelOf(c)
	// Ticking a criterion off is not a change of the text.
	crit, err := st.AddCriterion(id, "noch ein Kriterium", "mia")
	if err != nil {
		t.Fatal(err)
	}
	due, err := st.EmbeddingDue(KindRequest, m, 8, time.Now(), false)
	if err != nil || len(due.Items) != 1 || due.Items[0].ID != id {
		t.Fatalf("a new criterion makes the vector stale, although the request was not touched: %+v %v", due, err)
	}
	embedKindAll(t, st, c, KindRequest)
	if err := st.SetCriterionState(crit.ID, "met", requestdomain.Evidence{Kind: "test", Ref: "x"}); err != nil {
		t.Fatal(err)
	}
	due, err = st.EmbeddingDue(KindRequest, m, 8, time.Now(), false)
	if err != nil || len(due.Items) != 0 || len(due.Unchanged) != 1 {
		t.Fatalf("ticking a criterion off leaves the text alone: %+v %v", due, err)
	}
	status, err := st.EmbeddingStatus(KindRequest, m)
	if err != nil || status.Active != 1 || status.Current != 1 || status.Due != 0 {
		t.Fatalf("%+v %v", status, err)
	}
}

// A request distilled from the private session of another account is held back
// from its owner's colleagues; the vector part must not give it away.
func TestRequestVectorPartKeepsHeldRequestsOfPrivateSessionsHidden(t *testing.T) {
	f := newVisFixture(t) // f.b is a private session of mia
	_, c := newFakeEmbedder(t)
	res, err := f.st.db.Exec(`INSERT INTO requests(type,title,description,state,project,origin,person,session_ref,created_at,updated_at)
		VALUES('feature','held wish','wagen kaputt','open',?,'distilled','',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		roleProject, "session:"+strconv.FormatInt(f.b.ID, 10)+"#1")
	if err != nil {
		t.Fatal(err)
	}
	held, _ := res.LastInsertId()
	if _, err := f.st.db.Exec(`INSERT INTO search_documents(kind,domain_id,title,body,project) VALUES('request',?,'held wish','wagen kaputt',?)`, held, roleProject); err != nil {
		t.Fatal(err)
	}
	addRequest(t, f.st, "open wish", "wagen kaputt")
	embedKindAll(t, f.st, c, KindRequest)
	f.st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})
	for _, tc := range []struct {
		who  Principal
		want []string
	}{
		{robin, []string{"open wish"}}, {lena, []string{"open wish"}}, {rex, []string{"open wish"}}, {gus, []string{"open wish"}},
		{mia, []string{"held wish", "open wish"}},
	} {
		for _, mode := range []SearchMode{SearchVectorOnly, SearchHybridStrict, SearchFullTextOnly} {
			pa := f.st.Access(tc.who)
			page, err := f.st.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{Query: "wagen kaputt", Limit: 10}), mode)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, h := range page.Results {
				got = append(got, h.Request.Title)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s, mode %d: %v, want %v", tc.who.Label, mode, got, tc.want)
			}
		}
	}
}
