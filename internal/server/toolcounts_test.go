package server

import "testing"

// REQ-436: a report of tool calls goes through ProjectAccess; it needs the
// right to write in the project, so neither a stranger nor a guest adds to the
// project's statistics.
func TestToolUseReportNeedsTheRightToUploadSessions(t *testing.T) {
	f := accessAPI(t, true)
	body := map[string]any{"project": accProject, "session_ref": "s-report", "harness": "claude",
		"uses": []map[string]any{{"tool": "context_search", "action": "search", "calls": 2}}}
	f.expect(t, "mia", 200, "POST", "/api/tool-uses", body)
	f.expect(t, "nora", 404, "POST", "/api/tool-uses", body)
	f.expect(t, "gus", 403, "POST", "/api/tool-uses", body)
	f.expect(t, "mia", 400, "POST", "/api/tool-uses", map[string]any{"project": accProject, "session_ref": "s-report", "uses": []any{}})
	var calls int
	if err := f.st.DB().QueryRow(`SELECT SUM(calls) FROM tool_call_counts WHERE session_ref='s-report'`).Scan(&calls); err != nil || calls != 2 {
		t.Fatalf("stored calls %d %v", calls, err)
	}
}
