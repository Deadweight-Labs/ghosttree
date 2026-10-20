package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type apiHit struct {
	name     string
	semantic bool
}

func names(hits []apiHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

// hybridKindsAPI is the access fixture with an embedding server behind its store
// and, in the claimed project, a request, a pain, a description of a path and a
// document each called V1, V2, V3 (V1 and V2 match the query "q1 q2 q3 q4" by words,
// V3 by meaning only). `hidden` objects of every kind sit in the other project,
// which only its owner reads, and rank above the visible ones in the embedding
// ranking.
func hybridKindsAPI(t *testing.T, hidden int, searchKinds ...string) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, true)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"s1": "q1", "s2": "q2", "s3": "q3", "s4": "q4"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	db := f.st.DB()
	add := func(name, text, project, person string, account int64) {
		t.Helper()
		if _, err := f.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: name, Description: text, Scope: scope.Axes{Project: project}, Person: person}, AuthorID: account}); err != nil {
			t.Fatal(err)
		}
		res, err := db.Exec(`INSERT INTO pains(project,title,body,state,person,created_at,updated_at) VALUES(?,?,?,'active',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, project, name, text, person)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO pain_authors(pain_id, account_id) VALUES(?,?)`, id, account); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.PutGhostFile(store.GhostFile{Project: project, Path: name + ".go", Kind: "file", Description: text, Person: person}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.CreateDocument(store.Document{Project: project, Slug: strings.ToLower(name), Kind: "report", Title: name, Person: person}, text, "m"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range hidden {
		add(fmt.Sprintf("H%d", i), "s1 s2 s3 s4 q1", accOther, "robin", 1)
	}
	add("V1", "q1 q2 f1 f2", accProject, "mia", 3)
	add("V2", "q1 s2 s3", accProject, "mia", 3)
	add("V3", "s1 s2", accProject, "mia", 3)

	job := embedjob.New(f.st, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: store.EmbedKinds()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	if len(searchKinds) == 0 {
		searchKinds = store.EmbedKinds()
	}
	f.st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2, Kinds: searchKinds})
	return f
}

func (f *accessAPIFixture) get(t *testing.T, who, path string, into any) {
	t.Helper()
	body := f.expect(t, who, 200, "GET", path, nil)
	if err := json.Unmarshal([]byte(body), into); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, body)
	}
}

// One search per kind, as the handlers of the server answer it.
var kindSearches = []struct {
	kind string
	hits func(t *testing.T, f *accessAPIFixture, who, query string) []apiHit
}{
	{store.KindRequest, func(t *testing.T, f *accessAPIFixture, who, q string) []apiHit {
		var out struct {
			Requests []requestdomain.SearchHit `json:"requests"`
		}
		f.get(t, who, "/api/search?kind=requests&limit=20&q="+url.QueryEscape(q), &out)
		var hits []apiHit
		for _, h := range out.Requests {
			hits = append(hits, apiHit{h.Request.Title, h.Match == store.MatchSemantic})
		}
		return hits
	}},
	{store.KindGhost, func(t *testing.T, f *accessAPIFixture, who, q string) []apiHit {
		var out []store.GhostFile
		f.get(t, who, "/api/ghosts/search?limit=20&q="+url.QueryEscape(q), &out)
		var hits []apiHit
		for _, g := range out {
			hits = append(hits, apiHit{strings.TrimSuffix(g.Path, ".go"), g.Match == store.MatchSemantic})
		}
		return hits
	}},
	{store.KindPain, func(t *testing.T, f *accessAPIFixture, who, q string) []apiHit {
		var out []pain.Pain
		f.get(t, who, "/api/pains?state=all&limit=20&project="+url.QueryEscape(accProject)+"&q="+url.QueryEscape(q), &out)
		var hits []apiHit
		for _, p := range out {
			hits = append(hits, apiHit{p.Title, p.Match == store.MatchSemantic})
		}
		return hits
	}},
	{store.KindDocument, func(t *testing.T, f *accessAPIFixture, who, q string) []apiHit {
		var out []store.Document
		f.get(t, who, "/api/documents?limit=20&q="+url.QueryEscape(q), &out)
		var hits []apiHit
		for _, d := range out {
			hits = append(hits, apiHit{d.Title, d.Match == store.MatchSemantic})
		}
		return hits
	}},
}

// queryFor is a query that the words of V1 and V2 match and the meaning of V3
// matches. The text match of pains is a phrase in the title or the text, so theirs
// is one word.
func queryFor(kind string) string {
	if kind == store.KindPain {
		return "q1"
	}
	return "q1 q2 q3 q4"
}

