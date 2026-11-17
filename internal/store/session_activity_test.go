package store

import "testing"

// REQ-688 (N10): the title of a session is the first request somebody typed, not the body of
// a skill and not an "Ok"; and the tool calls of a run are counted by kind.
func TestSessionTitleIsTheFirstTypedRequestAndItsToolCallsAreCounted(t *testing.T) {
	st := orgStore(t, "robin")
	toolCall := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
	}
	s := addSession(t, st, "run", 1, "", "m", "", chunksOf(
		userLine("2026-10-01T10:00:00Z", "Ok"),
		userLine("2026-10-01T10:00:01Z", "Base directory for this skill: /home/x/.claude/skills/ghost\n\n# Ghost\nShort commands between agent and human"),
		userLine("2026-10-01T10:00:02Z", "Stelle mir als Mensch eine Aufgabe in ghosttree, mir ein Token zu besorgen"),
		assistantLine("2026-10-01T10:00:03Z", toolCall("a", "Bash", map[string]any{"command": "ls"}), toolCall("b", "Read", map[string]any{"file_path": "/x/y.go"})),
		assistantLine("2026-10-01T10:00:04Z", toolCall("c", "mcp__ghosttree__human_task", map[string]any{"action": "create"}), toolCall("d", "Edit", map[string]any{"file_path": "/x/y.go", "old_string": "a", "new_string": "b"})),
	))
	if s.Title != "Stelle mir als Mensch eine Aufgabe in ghosttree, mir ein Token zu besorgen" {
		got, _ := st.SessionByID(s.ID)
		if got.Title != "Stelle mir als Mensch eine Aufgabe in ghosttree, mir ein Token zu besorgen" {
			t.Errorf("title = %q, want the first typed request", got.Title)
		}
	}
	act := st.SessionActivity(s)
	if act.Total != 4 || !act.Detailed || act.Commands != 1 || act.Reads != 1 || act.Ghost != 1 || act.Edits != 1 {
		t.Errorf("activity = %+v", act)
	}
	if empty := st.SessionActivity(addSession(t, st, "quiet", 1, "", "m", "", chunksOf(userLine("2026-10-01T10:00:00Z", "Hallo")))); empty.Total != 0 {
		t.Errorf("a run without tool calls counts %d", empty.Total)
	}
}
