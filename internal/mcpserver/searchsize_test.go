package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Listing the backlog is the first thing a fresh agent does, and it failed:
// every hit carried its full description, so twenty-four requests came to
// 64,462 characters and blew the tool-result limit. The descriptions are long
// because they are good — problem, evidence, trade-off, approach — so the answer
// cannot be "write less".
func TestRequestSearchStaysSmallEnoughToRead(t *testing.T) {
	c, st := newTestClient(t)
	for i := range 30 {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "bug", Title: fmt.Sprintf("Befund %d", i), Priority: "hoch",
			Description: strings.Repeat("Ausführliche Begründung mit Beleg und Abwägung. ", 60),
			Scope:       scope.Axes{Project: "github.com/x/y"},
		}, Criteria: []string{"a", "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	res, _, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if len(got) > 8000 {
		t.Errorf("listing 25 requests produced %d characters, want a compact list", len(got))
	}
	// Compact must still be enough to choose from. The newest page is returned,
	// so the assertion names an entry that is on it.
	for _, want := range []string{"Befund 29", "bug", "hoch", "open"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from the list: %.400s", want, got)
		}
	}
	// The description belongs to the detail view, not the list.
	if strings.Contains(got, "Ausführliche Begründung") {
		t.Errorf("descriptions leaked into the list")
	}
}

// A question that names nothing must show the backlog rather than the one entry
// that happens to contain all its words.
func TestNaturalQuestionListsTheBacklog(t *testing.T) {
	c, st := newTestClient(t)
	for i := range 6 {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "feature", Title: fmt.Sprintf("Sache %d", i), Scope: scope.Axes{Project: "github.com/x/y"},
			Description: "Etwas, das noch zu tun ist.",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	_, out, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "was ist noch zu tun", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 6 {
		t.Fatalf("results = %d, want all six open requests", len(out.Results))
	}
}

// Naming a subject must still narrow.
func TestNamedSubjectStillNarrows(t *testing.T) {
	c, st := newTestClient(t)
	for _, title := range []string{"Bootstrap zurückbauen", "Worktree-Projekte reparieren"} {
		if _, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{
			Type: "bug", Title: title, Scope: scope.Axes{Project: "github.com/x/y"}}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}
	_, out, err := s.handleRequestSearch(context.Background(), nil, RequestSearchInput{Query: "Worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || !strings.Contains(out.Results[0].Title, "Worktree") {
		t.Fatalf("results = %+v, want only the worktree request", out.Results)
	}
}
