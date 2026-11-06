package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// hybridAPI is the access fixture with an embedding server behind its store.
// The text ranking of the query "q1 q2 q3 q4" is V1, V2; the embedding ranking
// of what a guest may see is V2, V3, V1. With `hidden` staged entries (which a
// guest may not read) at the top of the embedding ranking, fusing before
// filtering would put V1 before V2.
func hybridAPI(t *testing.T, hidden int) *accessAPIFixture {
	t.Helper()
	f := accessAPI(t, true)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"s1": "q1", "s2": "q2", "s3": "q3", "s4": "q4"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	p := scope.Axes{Project: accProject}
	add := func(title, body, confidence, person string, ax scope.Axes) {
		t.Helper()
		if _, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: body, Scope: ax, Confidence: confidence, Person: person}); err != nil {
			t.Fatal(err)
		}
	}
	add("V1", "q1 q2 f1 f2", "trusted", "mia", p)
	add("V2", "q1 s2 s3", "trusted", "mia", p)
	add("V3", "s1 s2", "trusted", "mia", p)
	for i := range hidden {
		add(fmt.Sprintf("H%d", i), "s1 s2 s3 s4", "staged", "robin", p)
	}
	// Found by a word of the query, so that a cap on the hits found by meaning alone
	// does not decide whether a reader who may see them does.
	add("X0", "s1 s2 s3 s4 q1", "trusted", "robin", scope.Axes{Project: accOther})
	add("G0", "s1 s2 s3 s4 q1", "trusted", "robin", scope.Axes{})

	job := embedjob.New(f.st, c, embedjob.Config{Batch: 16, StartDelay: -1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	f.st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2})
	return f
}

func hybridHits(t *testing.T, f *accessAPIFixture, who, path string) ([]string, map[string]string) {
	t.Helper()
	var out struct {
		Knowledge []store.Knowledge `json:"knowledge"`
	}
	body := f.expect(t, who, 200, "GET", path, nil)
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	var titles []string
	match := map[string]string{}
	for _, k := range out.Knowledge {
		titles = append(titles, k.Title)
		match[k.Title] = k.Match
	}
	return titles, match
}

func TestSearchAPIHybridShowsAGuestOnlyWhatAGuestMaySeeInTheSameOrder(t *testing.T) {
	const path = "/api/search?kind=knowledge&q=q1+q2+q3+q4&limit=20"
	clean, withHidden := hybridAPI(t, 0), hybridAPI(t, 40)

	want, match := hybridHits(t, clean, "gus", path)
	if !slices.Equal(want, []string{"V2", "V1", "V3"}) {
		t.Fatalf("guest without hidden rows: %v", want)
	}
	if match["V3"] != store.MatchSemantic || match["V1"] != "" || match["V2"] != "" {
		t.Fatalf("markers: %v", match)
	}
	got, _ := hybridHits(t, withHidden, "gus", path)
	if !slices.Equal(got, want) {
		t.Fatalf("hidden rows changed the guest's answer: %v, want %v", got, want)
	}
	for _, title := range got {
		if strings.HasPrefix(title, "H") || title == "X0" || title == "G0" {
			t.Fatalf("a guest was shown %q", title)
		}
	}

	// A member reads the staged entries; the owner reads the other project; the
	// author of the global entry reads that; a stranger reads nothing.
	const wide = "/api/search?kind=knowledge&q=q1+q2+q3+q4&limit=100"
	if members, _ := hybridHits(t, withHidden, "mia", wide); !slices.Contains(members, "H0") || slices.Contains(members, "X0") {
		t.Fatalf("member: %v", members)
	}
	if owner, _ := hybridHits(t, withHidden, "robin", wide); !slices.Contains(owner, "X0") || !slices.Contains(owner, "G0") {
		t.Fatalf("owner: %v", owner)
	}
	if stranger, _ := hybridHits(t, withHidden, "nora", wide); len(stranger) != 0 {
		t.Fatalf("stranger: %v", stranger)
	}
	// The same through the project scope that sessions search with.
	scoped := path + "&scope=union&project=" + accProject
	if guest, _ := hybridHits(t, withHidden, "gus", scoped); !slices.Equal(guest, want) {
		t.Fatalf("guest, union scope: %v", guest)
	}
}

func TestSearchAPIAnswersFromFullTextWhenTheEmbeddingServerIsGone(t *testing.T) {
	const path = "/api/search?kind=knowledge&q=q1+q2&limit=20"
	f := hybridAPI(t, 0)
	hybrid, _ := hybridHits(t, f, "mia", path)
	f.st.SetEmbedder(store.EmbedSearchConfig{}) // as on a server started without --embed-url
	plain, match := hybridHits(t, f, "mia", path)
	if len(plain) == 0 || len(plain) > len(hybrid) {
		t.Fatalf("hybrid %v, plain %v", hybrid, plain)
	}
	for title, m := range match {
		if m != "" {
			t.Fatalf("%s carries %q without an embedding server", title, m)
		}
	}
	body := f.expect(t, "mia", 200, "GET", path, nil)
	if strings.Contains(body, `"match"`) {
		t.Fatalf("the answer of a search without embeddings must be the old one: %s", body)
	}
}
