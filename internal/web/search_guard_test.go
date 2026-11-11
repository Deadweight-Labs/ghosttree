package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A search of an account ends its streams that still run; a kind loaded again
// ends only the earlier load of the same kind. Other accounts are not touched.
func TestSearchStreamsOneSearchPerAccount(t *testing.T) {
	var s searchStreams
	ended := map[string]bool{}
	cancel := func(name string) context.CancelFunc { return func() { ended[name] = true } }
	s.start("person:1", "", cancel("search1"))
	s.start("person:2", "", cancel("other"))
	s.start("person:1", "sessions", cancel("more1"))
	s.start("person:1", "knowledge", cancel("moreK"))
	if len(ended) != 0 {
		t.Fatalf("ended too early: %v", ended)
	}
	s.start("person:1", "sessions", cancel("more2"))
	if !ended["more1"] || ended["search1"] || ended["moreK"] {
		t.Fatalf("loading a kind again: %v", ended)
	}
	done := s.start("person:1", "", cancel("search2"))
	for _, name := range []string{"search1", "more2", "moreK"} {
		if !ended[name] {
			t.Errorf("a new search leaves %s running", name)
		}
	}
	if ended["other"] || ended["search2"] {
		t.Errorf("ended what it should not: %v", ended)
	}
	done()
	if _, ok := s.m["person:1\x00"]; ok {
		t.Error("a finished stream stays registered")
	}
}

// The kinds of all searches together hold one read connection less than the
// store has; a kind that finds no free place gives up when its context ends.
func TestSearchKindsLeaveAReadConnectionFree(t *testing.T) {
	st, err := store.OpenWithOptions(t.TempDir()+"/pool.db", store.OpenOptions{MaxOpenConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var l searchLimiter
	var releases []func()
	for range 2 {
		release, ok := l.acquire(context.Background(), st)
		if !ok {
			t.Fatal("no place for the first two kinds")
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := l.acquire(ctx, st); ok {
		t.Fatal("a third kind got a place with three connections")
	}
	releases[0]()
	if release, ok := l.acquire(context.Background(), st); !ok {
		t.Fatal("a freed place is not given again")
	} else {
		release()
	}
}

// A kind that panics gives an empty group and a log line; the others and the
// server go on.
func TestSearchKindSurvivesAPanic(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var logs bytes.Buffer
	a := &app{store: st, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	// raw shorter than lines: reading the line of the first kind panics.
	p := &searchPlan{l: langEN, lines: []searchLine{{text: "zebra"}}, href: func(string) string { return "" }}
	r := httptest.NewRequest(http.MethodGet, "/ui/search/stream?q=zebra", nil)
	g := a.searchKind(context.Background(), r, p, "requests")
	if g.Key != "requests" || len(g.Hits) != 0 {
		t.Fatalf("after a panic: %+v", g)
	}
	if !strings.Contains(logs.String(), `"msg":"search_panic"`) || !strings.Contains(logs.String(), `"kind":"requests"`) {
		t.Errorf("the panic is not logged: %s", logs.String())
	}
	// The place of the kind is free again.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if release, ok := a.searchSlots.acquire(ctx, st); !ok {
		t.Error("the kind that panicked kept its place")
	} else {
		release()
	}
}
