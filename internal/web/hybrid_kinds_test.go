package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
	"github.com/Deadweight-Labs/ghosttree/internal/embedjob"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The search page and the page of requests find a request by meaning, never reach
// one of a project the viewer has no role in, and page through a ranked search.
func TestRequestPagesFindByMeaningNeverReachAHiddenProjectAndPage(t *testing.T) {
	e := ovEnv(t)
	fake := embedtest.New(t)
	fake.Dim = 256
	fake.Concepts = map[string]string{"wagen": "auto", "kaputt": "defekt"}
	c, err := embed.New(embed.Config{URL: fake.URL})
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, description, project string) {
		t.Helper()
		if _, err := e.St.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Description: description, Scope: scope.Axes{Project: project}, Person: "anna"}}); err != nil {
			t.Fatal(err)
		}
	}
	add("Wagen SECRET kaputt", "der wagen ist kaputt", shellHiddenProject)
	for i := range 25 {
		add(fmt.Sprintf("Wagen Nr %02d", i), "der wagen ist kaputt", shellProject)
	}
	job := embedjob.New(e.St, c, embedjob.Config{Batch: 16, StartDelay: -1, Kinds: []string{store.KindRequest}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for {
		res, err := job.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res == (embedjob.Result{}) {
			break
		}
	}
	const search = "/ui/search?q=auto+defekt"
	const list = "/ui/requests?state=all&q=auto+defekt"
	_, before := fetchPage(t, e.Owner, e.Base+search)
	if strings.Contains(before, "Wagen Nr") {
		t.Fatalf("without embeddings the words of the query do not match a request:\n%s", before)
	}

	e.St.SetEmbedder(store.EmbedSearchConfig{Embedder: c, MinSimilarity: 0.2, Kinds: []string{store.KindRequest}})
	for name, client := range map[string]*http.Client{"owner": e.Owner, "member": e.Member, "guest": e.Guest} {
		for _, path := range []string{search, list} {
			code, page := fetchPage(t, client, e.Base+path)
			if code != http.StatusOK {
				t.Fatalf("%s %s: status %d", name, path, code)
			}
			if !strings.Contains(page, "Wagen Nr") {
				t.Errorf("%s %s: no request is found by its meaning:\n%s", name, path, page)
			}
			if hidden := strings.Contains(page, "SECRET"); hidden != (name == "owner") {
				t.Errorf("%s %s: sees the hidden project's request = %v", name, path, hidden)
			}
		}
	}

	// The request page shows at most five requests found by meaning alone, and its
	// "older" link, which carries a position, is followed.
	_, page := fetchPage(t, e.Member, e.Base+list)
	if got := strings.Count(page, `href="/ui/requests/`); got == 0 || got > maxSemanticRows {
		t.Fatalf("%d requests on a page of a search that found them by meaning alone", got)
	}

	// The same pages when the embedding server is gone: the plain answer, no error.
	fake.Fail.Store(true)
	code, page := fetchPage(t, e.Member, e.Base+list)
	if code != http.StatusOK || strings.Contains(page, "Wagen Nr") {
		t.Fatalf("fallback: status %d\n%s", code, page)
	}
	// A cursor of a ranked search that reaches a page that cannot rank is not
	// thrown away as an invalid id: it is a position.
	if code, _ := fetchPage(t, e.Member, e.Base+"/ui/requests?state=all&q=auto+defekt&cursor=~3"); code != http.StatusOK {
		t.Fatalf("status %d for a ranked cursor", code)
	}
}

// maxSemanticRows is the most requests of a search that the page may show when
// only the meaning found them.
const maxSemanticRows = 5
