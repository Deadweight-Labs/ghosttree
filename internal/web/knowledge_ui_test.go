package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestContextLinesNumberHeadingsAndMarkWhatOnlyThePreviewHas(t *testing.T) {
	delivered := "## Pitfalls\n- one\n"
	preview := "## Pitfalls\n- one\n- two\n\n## Preview\n"
	got := contextLines(preview, delivered)
	if len(got) != 5 {
		t.Fatalf("lines = %+v", got)
	}
	for i, want := range []struct{ head, added bool }{{true, false}, {false, false}, {false, true}, {false, false}, {true, true}} {
		if got[i].N != i+1 || got[i].Head != want.head || got[i].Added != want.added {
			t.Errorf("line %d = %+v, want head=%v added=%v", i+1, got[i], want.head, want.added)
		}
	}
	// Without a delivered text nothing is marked as added.
	for _, l := range contextLines(preview, "") {
		if l.Added {
			t.Errorf("line %q marked added without a reference", l.Text)
		}
	}
}

func TestKnowledgeListFiltersByAuthorAndKeepsAnEmptyTypeTabOpen(t *testing.T) {
	e := ovEnv(t)
	knInsert(t, e.St, store.Knowledge{Title: "Store lesson", Person: "claude", Confidence: "trusted"})
	knInsert(t, e.St, store.Knowledge{Title: "Web lesson", Person: "robin", Confidence: "trusted"})
	_, page := fetchPage(t, e.Member, e.Base+"/ui/knowledge")
	for _, want := range []string{`class="lb neu"`, "Any author", ">claude<", ">robin<"} {
		if !strings.Contains(page, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	_, byAuthor := fetchPage(t, e.Member, e.Base+"/ui/knowledge?author=robin")
	if !strings.Contains(byAuthor, "Web lesson") || strings.Contains(byAuthor, "Store lesson") {
		t.Error("the author filter does not narrow the list")
	}
	_, byType := fetchPage(t, e.Member, e.Base+"/ui/knowledge?type=plan")
	if !strings.Contains(byType, `<a class="tab on"`) || strings.Contains(byType, "Store lesson") {
		t.Error("an empty type tab hides or shows the wrong entries")
	}
}

func TestReviewSourceTabsSplitGardenerAndAgents(t *testing.T) {
	e, _, _ := gardenReviewEnv(t)
	_, all := fetchPage(t, e.Lead, e.Base+"/ui/review")
	for _, want := range []string{"From the gardener", "From agents", "Roles table docs", "Agent proposal"} {
		if !strings.Contains(all, want) {
			t.Errorf("review lacks %q", want)
		}
	}
	_, agents := fetchPage(t, e.Lead, e.Base+"/ui/review?source=agents")
	if strings.Contains(agents, "Roles table docs") || !strings.Contains(agents, "Agent proposal") {
		t.Error("the agents tab does not narrow the list")
	}
	if strings.Contains(agents, "/ui/review/bulk") {
		t.Error("the bulk buttons belong to the gardener's notes")
	}
}

func TestEntryPageStandsInTheBandOfItsProjectAndListsRevisionsAndTimeline(t *testing.T) {
	e := ovEnv(t)
	id := knInsert(t, e.St, store.Knowledge{Title: "First", Body: "first body", Person: "alice", Confidence: "trusted"})
	if err := e.St.UpdateKnowledgeBy(id, map[string]string{"title": "Second", "body": "second body"}, "lars"); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/knowledge/"+strconv.FormatInt(id, 10))
	for _, want := range []string{`class="rband`, `aria-current="page"`, `id="revisions"`, "Revision 2", "Revision 1", "Timeline", "edited the entry", "created the entry"} {
		if !strings.Contains(page, want) {
			t.Errorf("entry page lacks %q", want)
		}
	}
}
