package transcript

import "testing"

// Rollouts mit dem exec-Werkzeug: die Ausgabe ist eine Liste von Teilen, und
// jeder Befehl, den das Skript ausführte, steht mit Ausgabe in item_completed.
func TestCodexExecToolOutputsAndCommands(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-11-01T10:00:00.000Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"const r = await run('go vet ./...')"}}`,
		`{"timestamp":"2026-11-01T10:00:01.000Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"th","turn_id":"tu","item":{"type":"CommandExecution","id":"ce1","command":"go vet ./...","status":"failed","aggregated_output":"vet: unused variable\n","exit_code":1}}}`,
		`{"timestamp":"2026-11-01T10:00:02.000Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"Script completed"},{"type":"input_image","image_url":"[ghosttree: Bild entfernt]"},{"type":"input_text","text":"exit 1"}]}}`,
		`{"timestamp":"2026-11-01T10:00:03.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"ce2","command":["git","status"],"stdout":"clean\n","exit_code":0}}}`,
		`{"timestamp":"2026-11-01T10:00:04.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"copy"}]}}}`,
	}
	var blocks []Block
	for i, l := range lines {
		blocks = append(blocks, Parse("codex", i, l).Blocks...)
	}
	paired := Pair(blocks)
	if len(paired) != 3 {
		t.Fatalf("%d blocks: %+v", len(paired), paired)
	}
	exec, vet, status := paired[0], paired[1], paired[2]
	if exec.Tool != "exec" || exec.Output != "Script completed\nexit 1" || exec.ResultSeq != 2 {
		t.Errorf("exec = %+v", exec)
	}
	if vet.Tool != "shell" || vet.Input != "go vet ./..." || vet.Output != "vet: unused variable" || !vet.Failed || vet.Status != "exit 1" || vet.Seq != 1 {
		t.Errorf("command = %+v", vet)
	}
	if status.Input != "git status" || status.Output != "clean" || status.Failed {
		t.Errorf("argv command = %+v", status)
	}
	if f := Fields(Parse("codex", 1, lines[1])); f.ToolCalls != 1 || f.ToolOutput != "vet: unused variable" {
		t.Errorf("index fields = %+v", f)
	}
}
