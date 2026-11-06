package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The search page finds an entry by meaning, and the meaning of an entry of a
// project the viewer has no role in finds nothing for them.
func TestSearchPageFindsByMeaningAndNeverReachesAHiddenProject(t *testing.T) {
	e := ovEnv(t)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	knInsert(t, e.St, store.Knowledge{Title: "Wagen kaputt", Body: "der wagen ist kaputt", Confidence: "trusted"})
	knInsert(t, e.St, store.Knowledge{Title: "Wagen SECRET kaputt", Body: "der wagen ist kaputt", Confidence: "trusted", Scope: scope.Axes{Project: shellHiddenProject}})
	job := embedjob.New(e.St, c, embedjob.Config{Batch: 16, StartDelay: -1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	const path = "/ui/search?q=auto+defekt"
	_, before := fetchPage(t, e.Owner, e.Base+path)
	if strings.Contains(before, "Wagen kaputt") {
		t.Fatalf("without embeddings the words of the query do not match the entry:\n%s", before)
	}

	e.St.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2})
	for name, client := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		code, page := fetchPage(t, client, e.Base+path)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", name, code)
		}
		if !strings.Contains(page, "Wagen kaputt") {
			t.Errorf("%s: the entry is not found by its meaning:\n%s", name, page)
		}
		if hidden := strings.Contains(page, "SECRET"); hidden != (name == "owner") {
			t.Errorf("%s: sees the hidden project's entry = %v", name, hidden)
		}
	}

	// The same page when the embedding server is gone: the plain answer, no error.
	fake.Fail.Store(true)
	code, page := fetchPage(t, e.Member, e.Base+path)
	if code != http.StatusOK || strings.Contains(page, "Wagen kaputt") {
		t.Fatalf("fallback: status %d\n%s", code, page)
	}
}
