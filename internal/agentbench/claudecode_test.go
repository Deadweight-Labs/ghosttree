package agentbench

import (
	"strings"
	"testing"
)

func TestParseStreamJSONCountsToolCallsAndTokens(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep"}]}}
{"type":"result","result":"fertig","usage":{"input_tokens":1200,"output_tokens":300}}
`
	tr, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStreamJSON: %v", err)
	}
	if tr.ToolCalls != 2 {
		t.Fatalf("tool calls: want 2, got %d", tr.ToolCalls)
	}
	if tr.FilesRead != 1 {
		t.Fatalf("files read counts only the Read tool: want 1, got %d", tr.FilesRead)
	}
	if tr.InputTokens != 1200 || tr.OutputTokens != 300 {
		t.Fatalf("tokens: got %d/%d", tr.InputTokens, tr.OutputTokens)
	}
	if tr.Output != "fertig" {
		t.Fatalf("output: got %q", tr.Output)
	}
}

func TestParseStreamJSONSurvivesForeignLines(t *testing.T) {
	stream := "not json at all\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}` + "\n" +
		"\n" +
		`{"type":"result","result":"ok","usage":{"input_tokens":1,"output_tokens":2}}` + "\n"
	tr, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("a foreign line must not lose the run: %v", err)
	}
	if tr.Output != "ok" || tr.ToolCalls != 1 {
		t.Fatalf("unexpected transcript: %+v", tr)
	}
}

func TestClosingFormInstructionNamesEverySlot(t *testing.T) {
	expected := 60
	task := Task{
		ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{
			{ID: "decision_path", Type: SlotPath, Weight: 1, Accepted: []string{"x"}},
			{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected},
		},
	}
	instruction := closingFormInstruction(task)
	for _, want := range []string{"decision_path", "tolerance", "path", "integer", formFence} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction must mention %q:\n%s", want, instruction)
		}
	}
}

func TestClosingFormInstructionDiscouragesGuessing(t *testing.T) {
	task := Task{ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "s", Type: SlotString, Weight: 1, Accepted: []string{"x"}}}}
	instruction := closingFormInstruction(task)
	if !strings.Contains(strings.ToLower(instruction), "raten") {
		t.Fatalf("the agent must be told that guessing scores worse than abstaining:\n%s", instruction)
	}
}

func TestEnvSliceIsSortedForReproducibility(t *testing.T) {
	env := envSlice(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []string{"A=1", "B=2", "C=3"}
	for i, entry := range want {
		if env[i] != entry {
			t.Fatalf("env must be sorted for reproducible runs: %v", env)
		}
	}
}
