package actor

import "testing"

func TestCallsToolCountsOnlyRealCalls(t *testing.T) {
	tools, cmds := []string{"pain_record"}, []string{"ctx pain add"}
	for _, c := range []struct {
		name, line string
		want       bool
	}{
		{"claude mcp", `{"type":"assistant","message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","name":"mcp__ghosttree__pain_record","input":{"title":"t"}}]}}`, true},
		{"claude bash", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ctx pain add \"Titel\" --project x"}}]}}`, true},
		{"codex mcp", `{"type":"response_item","payload":{"type":"function_call","name":"mcp__ghosttree__pain_record","arguments":"{\"title\":\"t\"}"}}`, true},
		{"codex shell", `{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"ctx pain add Titel\"]}"}}`, true},
		{"codex exec", `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ctx pain add Titel\"}"}}`, true},
		// Nennen ist kein Aufruf.
		{"tool result", `{"type":"user","message":{"content":[{"type":"tool_result","content":"pain_record ok, ctx pain add"}]}}`, false},
		{"prompt", `{"type":"user","message":{"content":"bitte pain_record aufrufen oder ctx pain add"}}`, false},
		{"assistant text", `{"type":"assistant","message":{"content":[{"type":"text","text":"ich rufe gleich pain_record auf"}]}}`, false},
		{"other tool", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__ghosttree__pain_search","input":{}}]}}`, false},
		{"bash elsewhere", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep 'ctx pain add' docs"}}]}}`, false},
		{"codex output", `{"type":"response_item","payload":{"type":"function_call_output","output":"pain_record"}}`, false},
		{"suffix only", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"xpain_record","input":{}}]}}`, false},
	} {
		if got := CallsTool(c.line, tools, cmds); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if !IsToolResult(`{"type":"user","message":{"content":[{"type":"tool_result","content":"PAIN-3"}]}}`) || IsToolResult(`{"type":"user","message":{"content":"PAIN-3"}}`) {
		t.Error("IsToolResult")
	}
}
