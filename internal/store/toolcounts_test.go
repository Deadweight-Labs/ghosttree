package store

import (
	"testing"
	"time"
)

// REQ-436, criterion 12: calls are counted per session, tool, action, harness
// and subagent, and the statistics name the sessions that called nothing.
func TestToolUseStatsCountCallsAndSilentSessions(t *testing.T) {
	st := originStore(t)
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	seen := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	actorSession(t, st, "claude", "s-busy", 1, "laptop", start, seen)
	actorSession(t, st, "claude", "s-silent", 1, "laptop", start, seen)
	actorSession(t, st, "codex", "s-codex", 1, "laptop", start, seen)
	// A session that ended before counting began is not counted as silent.
	actorSession(t, st, "claude", "s-old", 1, "laptop", start, start)

	if err := st.RecordToolUses(ToolUseBatch{Project: actorProject, SessionRef: "s-busy", Harness: "claude", Uses: []ToolUse{
		{Tool: "context_search", Action: "search", Calls: 2},
		{Tool: "context_remember", Action: "remember", Subagent: true, Calls: 1},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	// A second report adds to the first.
	if err := st.RecordToolUses(ToolUseBatch{Project: actorProject, SessionRef: "s-busy", Harness: "claude", Uses: []ToolUse{
		{Tool: "context_search", Action: "search", Calls: 3},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordToolUses(ToolUseBatch{Project: actorProject, SessionRef: "s-codex", Harness: "codex", Uses: []ToolUse{
		{Tool: "request", Action: "get", Calls: 1},
	}}, 1); err != nil {
		t.Fatal(err)
	}

	rep, err := st.ToolUseStats([]string{actorProject}, 30)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Since == "" || rep.Sessions != 3 || rep.Silent != 1 {
		t.Fatalf("sessions %d, silent %d, since %q; want 3, 1 and a start", rep.Sessions, rep.Silent, rep.Since)
	}
	first := rep.Lines[0]
	if first.Tool != "context_search" || first.Calls != 5 || first.Sessions != 1 || first.Subagent {
		t.Fatalf("first line %+v", first)
	}
	var sub bool
	for _, l := range rep.Lines {
		if l.Tool == "context_remember" && l.Subagent && l.Calls == 1 {
			sub = true
		}
	}
	if !sub {
		t.Fatalf("subagent call missing: %+v", rep.Lines)
	}
	for _, h := range rep.Harness {
		if h.Harness == "claude" && (h.Sessions != 2 || h.WithCalls != 1) {
			t.Fatalf("claude: %+v", h)
		}
	}

	// Another project's viewer sees nothing of these.
	if other, err := st.ToolUseStats([]string{"github.com/other/repo"}, 30); err != nil || other.Since != "" || len(other.Lines) != 0 {
		t.Fatalf("other project: %+v %v", other, err)
	}
}

func TestToolUseBatchRejectsWhatIsNotACount(t *testing.T) {
	for name, b := range map[string]ToolUseBatch{
		"no project":   {SessionRef: "s", Uses: []ToolUse{{Tool: "x", Calls: 1}}},
		"no uses":      {Project: "p", SessionRef: "s"},
		"zero calls":   {Project: "p", SessionRef: "s", Uses: []ToolUse{{Tool: "x"}}},
		"no tool name": {Project: "p", SessionRef: "s", Uses: []ToolUse{{Calls: 1}}},
	} {
		if b.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
