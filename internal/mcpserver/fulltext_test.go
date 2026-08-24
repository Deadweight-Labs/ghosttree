package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// longSpec is the shape the archive actually holds: headings, prose, a bullet
// list and a fenced block, well past eight thousand characters.
func longSpec() string {
	var b strings.Builder
	b.WriteString("# Ziel und Abgrenzung\n\nDas Datenmodell und die Infrastruktur.\n\n")
	b.WriteString("# Teil 1: Schemaänderungen\n\n- knowledge.confidence: CHECK neu\n- knowledge.origin: NEU\n- knowledge.superseded_by: NEU\n\n")
	b.WriteString("```sql\nSELECT id, title FROM knowledge WHERE status = 'active';\n```\n\n")
	b.WriteString("# Teil 2: Der Tabellenneubau\n\n")
	for range 120 {
		b.WriteString("SQLite kann einen CHECK-Constraint nicht per ALTER ändern, also muss die Tabelle neu gebaut werden.\n")
	}
	return b.String()
}

func storeSpec(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.InsertKnowledge(store.Knowledge{
		Type: "plan", Title: "Spec Distiller Stück 1", Body: longSpec(),
		Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTargetedRetrievalReturnsTheEntryVerbatim(t *testing.T) {
	c, st := newTestClient(t)
	id := storeSpec(t, st)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{KnowledgeID: id})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if !strings.Contains(got, longSpec()) {
		t.Fatal("the body did not come back verbatim")
	}
	for _, want := range []string{"# Teil 1: Schemaänderungen", "- knowledge.origin: NEU", "```sql"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing: line breaks, lists or code blocks were flattened", want)
		}
	}
	if strings.Count(got, "\n") < 100 {
		t.Errorf("result has %d line breaks, want the structure the entry was written with", strings.Count(got, "\n"))
	}
}

func TestTargetedRetrievalDoesNotCutALongEntry(t *testing.T) {
	c, st := newTestClient(t)
	id := storeSpec(t, st)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{KnowledgeID: id})
	if err != nil {
		t.Fatal(err)
	}
	body := longSpec()
	if len(body) < 8000 {
		t.Fatalf("fixture is %d characters, the case only holds above 8000", len(body))
	}
	if got := text(t, res); len(got) < len(body) {
		t.Errorf("returned %d of %d characters", len(got), len(body))
	}
}

func TestSearchHitStaysCompactAndNamesTheWayToTheFullText(t *testing.T) {
	c, st := newTestClient(t)
	id := storeSpec(t, st)
	s := &Server{client: c, ctxAxes: scope.Axes{Project: "github.com/x/y"}}

	res, _, err := s.handleSearch(context.Background(), nil, SearchInput{Query: "Tabellenneubau", Kind: "knowledge"})
	if err != nil {
		t.Fatal(err)
	}
	got := text(t, res)
	if len(got) > 2000 {
		t.Errorf("one hit on an 8k entry produced %d characters, want a snippet", len(got))
	}
	if !strings.Contains(got, "Spec Distiller Stück 1") {
		t.Fatalf("the hit does not name its entry: %.300s", got)
	}
	// Compact is only honest if the way to the rest is on the page.
	if !strings.Contains(got, "knowledge_id") {
		t.Errorf("the hit does not say how to read the whole entry: %.400s", got)
	}
	if !strings.Contains(got, "#"+strconv.FormatInt(id, 10)) {
		t.Errorf("the hit does not carry the id needed to ask for it: %.400s", got)
	}
}

// The bootstrap has the opposite job: every character counts against a budget,
// and a list of titles must stay a list.
func TestBootstrapStillFoldsEntriesToOneLine(t *testing.T) {
	entries := []store.Knowledge{{
		Type: "pitfall", Title: "kurz", Body: "erste Zeile\n\n- zweite\n- dritte\n",
		Scope: scope.Axes{Project: "github.com/x/y"}, Confidence: "trusted", Status: "active",
	}}
	out := server.RenderBootstrap(entries, 12000)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "- [") && strings.Contains(line, "kurz") {
			if !strings.Contains(line, "erste Zeile - zweite - dritte") {
				t.Errorf("bootstrap line = %q, want the body folded into it", line)
			}
			return
		}
	}
	t.Fatalf("entry missing from the bootstrap: %s", out)
}
