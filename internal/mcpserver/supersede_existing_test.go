package mcpserver

import (
	"fmt"
	"strings"
	"testing"
)

func TestSupersedeExistingEntryAndReadTheReasonBack(t *testing.T) {
	c, st := targetFixture(t)
	session := connect(t, targetServer(c, tgtHere))
	remember := func(title string) int64 {
		t.Helper()
		if got, failed := callTool(t, session, "context_remember", map[string]any{"type": "note", "title": title, "body": "details of " + title, "scope_hint": "project"}); failed {
			t.Fatal(got)
		}
		return knowledgeNamed(t, st, title).ID
	}
	old, fresh := remember("share location created"), remember("share location removed again")

	if got, failed := callTool(t, session, "context_supersede", map[string]any{"id": fresh, "supersedes": []int64{old}, "reason": " "}); !failed || !strings.Contains(got, "reason") {
		t.Errorf("without a reason: %v %s", failed, got)
	}
	if got, failed := callTool(t, session, "context_supersede", map[string]any{"id": fresh, "supersedes": []int64{old}, "reason": "the share was removed"}); failed || !strings.Contains(got, fmt.Sprintf("#%d now supersedes #%d", fresh, old)) {
		t.Fatalf("supersede: %v %s", failed, got)
	}
	found, _ := callTool(t, session, "context_search", map[string]any{"query": "share location", "kind": "knowledge"})
	if strings.Contains(found, "created") || !strings.Contains(found, "removed again") {
		t.Errorf("search after supersede:\n%s", found)
	}
	full, _ := callTool(t, session, "context_search", map[string]any{"knowledge_id": old})
	if want := fmt.Sprintf("superseded by #%d", fresh); !strings.Contains(full, want) || !strings.Contains(full, "the share was removed") {
		t.Errorf("detail of the superseded entry lacks the reason:\n%s", full)
	}

	gone := remember("temporary tunnel open")
	if got, failed := callTool(t, session, "context_retire", map[string]any{"id": gone, "reason": "the tunnel was closed"}); failed {
		t.Fatal(got)
	}
	full, _ = callTool(t, session, "context_search", map[string]any{"knowledge_id": gone})
	if !strings.Contains(full, "retired") || !strings.Contains(full, "the tunnel was closed") {
		t.Errorf("detail of the retired entry lacks the reason:\n%s", full)
	}
}
