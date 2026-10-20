package mcpserver

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A hit that only the embedding found says so in the label, so a reader can
// weigh it differently from a hit that contains the words of the query. A hit
// of the full-text index reads as it always did.
func TestRenderKnowledgeMarksSemanticHitsAndLeavesTheRestAlone(t *testing.T) {
	k := store.Knowledge{ID: 7, Type: "pitfall", Title: "Title", Body: "body", Confidence: "trusted", Status: "active", Origin: "agent"}
	plain := renderKnowledge(k)
	if strings.Contains(plain, "match") {
		t.Fatalf("a full-text hit must not carry a marker: %s", plain)
	}
	k.Match = store.MatchSemantic
	marked := renderKnowledge(k)
	if !strings.Contains(marked, "|match:semantic]") {
		t.Fatalf("a semantic hit must say so: %s", marked)
	}
	if strings.Replace(marked, "|match:semantic", "", 1) != plain {
		t.Fatalf("the marker is the only difference:\n%s%s", plain, marked)
	}
}
