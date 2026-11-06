package web

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The pains group of the search page is one search over every project the viewer
// sees: the query is embedded once, and the hits that only the meaning found are
// counted for the group, so the neighbours of the projects that come first do not
// fill it before the pain that matches the words of a later project is reached.
func TestSearchPageAsksOnceForThePainsOfAllProjects(t *testing.T) {
	e := ovEnv(t)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	const early, late = "github.com/x/aaa", "github.com/x/zzz"
	for _, remote := range []string{early, late} {
		if _, err := e.St.ClaimProject("person:1", remote, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	add := func(project, title, body string) {
		t.Helper()
		if _, err := e.St.DB().Exec(`INSERT INTO pains(project,title,body,state,created_at,updated_at) VALUES(?,?,?,'active','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, project, title, body); err != nil {
			t.Fatal(err)
		}
	}
	// Neighbours by meaning in the projects that sort first, the one pain that
	// contains the words of the query in the project that sorts last.
	for _, project := range []string{early, shellHiddenProject, shellProject} {
		for _, n := range []string{"eins", "zwei", "drei", "vier", "fuenf"} {
			add(project, "Nachbar "+n+" "+project[strings.LastIndex(project, "/")+1:], "auto defekt")
		}
	}
	add(late, "Echter Treffer", "der wagen kaputt ist")
	job := embedjob.New(e.St, c, embedjob.Config{Batch: 32, StartDelay: -1, Kinds: []string{store.KindPain}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	e.St.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2, Kinds: []string{store.KindPain}})
	before := len(fake.Requests())

	code, page := fetchPage(t, e.Owner, e.Base+"/ui/search?q=wagen+kaputt")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	queries := 0
	for _, in := range fake.Requests()[before:] {
		for _, text := range in {
			if strings.HasPrefix(text, "task: search result | query: ") {
				queries++
			}
		}
	}
	if queries != 1 {
		t.Fatalf("the query was embedded %d times, once is enough for every project", queries)
	}
	if !strings.Contains(page, "Echter Treffer") {
		t.Fatalf("the pain that matches the words is not in the group:\n%s", page)
	}
	if neighbours := strings.Count(page, "Nachbar "); neighbours > 3 {
		t.Fatalf("%d neighbours in a group of five: the share is counted per project, not per group", neighbours)
	}
}
