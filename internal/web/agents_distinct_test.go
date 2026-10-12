package web

import (
	"strings"
	"testing"
)

// Three MCP processes of one person on one machine read "Agent · GT Test 2 ·
// gt-fresh" three times. Rows that would read the same get a short tag taken
// from the agent's own id, so they can be told apart (acceptance finding 9).
func TestAgentRowsWithTheSameNameGetAShortTag(t *testing.T) {
	rows := []agentRow{
		{Name: "Agent · GT Test 2 · gt-fresh", ExternalID: "derived:gt-fresh:4821"},
		{Name: "Agent · GT Test 2 · gt-fresh", ExternalID: "derived:gt-fresh:4822"},
		{Name: "Agent · GT Test 2 · gt-fresh", ExternalID: "derived:gt-fresh:4823"},
		{Name: "Claude · Robin · mainex", ExternalID: "claude:mainex:0a1b2c3d-0000-4000-8000-000000000001"},
	}
	tellAgentRowsApart(rows)
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Name] {
			t.Errorf("two rows read %q", r.Name)
		}
		seen[r.Name] = true
	}
	if rows[3].Name != "Claude · Robin · mainex" {
		t.Errorf("a unique name was changed: %q", rows[3].Name)
	}
	if !strings.HasSuffix(rows[0].Name, "#4821") || strings.Contains(rows[0].Name, "derived") {
		t.Errorf("tag %q: want the short session name, not the raw id", rows[0].Name)
	}
}

// The tag never leaks more of an id than its tail, and it still tells apart
// agents whose ids end the same way.
func TestAgentRowTagsStayDistinctWhenIdsEndAlike(t *testing.T) {
	rows := []agentRow{
		{Name: "Agent · A", ExternalID: "derived:box:1111"},
		{Name: "Agent · A", ExternalID: "other:box:1111"},
		{Name: "Agent · A", ExternalID: ""},
	}
	tellAgentRowsApart(rows)
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Name] {
			t.Errorf("two rows read %q: %+v", r.Name, rows)
		}
		seen[r.Name] = true
	}
}
