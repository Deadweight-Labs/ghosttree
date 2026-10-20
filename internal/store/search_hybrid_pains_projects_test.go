package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// One search over several projects embeds the query once, shares the hits for
// meaning alone among all of them, keeps the order of the projects as they were
// given when no embedding is used, and still hides what the reader may not see.
func TestPainSearchOverSeveralProjectsIsOneSearch(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	srv, c := newFakeEmbedder(t)
	projects := []string{"github.com/dw/a", "github.com/dw/b", "github.com/dw/c"}
	for _, p := range projects {
		if _, err := st.EnsureProject("person:1", p); err != nil {
			t.Fatal(err)
		}
	}
	add := func(project, title, body, person string, account int64) {
		t.Helper()
		res, err := st.db.Exec(`INSERT INTO pains(project,title,body,state,person,created_at,updated_at) VALUES(?,?,?,'active',?,?,?)`, project, title, body, person, now(), now())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := st.db.Exec(`INSERT INTO pain_authors(pain_id, account_id) VALUES(?,?)`, id, account); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range projects[:2] {
		for i := range 5 {
			add(p, fmt.Sprintf("N-%s-%d", p[len(p)-1:], i), "wagen kaputt", "robin", 1)
		}
	}
	add(projects[2], "TREFFER", "auto defekt q1", "robin", 1)
	// A project nobody has claimed: only its author reads it.
	add(freeProject, "N-free", "wagen kaputt", "nora", 6)
	embedKindAll(t, st, c, KindPain)
	st.SetEmbedder(EmbedSearchConfig{Embedder: c, Kinds: []string{KindPain}})

	queries := func() int {
		n := 0
		for _, in := range srv.Requests() {
			for _, text := range in {
				if strings.HasPrefix(text, "task: search result") {
					n++
				}
			}
		}
		return n
	}
	all := append(slices.Clone(projects), freeProject)
	pa := st.Access(robin)
	f := pain.Filter{Projects: all, State: "all", Query: "auto defekt", Limit: 6}
	got, err := pa.SearchPains(f)
	if err != nil {
		t.Fatal(err)
	}
	if queries() != 1 {
		t.Fatalf("%d embeddings of the query for three projects", queries())
	}
	if len(got) == 0 || got[0].Title != "TREFFER" {
		t.Fatalf("the pain that holds the words leads the group, whatever project it is in: %v", painTitles(got))
	}
	neighbours := 0
	for _, p := range got {
		if p.Match == MatchSemantic {
			neighbours++
		}
	}
	if neighbours == 0 || neighbours > maxSemanticOnly(6) {
		t.Fatalf("%d hits for meaning alone in a group of six, at most %d: %v", neighbours, maxSemanticOnly(6), painTitles(got))
	}

	// The reader of a hidden project gets nothing of it, however many projects she
	// names; the author of the unclaimed one gets hers.
	// (The ranking by meaning alone shows every pain the reader may see, which the
	// fusion would cut to a few neighbours.)
	everything := pain.Filter{Projects: all, State: "all", Query: "auto defekt", Limit: 50}
	seen := func(who Principal) []string {
		list, err := st.SearchPainsWith(everything, PainSearch{Keep: st.Access(who).CanSeePain, Mode: SearchVectorOnly})
		if err != nil {
			t.Fatal(err)
		}
		return painTitles(list)
	}
	if list := seen(robin); len(list) != 12 || !slices.Contains(list, "N-free") {
		t.Fatalf("the admin reads the unclaimed project too: %v", list)
	}
	if list := seen(nora); !slices.Equal(list, []string{"N-free"}) {
		t.Fatalf("nora reads only what she wrote: %v", list)
	}
	if list := seen(gus); len(list) != 0 {
		t.Fatalf("a guest of another project reads none of these: %v", list)
	}

	// Without the embedding the pains come project after project in the order
	// given, as they did when each project was asked for on its own.
	st.SetEmbedder(EmbedSearchConfig{})
	rev := []string{projects[2], projects[1], projects[0]}
	list, err := pa.SearchPains(pain.Filter{Projects: rev, State: "all", Query: "q1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if titles := painTitles(list); !slices.Equal(titles, []string{"TREFFER"}) {
		t.Fatalf("%v", titles)
	}
	list, err = pa.SearchPains(pain.Filter{Projects: rev, State: "all", Query: "wagen kaputt", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 10 || list[0].Project != projects[1] || list[9].Project != projects[0] {
		t.Fatalf("project order: %v", painTitles(list))
	}
	// A single project is still searched as before.
	one, err := pa.SearchPains(pain.Filter{Project: projects[0], State: "all", Query: "wagen kaputt", Limit: 50})
	if err != nil || len(one) != 5 {
		t.Fatalf("%v %v", painTitles(one), err)
	}
}

func painTitles(ps []pain.Pain) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Title
	}
	return out
}
