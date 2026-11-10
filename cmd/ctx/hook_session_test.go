package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func turnEndOutput(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var out bytes.Buffer
	if code := cmdHookWith(bytes.NewReader(raw), []string{"turn-end", "--harness", "claude"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Stop hook output %q: %v", out.String(), err)
	}
	return got
}

// reminderText is the reminder a Stop hook output carries, "" for none.
func reminderText(got map[string]any) string {
	spec, _ := got["hookSpecificOutput"].(map[string]any)
	if spec == nil || spec["hookEventName"] != "Stop" {
		return ""
	}
	text, _ := spec["additionalContext"].(string)
	return text
}

func toolCall(t *testing.T, session, tool string, input map[string]any) map[string]any {
	t.Helper()
	p := registerPayload(t, session)
	p["tool_name"] = tool
	p["tool_input"] = input
	return p
}

// REQ-436, criterion 11: a session that changed files and recorded nothing gets
// one reminder at the end of a turn, naming what it changed.
func TestTurnEndRemindsOnceWhenFilesChangedAndNothingWasRecorded(t *testing.T) {
	registerEnv(t)
	const id = "sess-remind"
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Read", map[string]any{"file_path": "/repo/a.go"}))
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Edit", map[string]any{"file_path": "/repo/internal/store/b.go"}))
	got := turnEndOutput(t, registerPayload(t, id))
	reason := reminderText(got)
	if !strings.Contains(reason, "b.go") || !strings.Contains(reason, "context_remember") {
		t.Fatalf("no reminder after a change: %v", got)
	}
	if strings.Contains(reason, "a.go") {
		t.Fatalf("a file that was only read is named: %s", reason)
	}
	if again := turnEndOutput(t, registerPayload(t, id)); len(again) != 0 {
		t.Fatalf("the reminder came twice: %v", again)
	}
}

func TestTurnEndStaysSilentForReadOnlySessions(t *testing.T) {
	registerEnv(t)
	const id = "sess-reader"
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Read", map[string]any{"file_path": "/repo/a.go"}))
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "mcp__ghosttree__context_search", map[string]any{"query": "x"}))
	if got := turnEndOutput(t, registerPayload(t, id)); len(got) != 0 {
		t.Fatalf("a read-only session got a reminder: %v", got)
	}
}

func TestTurnEndStaysSilentWhenSomethingWasRecorded(t *testing.T) {
	for name, call := range map[string]struct {
		tool  string
		input map[string]any
	}{
		"remember":      {"mcp__ghosttree__context_remember", map[string]any{"type": "pitfall"}},
		"describe":      {"mcp__ghosttree__context_remember", map[string]any{"action": "describe_file"}},
		"criterion":     {"mcp__ghosttree__request", map[string]any{"action": "criterion_met"}},
		"pain":          {"mcp__ghosttree__pain", map[string]any{"action": "record"}},
		"old name":      {"mcp__ghosttree__request_record_progress", map[string]any{"action": "criterion_met"}},
		"by a subagent": {"mcp__ghosttree__pain_record", map[string]any{"title": "t"}},
	} {
		t.Run(name, func(t *testing.T) {
			registerEnv(t)
			id := "sess-recorded-" + strings.ReplaceAll(name, " ", "-")
			runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Write", map[string]any{"file_path": "/repo/new.go"}))
			p := toolCall(t, id, call.tool, call.input)
			if name == "by a subagent" {
				p["agent_id"] = "agent-1"
			}
			runHookAs(t, "post-tool-use", "claude", p)
			if got := turnEndOutput(t, registerPayload(t, id)); len(got) != 0 {
				t.Fatalf("reminded although something was recorded: %v", got)
			}
		})
	}
}

// A turn that a Stop hook already continued, and a StopFailure, get no
// reminder; the reminder is kept for a real turn end.
func TestTurnEndKeepsTheReminderForARealTurnEnd(t *testing.T) {
	registerEnv(t)
	const id = "sess-active"
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Edit", map[string]any{"file_path": "/repo/c.go"}))
	p := registerPayload(t, id)
	p["stop_hook_active"] = true
	if got := turnEndOutput(t, p); len(got) != 0 {
		t.Fatalf("reminded inside a continued turn: %v", got)
	}
	p = registerPayload(t, id)
	p["hook_event_name"] = "StopFailure"
	if got := turnEndOutput(t, p); len(got) != 0 {
		t.Fatalf("reminded after an API error: %v", got)
	}
	p = registerPayload(t, id)
	p["hook_event_name"] = "Stop"
	if got := turnEndOutput(t, p); reminderText(got) == "" {
		t.Fatalf("the real turn end lost its reminder: %v", got)
	}
}

// REQ-436, criterion 12: the hooks count ghosttree calls per tool, action and
// subagent and hand them to the server at the end of the turn.
func TestHooksReportGhosttreeCallsAtTurnEnd(t *testing.T) {
	e := registerEnv(t)
	const id = "sess-count"
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "mcp__ghosttree__context_search", map[string]any{"query": "x"}))
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "mcp__ghosttree__context_search", map[string]any{"query": "y"}))
	sub := toolCall(t, id, "mcp__ghosttree__request", map[string]any{"action": "get", "request_id": 1})
	sub["agent_id"] = "agent-9"
	runHookAs(t, "post-tool-use", "claude", sub)
	runHookAs(t, "post-tool-use", "claude", toolCall(t, id, "Bash", map[string]any{"command": "ls"}))
	turnEndOutput(t, registerPayload(t, id))

	rows, err := e.st.DB().Query(`SELECT tool, action, subagent, calls, harness FROM tool_call_counts WHERE session_ref=? ORDER BY tool`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var tool, action, harness string
		var sub, calls int
		if err := rows.Scan(&tool, &action, &sub, &calls, &harness); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{tool, action, map[int]string{0: "main", 1: "sub"}[sub], string(rune('0' + calls)), harness}, " "))
	}
	want := "context_search search main 2 claude|request get sub 1 claude"
	if strings.Join(got, "|") != want {
		t.Fatalf("counted %q, want %q", strings.Join(got, "|"), want)
	}
	// Sent once: a second turn end adds nothing.
	turnEndOutput(t, registerPayload(t, id))
	var total int
	if err := e.st.DB().QueryRow(`SELECT SUM(calls) FROM tool_call_counts WHERE session_ref=?`, id).Scan(&total); err != nil || total != 3 {
		t.Fatalf("calls after a second turn end: %d %v", total, err)
	}
}
