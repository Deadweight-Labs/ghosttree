package mcpserver

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A hit that only the meaning of the query found says so, in the file hits and the
// request list; a hit of the words does not.
func TestSearchHitsSayWhenOnlyTheMeaningMatched(t *testing.T) {
	plain := renderGhostHit(store.GhostFile{Path: "a.go", Kind: "file", Description: "macht etwas"})
	semantic := renderGhostHit(store.GhostFile{Path: "b.go", Kind: "file", Description: "macht etwas", Match: store.MatchSemantic})
	if !strings.Contains(plain, "[file]") || strings.Contains(plain, "match") {
		t.Fatalf("%q", plain)
	}
	if !strings.Contains(semantic, "[file|match:semantic]") {
		t.Fatalf("%q", semantic)
	}

	out := listFromPage(requestdomain.SearchPage{Results: []requestdomain.SearchHit{
		{Request: requestdomain.Request{ID: 1, Title: "by words"}},
		{Request: requestdomain.Request{ID: 2, Title: "by meaning"}, Match: store.MatchSemantic, MatchReason: "semantic match"},
	}, NextCursor: "~2"})
	if len(out.Results) != 2 || out.Results[0].Match != "" || out.Results[1].Match != store.MatchSemantic || out.NextCursor != "~2" {
		t.Fatalf("%+v", out)
	}
}
