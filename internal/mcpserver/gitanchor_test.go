package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const (
	anchorProject = "github.com/x/anchored"
	anchorCommitA = "aaaaaaa1111111111111111111111111111111aa"
	anchorCommitB = "bbbbbbb2222222222222222222222222222222bb"
)

func TestSearchFiltersByCommitAndShowsTheShortCommit(t *testing.T) {
	c, st := newTestClient(t)
	p := scope.Axes{Project: anchorProject}
	for title, a := range map[string]*gitanchor.Anchor{
		"alpha": {Commit: anchorCommitA, Branch: "main"},
		"beta":  {Commit: anchorCommitB, Branch: "feat/b", Dirty: true},
		"gamma": nil,
	} {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "gemeinsames Stichwort " + title, Scope: p, Person: "robin", GitAnchor: a}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: p}
	search := func(in SearchInput) string {
		t.Helper()
		in.Query = "stichwort"
		in.Kind = "knowledge"
		res, _, err := s.handleSearch(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		return text(t, res)
	}
	got := search(SearchInput{Commit: anchorCommitA[:7]})
	if !strings.Contains(got, "alpha") || strings.Contains(got, "beta") || strings.Contains(got, "gamma") {
		t.Fatalf("commit filter: %s", got)
	}
	if !strings.Contains(got, "at:aaaaaaa11111@main") || strings.Contains(got, anchorCommitA) {
		t.Fatalf("a hit names the anchor with the short commit: %s", got)
	}
	if got := search(SearchInput{Commit: anchorCommitB[:8]}); !strings.Contains(got, "at:bbbbbbb22222+dirty@feat/b") {
		t.Fatalf("dirty marker: %s", got)
	}
	// Ohne Filter sieht man alles, und ein Eintrag ohne Anker bleibt unmarkiert.
	if got := search(SearchInput{}); !strings.Contains(got, "gamma") || !strings.Contains(got, "alpha") {
		t.Fatalf("no filter: %s", got)
	}
	if got := search(SearchInput{PR: 3}); strings.Contains(got, "alpha") {
		t.Fatalf("pr filter finds only entries with a pr: %s", got)
	}
	for _, bad := range []string{"abc", "nothex1", "ABC12"} {
		_, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "x", Commit: bad})
		if err == nil || !strings.Contains(err.Error(), "hexadecimal") {
			t.Errorf("commit %q must be refused, got %v", bad, err)
		}
	}
}

// branch liest wie ein Branch, der von dev abzweigt: er sieht dev mit. Die
// Vorfahren kommen aus dem lokalen Repo; ohne sie bleibt es beim Branch selbst.
func TestSearchOnAnotherBranchReadsItsAncestors(t *testing.T) {
	c, st := newTestClient(t)
	for branch, title := range map[string]string{"dev": "devwissen", "feat/y": "featwissen", "feat/z": "geschwister"} {
		if _, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: title, Body: "gemeinsames Stichwort",
			Scope: scope.Axes{Project: anchorProject, Branch: branch}, Person: "robin"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: anchorProject, Branch: "main"}}
	search := func() string {
		t.Helper()
		res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "stichwort", Kind: "knowledge", Branch: "feat/y"})
		if err != nil {
			t.Fatal(err)
		}
		return text(t, res)
	}
	got := search()
	if !strings.Contains(got, "featwissen") || strings.Contains(got, "devwissen") {
		t.Fatalf("without a resolver only the branch itself: %s", got)
	}
	s.SetLineageResolver(func(branch string) []string {
		if branch != "feat/y" {
			t.Errorf("resolver asked for %q", branch)
		}
		return []string{"dev"}
	})
	got = search()
	if !strings.Contains(got, "featwissen") || !strings.Contains(got, "devwissen") || strings.Contains(got, "geschwister") {
		t.Fatalf("branch with ancestors must see dev but not a sibling: %s", got)
	}
}
