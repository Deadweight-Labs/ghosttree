package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// usageWorld has two entries that contain the words of the query "q1 q2" and
// thirty that only share its meaning, in a project where access is enforced (so
// the API fetches four times the limit).
func usageWorld(t *testing.T) (*accessAPIFixture, map[string]int64) {
	t.Helper()
	f := accessAPI(t, true)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"s1": "q1", "s2": "q2", "s3": "q3"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	add := func(title, body string) {
		id, err := f.st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: body, Scope: scope.Axes{Project: accProject}, Confidence: "trusted", Person: "mia"})
		if err != nil {
			t.Fatal(err)
		}
		ids[title] = id
	}
	add("hit one", "q1 q2 f1")
	add("hit two", "q1 q2 f2")
	for i := range 30 {
		add(fmt.Sprintf("near%02d", i), "s1 s2 s3")
	}
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
	f.st.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: embed.DefaultMinSimilarity})
	return f, ids
}

func usedTitles(t *testing.T, f *accessAPIFixture, ids map[string]int64) []string {
	t.Helper()
	var used []string
	for title, id := range ids {
		hits, _, err := f.st.KnowledgeUsage(id)
		if err != nil {
			t.Fatal(err)
		}
		if hits > 0 {
			used = append(used, title)
		}
	}
	return used
}

func knowledgeHits(t *testing.T, f *accessAPIFixture, path string) []store.Knowledge {
	t.Helper()
	var out struct {
		Knowledge []store.Knowledge `json:"knowledge"`
	}
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "GET", path, nil)), &out); err != nil {
		t.Fatal(err)
	}
	return out.Knowledge
}

// What a search counts as used is what it delivers, once. The API fetches more
// than the limit when access is enforced, to have enough left after the filters;
// the rest, and the neighbours that never made it into the answer, are not use.
func TestSearchCountsOnlyTheHitsItDelivers(t *testing.T) {
	f, ids := usageWorld(t)
	got := knowledgeHits(t, f, "/api/search?kind=knowledge&q=q1+q2&limit=20")
	if len(got) != 2+5 {
		t.Fatalf("a search with two word hits and thirty neighbours returned %d entries, want 7", len(got))
	}
	used := usedTitles(t, f, ids)
	if len(used) != len(got) {
		t.Fatalf("%d entries counted as used, %d delivered: %v", len(used), len(got), used)
	}
	for _, k := range got {
		if hits, _, _ := f.st.KnowledgeUsage(k.ID); hits != 1 {
			t.Fatalf("%q was delivered and counted %d times", k.Title, hits)
		}
	}
	// A second search counts the same entries once more.
	knowledgeHits(t, f, "/api/search?kind=knowledge&q=q1+q2&limit=20")
	for _, k := range got {
		if hits, _, _ := f.st.KnowledgeUsage(k.ID); hits != 2 {
			t.Fatalf("%q counted %d times after two searches", k.Title, hits)
		}
	}
}

// The same holds without an embedding server, where before only the full-text
// hits were fetched and counted beyond the limit.
func TestFullTextSearchCountsOnlyTheHitsItDelivers(t *testing.T) {
	f, ids := usageWorld(t)
	f.st.SetEmbedder(store.EmbedSearchConfig{})
	got := knowledgeHits(t, f, "/api/search?kind=knowledge&q=q1+q2&limit=1")
	if len(got) != 1 {
		t.Fatalf("limit 1 returned %d", len(got))
	}
	if used := usedTitles(t, f, ids); len(used) != 1 || used[0] != got[0].Title {
		t.Fatalf("counted %v, delivered %q", used, got[0].Title)
	}
}