// The embedding part of every search shows a reader what the words part shows and
// nothing else, in the same order whether or not hidden objects exist.
func TestSearchAPIsHybridShowAGuestOnlyWhatAGuestMaySeeInTheSameOrder(t *testing.T) {
	clean, crowded := hybridKindsAPI(t, 0), hybridKindsAPI(t, 40)
	for _, ks := range kindSearches {
		t.Run(ks.kind, func(t *testing.T) {
			query := queryFor(ks.kind)
			want := ks.hits(t, clean, "gus", query)
			if len(want) != 3 {
				t.Fatalf("the guest sees V1 V2 V3 in the clean world: %v", names(want))
			}
			got := ks.hits(t, crowded, "gus", query)
			if !slices.Equal(names(got), names(want)) {
				t.Fatalf("hidden objects changed the guest's answer: %v, want %v", names(got), names(want))
			}
			for _, h := range got {
				if strings.HasPrefix(h.name, "H") {
					t.Fatalf("a guest was shown %q", h.name)
				}
			}
			// V3 shares no word with the query: it is there by meaning, and says so.
			for _, h := range got {
				if h.semantic != (h.name == "V3") {
					t.Fatalf("markers: %+v", got)
				}
			}
			for _, who := range []string{"lena", "mia", "rex"} {
				if got := ks.hits(t, crowded, who, query); !slices.Equal(names(got), names(want)) {
					t.Errorf("%s: %v, want %v", who, names(got), names(want))
				}
			}
			// The owner reads the other project, too: those rows exist and are found.
			if owner := ks.hits(t, crowded, "robin", query); !slices.ContainsFunc(owner, func(h apiHit) bool { return strings.HasPrefix(h.name, "H") }) && ks.kind != store.KindPain {
				t.Errorf("the owner does not see the hidden objects either: %v", names(owner))
			}
		})
	}
}

// A stranger is told nothing: not "no hits", where the project is hidden, but what
// an unknown project would answer.
func TestSearchAPIsHybridShowAStrangerNothing(t *testing.T) {
	f := hybridKindsAPI(t, 3)
	for _, ks := range kindSearches {
		t.Run(ks.kind, func(t *testing.T) {
			if ks.kind == store.KindPain {
				f.expect(t, "nora", 404, "GET", "/api/pains?state=all&q=q1&project="+url.QueryEscape(accProject), nil)
				return
			}
			if got := ks.hits(t, f, "nora", "q1 q2 q3 q4"); len(got) != 0 {
				t.Fatalf("a stranger was shown %v", names(got))
			}
		})
	}
}

// A kind that is not switched on for search answers from its words, with the
// embedding server never asked; the others still use the vectors.
func TestSearchAPIsUseTheVectorsOnlyOfTheKindsSwitchedOn(t *testing.T) {
	f := hybridKindsAPI(t, 0, store.KindRequest)
	for _, ks := range kindSearches {
		got := ks.hits(t, f, "mia", queryFor(ks.kind))
		semantic := slices.ContainsFunc(got, func(h apiHit) bool { return h.semantic })
		if semantic != (ks.kind == store.KindRequest) {
			t.Errorf("%s: semantic hits %v in %v", ks.kind, semantic, got)
		}
		if len(got) == 0 {
			t.Errorf("%s: the words find V1 and V2 whatever the vectors do", ks.kind)
		}
	}
}

// The pages of a ranked request search are reachable through the API, with the
// cursor the answer hands out.
func TestRequestSearchAPIPagesWithTheCursorOfARankedSearch(t *testing.T) {
	f := hybridKindsAPI(t, 0)
	page := func(cursor string) (titles []string, next string) {
		var out requestdomain.SearchPage
		path := "/api/requests/search?limit=2&project=" + url.QueryEscape(accProject) + "&q=" + url.QueryEscape("q1 q2 q3 q4")
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		f.get(t, "mia", path, &out)
		for _, h := range out.Results {
			titles = append(titles, h.Request.Title)
		}
		return titles, out.NextCursor
	}
	first, next := page("")
	if next != "~2" || len(first) != 2 {
		t.Fatalf("%v %q", first, next)
	}
	second, next := page(next)
	if next != "" || len(second) != 1 {
		t.Fatalf("%v %q", second, next)
	}
	if all := append(first, second...); !slices.Equal(all, []string{"V2", "V1", "V3"}) {
		t.Fatalf("the pages are one ranking: %v", all)
	}
	// Without a query the cursor is still an id.
	var plain requestdomain.SearchPage
	f.get(t, "mia", "/api/requests/search?limit=1&project="+url.QueryEscape(accProject), &plain)
	if strings.HasPrefix(plain.NextCursor, "~") || plain.NextCursor == "" {
		t.Fatalf("the list pages by id: %q", plain.NextCursor)
	}
}
