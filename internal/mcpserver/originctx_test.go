package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestSearchFiltersByTimeAndAreaAndShowsDateAndMainArea(t *testing.T) {
	c, st := newTestClient(t)
	p := scope.Axes{Project: "github.com/x/origins"}
	ctxAt := func(at, area string) *originctx.Context {
		return &originctx.Context{SessionRef: "s", CapturedAt: at, Areas: []originctx.Area{{Path: area, Weight: 3}}}
	}
	recent := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	for title, oc := range map[string]*originctx.Context{
		"alpha": ctxAt(recent, "internal/store"),
		"beta":  ctxAt(recent, "internal/web"),
		"gamma": ctxAt("2026-01-05T10:00:00Z", "internal/store"),
		"delta": nil,
	} {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "gemeinsames Stichwort " + title, Scope: p, Person: "robin", OriginContext: oc}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: p}
	search := func(in SearchInput) string {
		t.Helper()
		in.Query, in.Kind = "stichwort", "knowledge"
		res, _, err := s.handleSearch(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		return text(t, res)
	}
	got := search(SearchInput{Area: []string{"internal/store"}, Since: "14d"})
	if !strings.Contains(got, "alpha") || strings.Contains(got, "beta") || strings.Contains(got, "gamma") || strings.Contains(got, "delta") {
		t.Fatalf("store, last 14 days: %s", got)
	}
	if want := "|on:" + recent[:10] + "|in:internal/store"; !strings.Contains(got, want) {
		t.Fatalf("a hit shows date and main area %q: %s", want, got)
	}
	if got := search(SearchInput{Area: []string{"internal/store", "internal/web"}}); !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") || !strings.Contains(got, "gamma") {
		t.Fatalf("two areas: %s", got)
	}
	if got := search(SearchInput{Until: "2026-02-01"}); !strings.Contains(got, "gamma") || strings.Contains(got, "alpha") {
		t.Fatalf("until: %s", got)
	}
	if got := search(SearchInput{}); !strings.Contains(got, "delta") {
		t.Fatalf("no filter: %s", got)
	}
	// Ein Eintrag ohne Kontext zeigt trotzdem sein Datum.
	if !strings.Contains(search(SearchInput{}), "|on:"+time.Now().UTC().Format("2006-01-02")) {
		t.Fatal("an entry without a context still shows its creation date")
	}
	for _, in := range []SearchInput{{Since: "gestern"}, {Until: "7x"}, {Area: []string{"/etc"}}} {
		in.Query = "x"
		if _, _, err := s.handleSearch(context.Background(), nil, in); err == nil {
			t.Errorf("%+v must be refused", in)
		}
	}
}
