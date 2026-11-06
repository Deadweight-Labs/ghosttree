package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// The accounts of accessFixture by label.
var personIDs = map[string]int64{"robin": 1, "lena": 2, "mia": 3, "rex": 4, "gus": 5, "nora": 6}

const freeProject = "github.com/free/repo" // a remote nobody has claimed

type kindHit struct {
	name     string
	semantic bool
}

func hitNames(hits []kindHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

// kindAdapter hides how an object of one kind is made and how the server searches
// it, so that the same cases run on every kind. search does what the handler of
// the kind does: the production search function with the row rule and the
// prefilter of the reader.
type kindAdapter struct {
	kind string
	// query is the query that every object of the world matches, by its words
	// and by its meaning.
	query string
	// global: objects without a project exist and belong to their author (G).
	global bool
	// add makes an object whose name is the title (the path, the slug), with a text.
	add func(t *testing.T, st *Store, name, text, project, person string)
	// search runs the search as who, in a project ("" for every project, where the
	// kind has such a search), with the given limit.
	search func(t *testing.T, st *Store, who Principal, project, q string, mode SearchMode, limit int) []kindHit
	// needsProject: the search of this kind always names a project.
	needsProject bool
}

func kindAdapters() []kindAdapter {
	return []kindAdapter{
		{
			kind: KindRequest, query: "q1 q2 q3 q4", global: true,
			add: func(t *testing.T, st *Store, name, text, project, person string) {
				t.Helper()
				if _, err := st.CreateRequest(requestdomain.CreateInput{
					Request:  requestdomain.Request{Type: "feature", Title: name, Description: text, Scope: scope.Axes{Project: project}, Person: person, Origin: "agent"},
					AuthorID: personIDs[person],
				}); err != nil {
					t.Fatal(err)
				}
			},
			search: func(t *testing.T, st *Store, who Principal, project, q string, mode SearchMode, limit int) []kindHit {
				t.Helper()
				pa := st.Access(who)
				var out []kindHit
				cursor := ""
				for range 20 {
					page, err := st.SearchRequestsWith(pa.RequestFilter(requestdomain.SearchFilter{
						Query: q, Scope: scope.Axes{Project: project}, Limit: limit, Cursor: cursor}), mode)
					if err != nil {
						t.Fatal(err)
					}
					for _, h := range page.Results {
						out = append(out, kindHit{h.Request.Title, h.Match == MatchSemantic})
					}
					// A limit above the page size of 25 asks for everything: follow the
					// cursors. Below it, one page is what was asked for.
					if cursor = page.NextCursor; cursor == "" || limit <= 25 {
						break
					}
				}
				return out
			},
		},
		{
			kind: KindPain, query: "q1", needsProject: true,
			add: func(t *testing.T, st *Store, name, text, project, person string) {
				t.Helper()
				ts := now()
				res, err := st.db.Exec(`INSERT INTO pains(project,title,body,state,person,created_at,updated_at) VALUES(?,?,?,'active',?,?,?)`, project, name, text, person, ts, ts)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := res.LastInsertId()
				if _, err := st.db.Exec(`INSERT INTO pain_authors(pain_id, account_id) VALUES(?,?)`, id, personIDs[person]); err != nil {
					t.Fatal(err)
				}
			},
			search: func(t *testing.T, st *Store, who Principal, project, q string, mode SearchMode, limit int) []kindHit {
				t.Helper()
				pa := st.Access(who)
				ps, err := st.SearchPainsWith(pain.Filter{Project: project, State: "all", Query: q, Limit: min(limit, 200)}, PainSearch{Keep: pa.CanSeePain, Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				var out []kindHit
				for _, p := range ps {
					out = append(out, kindHit{p.Title, p.Match == MatchSemantic})
				}
				return out
			},
		},
		{
			kind: KindGhost, query: "q1 q2 q3 q4",
			add: func(t *testing.T, st *Store, name, text, project, person string) {
				t.Helper()
				if _, err := st.PutGhostFile(GhostFile{Project: project, Path: name, Kind: "file", Description: text, Person: person}); err != nil {
					t.Fatal(err)
				}
			},
			search: func(t *testing.T, st *Store, who Principal, project, q string, mode SearchMode, limit int) []kindHit {
				t.Helper()
				pa := st.Access(who)
				readable := func(g GhostFile) bool { return pa.CanSeeGhost(g) && pa.CanDeliverGhost(g) }
				gs, err := st.SearchGhostFilesWith(q, GhostSearch{Project: project, Limit: limit, Keep: readable, Prefilter: pa.GhostPrefilter(), Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				var out []kindHit
				for _, g := range gs {
					// What the handler does afterwards.
					if readable(g) {
						out = append(out, kindHit{g.Path, g.Match == MatchSemantic})
					}
				}
				return out
			},
		},
		{
			kind: KindDocument, query: "q1 q2 q3 q4", global: true,
			add: func(t *testing.T, st *Store, name, text, project, person string) {
				t.Helper()
				if _, err := st.CreateDocument(Document{Project: project, Slug: name, Kind: "report", Title: name, Person: person}, text, "m"); err != nil {
					t.Fatal(err)
				}
			},
			search: func(t *testing.T, st *Store, who Principal, project, q string, mode SearchMode, limit int) []kindHit {
				t.Helper()
				pa := st.Access(who)
				ds, err := st.SearchDocuments(q, DocumentSearch{Project: project, Limit: limit, Keep: pa.CanSeeDocument, Prefilter: pa.DocumentPrefilter(), Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				var out []kindHit
				for _, d := range ds {
					if pa.CanSeeDocument(d) {
						out = append(out, kindHit{d.Slug, d.Match == MatchSemantic})
					}
				}
				return out
			},
		},
	}
}

// embedKindAll does what the background job does for one kind, in one go.
func embedKindAll(t *testing.T, st *Store, c *embed.Client, kind string) {
	t.Helper()
	for range 400 {
		due, err := st.EmbeddingDue(kind, modelOf(c), 16, time.Now(), true)
		if err != nil {
			t.Fatal(err)
		}
		if len(due.Items) == 0 {
			return
		}
		texts := make([]string, len(due.Items))
		for i, d := range due.Items {
			texts[i] = d.Text
		}
		vecs, err := c.Documents(context.Background(), texts)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range due.Items {
			q, scale, err := embed.Quantize(vecs[i])
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertEmbedding(Embedding{Kind: kind, ObjectID: d.ID, Model: c.Model(), PrefixVersion: c.PrefixVersion(),
				TextHash: d.Hash, Dim: len(q), Scale: float64(scale), Vec: q, ReadAt: due.ReadAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatalf("embedding %s did not converge", kind)
}

// kindWorld has, for one kind, the objects that every reader may see in the claimed
// project (V1, V2, V3; V2 and V1 match by words, V3 only by meaning), `hidden` objects
// of a project that only its owner reads (they rank above the visible ones in the
// embedding ranking), the objects of two accounts in a project nobody claimed, and,
// for kinds that have them, an object of mia that belongs to no project.
func kindWorld(t *testing.T, a kindAdapter, hidden int) (*Store, *embed.Client) {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	if _, err := st.EnsureProject("person:1", otherProject); err != nil {
		t.Fatal(err)
	}
	_, c := newFakeEmbedder(t)
	// The hidden objects come first: the list of pains is in the order of the ids,
	// and a hidden row that sits above the visible ones there moves them too.
	for i := range hidden {
		a.add(t, st, fmt.Sprintf("H%d", i), "s1 s2 s3 s4 q1", otherProject, "robin")
	}
	a.add(t, st, "V1", "q1 q2 f1 f2", roleProject, "mia")
	a.add(t, st, "V2", "q1 s2 s3", roleProject, "mia")
	a.add(t, st, "V3", "s1 s2", roleProject, "mia")
	a.add(t, st, "U-nora", "s1 s2 s3 s4 q1", freeProject, "nora")
	a.add(t, st, "U-mia", "s1 s2 s3 s4 q1", freeProject, "mia")
	if a.global {
		a.add(t, st, "G", "s1 s2 s3 s4 q1", "", "mia")
	}
	embedKindAll(t, st, c, a.kind)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{a.kind}})
	return st, c
}

// visibleOf is everything the reader is shown, over all the projects of the world.
func visibleOf(t *testing.T, a kindAdapter, st *Store, who Principal, mode SearchMode) []string {
	t.Helper()
	seen := map[string]bool{}
	projects := []string{roleProject, otherProject, freeProject}
	if !a.needsProject {
		projects = []string{""}
	}
	for _, p := range projects {
		for _, h := range a.search(t, st, who, p, a.query, mode, 500) {
			seen[h.name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

var (
	robin = Principal{ID: "person:1", Label: "robin"}
	lena  = Principal{ID: "person:2", Label: "lena"}
	mia   = Principal{ID: "person:3", Label: "mia"}
	rex   = Principal{ID: "person:4", Label: "rex"}
	gus   = Principal{ID: "person:5", Label: "gus"}
	nora  = Principal{ID: "person:6", Label: "nora"}
)

// expected is what each reader may see in kindWorld, derived from the read rules
// and not from the code under test: owner and instance admin robin reads everything
// in his org and every unclaimed project; the members read the claimed project, the
// guest too; the stranger only what she wrote in the unclaimed project; mia's
// project-less request is hers alone, and a project-less document belongs to the
// instance owner. A project shared with the org opens it to the members of the org.
func expected(a kindAdapter, who Principal, hidden int, shared bool) []string {
	var want []string
	claimed := []string{"V1", "V2", "V3"}
	opened := func() {
		for i := range hidden {
			want = append(want, fmt.Sprintf("H%d", i))
		}
	}
	switch who.Label {
	case "robin":
		want = append(append(want, claimed...), "U-nora", "U-mia")
		opened()
		if a.kind == KindDocument {
			want = append(want, "G")
		}
	case "lena", "rex", "gus":
		want = append(want, claimed...)
		if shared {
			opened()
		}
	case "mia":
		want = append(append(want, claimed...), "U-mia")
		if shared {
			opened()
		}
		if a.kind == KindRequest {
			want = append(want, "G")
		}
	case "nora":
		want = append(want, "U-nora")
	}
	slices.Sort(want)
	return want
}

func TestHybridKindsVisibilityForGuestMemberOwnerAndStranger(t *testing.T) {
	for _, a := range kindAdapters() {
		t.Run(a.kind, func(t *testing.T) {
			const hidden = 5
			st, _ := kindWorld(t, a, hidden)
			for _, shared := range []bool{false, true} {
				if shared {
					mustShare(t, st, "person:1", otherProject, true)
				}
				for _, who := range []Principal{robin, lena, mia, rex, gus, nora} {
					want := expected(a, who, hidden, shared)
					// The rule is the same for the words and for the meaning: the
					// full-text ranking, the embedding ranking and their fusion show
					// the reader the same set of objects.
					for _, mode := range []SearchMode{SearchFullTextOnly, SearchVectorOnly, SearchHybridStrict} {
						got := visibleOf(t, a, st, who, mode)
						// V3 has no word in common with the query, only its meaning; the
						// full-text ranking cannot have it.
						w := want
						if mode == SearchFullTextOnly {
							w = slices.DeleteFunc(slices.Clone(want), func(n string) bool { return n == "V3" })
						}
						if !slices.Equal(got, w) {
							t.Errorf("%s as %s (shared with the org: %v), mode %d: %v, want %v", a.kind, who.Label, shared, mode, got, w)
						}
					}
				}
			}
		})
	}
}

// unclaimedWorld is kindWorld for a kind whose search names a project and in which a
// claimed project hides nothing from its readers (pains): there the objects of two
// accounts share a project nobody claimed, which shows each of them only their own.
// mia's V1, V2 and V3 are what she sees; `hidden` objects of nora rank above them.
func unclaimedWorld(t *testing.T, a kindAdapter, hidden int) *Store {
	t.Helper()
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	_, c := newFakeEmbedder(t)
	for i := range hidden {
		a.add(t, st, fmt.Sprintf("H%d", i), "s1 s2 s3 s4 q1", freeProject, "nora")
	}
	a.add(t, st, "V1", "q1 q2 f1 f2", freeProject, "mia")
	a.add(t, st, "V2", "q1 s2 s3", freeProject, "mia")
	a.add(t, st, "V3", "s1 s2", freeProject, "mia")
	embedKindAll(t, st, c, a.kind)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{a.kind}})
	return st
}

func TestHybridKindsHiddenRowsNeverMoveVisibleOnes(t *testing.T) {
	for _, a := range kindAdapters() {
		t.Run(a.kind, func(t *testing.T) {
			var clean, crowded *Store
			var readers []Principal
			project := ""
			if a.needsProject {
				// A claimed project hides nothing from its readers, so the hidden
				// rows are the ones of another account in an unclaimed project.
				clean, crowded = unclaimedWorld(t, a, 0), unclaimedWorld(t, a, 40)
				readers, project = []Principal{mia}, freeProject
			} else {
				clean, _ = kindWorld(t, a, 0)
				crowded, _ = kindWorld(t, a, 40)
				readers = []Principal{lena, gus, rex}
			}
			visibleV := func(st *Store, who Principal) []string {
				var out []string
				for _, h := range a.search(t, st, who, project, a.query, SearchAuto, 20) {
					if strings.HasPrefix(h.name, "V") {
						out = append(out, h.name)
					}
				}
				return out
			}
			for _, who := range readers {
				want := visibleV(clean, who)
				if len(want) < 3 {
					t.Fatalf("%s: the reader should see V1 V2 V3 in the clean world, got %v", who.Label, want)
				}
				if got := visibleV(crowded, who); !slices.Equal(got, want) {
					t.Errorf("%s as %s: hidden rows changed the order to %v, want %v", a.kind, who.Label, got, want)
				}
			}
			// The test is worth something only if fusing all rows first and filtering
			// afterwards would have given another order: the owner, who reads the
			// hidden rows too, sees what that naive way produces.
			naive := visibleV(crowded, robin)
			if want := visibleV(clean, readers[0]); slices.Equal(naive, want) {
				t.Errorf("%s: the fixture does not tell filter-before-fusion from filter-after: both give %v", a.kind, naive)
			}
		})
	}
}

// Without an embedding server, with the server failing, and with the kind not
// switched on for search, the answer is the full-text answer, without an error.
func TestHybridKindsAnswerFromFullTextWhenTheEmbeddingIsNotUsed(t *testing.T) {
	for _, a := range kindAdapters() {
		t.Run(a.kind, func(t *testing.T) {
			st, c := kindWorld(t, a, 3)
			project := roleProject
			if !a.needsProject {
				project = ""
			}
			names := func() []string { return hitNames(a.search(t, st, lena, project, a.query, SearchAuto, 20)) }
			hybrid := names()
			st.SetEmbedder(EmbedSearchConfig{})
			plain := names()
			if !slices.Equal(plain, hitNames(a.search(t, st, lena, project, a.query, SearchFullTextOnly, 20))) {
				t.Fatalf("no server: %v is not the full-text answer", plain)
			}
			if slices.Equal(plain, hybrid) {
				t.Fatalf("the fixture does not tell the full-text answer from the hybrid one: %v", plain)
			}

			// Switched on for another kind only: the vectors of this one are unused,
			// and the query is not even sent to the server.
			other := KindDocument
			if a.kind == KindDocument {
				other = KindGhost
			}
			srv, c2 := newFakeEmbedder(t)
			st.SetEmbedder(EmbedSearchConfig{Embedder: c2, Kinds: []string{other}})
			if got := names(); !slices.Equal(got, plain) || srv.Calls() != 0 {
				t.Fatalf("kind off: %v (%d calls), want %v without a call", got, srv.Calls(), plain)
			}

			// A server that fails: the same answer again, and the failure is counted.
			srv.Fail.Store(true)
			st.SetEmbedder(EmbedSearchConfig{Embedder: c2, Kinds: []string{a.kind}})
			if got := names(); !slices.Equal(got, plain) {
				t.Fatalf("failing server: %v, want %v", got, plain)
			}
			if _, fallbacks := st.EmbedSearchStats(); fallbacks == 0 {
				t.Fatal("the fallback was not counted")
			}
			_ = c
		})
	}
}

// With the kind switched on, a vector of an object that is archived or gone is no
// hit, and the job takes the vector away.
func TestHybridKindsNeverReturnAnArchivedOrDeletedObject(t *testing.T) {
	t.Run("document", func(t *testing.T) {
		st := openTest(t)
		_, c := newFakeEmbedder(t)
		st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindDocument}})
		d, err := st.CreateDocument(Document{Project: "p", Slug: "wagen", Kind: "report", Title: "wagen", Person: "mia"}, "wagen kaputt", "m")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateDocument(Document{Project: "p", Slug: "live", Kind: "report", Title: "live", Person: "mia"}, "wagen kaputt", "m"); err != nil {
			t.Fatal(err)
		}
		embedKindAll(t, st, c, KindDocument)
		search := func(mode SearchMode) []string {
			ds, err := st.SearchDocuments("auto defekt", DocumentSearch{Project: "p", Limit: 10, Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, d := range ds {
				out = append(out, d.Slug)
			}
			slices.Sort(out)
			return out
		}
		if got := search(SearchVectorOnly); !slices.Equal(got, []string{"live", "wagen"}) {
			t.Fatalf("before: %v", got)
		}
		if err := st.PatchDocument(d.ID, map[string]string{"status": "archived"}); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []SearchMode{SearchVectorOnly, SearchHybridStrict, SearchFullTextOnly, SearchAuto} {
			if got := search(mode); slices.Contains(got, "wagen") {
				t.Fatalf("mode %d returned the archived document: %v", mode, got)
			}
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE kind='document'`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("the vector stays until the job removes it: %d %v", n, err)
		}
		if removed, err := st.PurgeEmbeddings(KindDocument); err != nil || removed != 1 {
			t.Fatalf("purge removed %d: %v", removed, err)
		}
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE kind='document'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("after the purge: %d %v", n, err)
		}
		// Back from the archive, the document is embedded again.
		if err := st.PatchDocument(d.ID, map[string]string{"status": "active"}); err != nil {
			t.Fatal(err)
		}
		embedKindAll(t, st, c, KindDocument)
		if got := search(SearchVectorOnly); !slices.Equal(got, []string{"live", "wagen"}) {
			t.Fatalf("after: %v", got)
		}
	})
	t.Run("ghost", func(t *testing.T) {
		st := openTest(t)
		_, c := newFakeEmbedder(t)
		st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindGhost}})
		for _, p := range []string{"a.go", "b.go"} {
			if _, err := st.PutGhostFile(GhostFile{Project: "p", Path: p, Kind: "file", Description: "wagen kaputt"}); err != nil {
				t.Fatal(err)
			}
		}
		embedKindAll(t, st, c, KindGhost)
		// The row goes, as archiving a deleted path does; its vector stays behind.
		if _, err := st.db.Exec(`DELETE FROM ghost_files WHERE path='a.go'`); err != nil {
			t.Fatal(err)
		}
		gs, err := st.SearchGhostFilesWith("auto defekt", GhostSearch{Project: "p", Limit: 10, Mode: SearchVectorOnly})
		if err != nil || len(gs) != 1 || gs[0].Path != "b.go" {
			t.Fatalf("%+v %v", gs, err)
		}
		if removed, err := st.PurgeEmbeddings(KindGhost); err != nil || removed != 1 {
			t.Fatalf("purge removed %d: %v", removed, err)
		}
	})
	t.Run("request", func(t *testing.T) {
		// A request is not archived, it is done or dropped; those stay findable, and
		// the state filter of the search applies to the vector part as to the words.
		st := openTest(t)
		_, c := newFakeEmbedder(t)
		st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindRequest}})
		open, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "wagen", Description: "wagen kaputt"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "auch wagen", Description: "wagen kaputt"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.DropRequest(open.Request.ID, "nein", "robin"); err != nil {
			t.Fatal(err)
		}
		embedKindAll(t, st, c, KindRequest)
		for state, want := range map[string]int{"open": 1, "dropped": 1, "": 2} {
			page, err := st.SearchRequestsWith(requestdomain.SearchFilter{Query: "auto defekt", State: state, Limit: 10}, SearchVectorOnly)
			if err != nil || len(page.Results) != want {
				t.Fatalf("state %q: %d hits (%v), want %d", state, len(page.Results), err, want)
			}
		}
	})
}
