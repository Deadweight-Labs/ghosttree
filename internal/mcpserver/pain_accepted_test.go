package mcpserver

import (
	"strings"
	"testing"
)

// REQ-616 über MCP: accepted verlangt eine Begründung, der Rückweg eine Notiz,
// und der Zustand erscheint in Suche und Karte.
func TestPainUpdateAcceptedNeedsAReasonOverTheWire(t *testing.T) {
	s, _, base, _ := linkWorld(t)
	wireText(t, s, "pain_record", map[string]any{"title": "Es hakt manchmal"})
	session := connect(t, s)

	for _, args := range []map[string]any{
		{"id": 1, "state": "accepted"},
		{"id": 1, "state": "accepted", "note": "   "},
	} {
		got, failed := callTool(t, session, "pain_update", args)
		if !failed || !strings.Contains(got, "accepted") || !strings.Contains(got, "why") {
			t.Errorf("accepting without a reason must be refused: %v %s", failed, got)
		}
	}
	got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "accepted", "note": "kostet mehr als es bringt"})
	if failed {
		t.Fatalf("accept: %s", got)
	}
	mustHave(t, "pain_update accepted", got, `"state":"accepted"`, `"accepted_reason":"kostet mehr als es bringt"`,
		"Pain PAIN-1", "state: accepted", "link: "+base+"/pains/1")

	// Die Suche kennt den Zustand; die offene Liste zeigt ihn nicht.
	got = wireText(t, s, "pain_search", map[string]any{"state": "accepted"})
	mustHave(t, "pain_search accepted", got, `"state":"accepted"`, `"accepted_reason":"kostet mehr als es bringt"`)
	if got = wireText(t, s, "pain_search", map[string]any{}); strings.Contains(got, "PAIN-1") {
		t.Errorf("the open list shows an accepted pain: %s", got)
	}
	// Als geschlossen in der Suchsprache.
	got = wireText(t, s, "context_search", map[string]any{"query": "is:pain is:closed"})
	mustHave(t, "context_search is:closed", got, "PAIN-1")
	if got = wireText(t, s, "context_search", map[string]any{"query": "is:pain is:open"}); strings.Contains(got, "PAIN-1") {
		t.Errorf("is:open finds an accepted pain: %s", got)
	}

	// Rückweg: nach active mit Notiz.
	if got, failed := callTool(t, session, "pain_update", map[string]any{"id": 1, "state": "active"}); !failed || !strings.Contains(got, "note_required") {
		t.Errorf("back to active without a note: %v %s", failed, got)
	}
	got = wireText(t, s, "pain_update", map[string]any{"id": 1, "state": "active", "note": "stört doch"})
	mustHave(t, "pain_update active", got, `"state":"active"`)
	if strings.Contains(got, "accepted_reason") {
		t.Errorf("the reason stays after the way back: %s", got)
	}
	// Der Grund darf auch als reason kommen.
	got = wireText(t, s, "pain_update", map[string]any{"id": 1, "state": "accepted", "reason": "bewusst"})
	mustHave(t, "pain_update accepted with reason", got, `"accepted_reason":"bewusst"`)
}
