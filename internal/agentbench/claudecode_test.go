package agentbench

import (
	"errors"
	"os"
	"os/exec"
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

// Der echte Fall aus run-zentrale-b6: der bare-Arm ruft Agent auf, und alles
// danach traegt parent_tool_use_id. Claude Code zaehlt dem Hauptagenten zwei
// Zuege an, waehrend der Subagent unbegrenzt weiterarbeitet — gemessen wurden
// 2 Zuege bei 17 Werkzeugaufrufen. Wird die delegierte Arbeit nicht getrennt
// erfasst, liest sich genau dieser Lauf als der sparsamste der Kampagne.
func TestParseStreamJSONSeparatesDelegatedWork(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent"}]}}
{"type":"assistant","parent_tool_use_id":"toolu_01AHVK","message":{"content":[{"type":"tool_use","name":"Bash"}]}}
{"type":"assistant","parent_tool_use_id":"toolu_01AHVK","message":{"content":[{"type":"tool_use","name":"Read"}]}}
{"type":"result","subtype":"success","num_turns":2,"result":"fertig"}
`
	tr, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStreamJSON: %v", err)
	}
	if tr.ToolCalls != 3 {
		t.Fatalf("delegierte Aufrufe sind Aufwand und zaehlen mit: want 3, got %d", tr.ToolCalls)
	}
	if tr.DelegatedToolCalls != 2 {
		t.Fatalf("delegated tool calls: want 2, got %d", tr.DelegatedToolCalls)
	}
	if tr.Turns != 2 {
		t.Fatalf("turns kommen weiter vom Ergebnis-Ereignis: want 2, got %d", tr.Turns)
	}
	if tr.Turns >= tr.ToolCalls {
		t.Fatal("der Sinn der Trennung ist, dass Zuege und Aufrufe auseinanderlaufen duerfen")
	}
}

func TestDelegationImbalanceIsReportedWhenArmsDifferSharply(t *testing.T) {
	// Robcord-Zentrale bei Budget 6: bare delegierte in 5 von 12 Laeufen,
	// ghosttree in keinem. Ab da messen die Zugzahlen der beiden Arme nicht
	// mehr dasselbe.
	summaries := []DelegationSummary{
		{Arm: ArmBare, Runs: 5, RunsTotal: 12},
		{Arm: ArmGhosttree, Runs: 0, RunsTotal: 12},
	}
	if !DelegationImbalanced(summaries) {
		t.Fatal("eine Schieflage von 5/12 gegen 0/12 muss gemeldet werden")
	}
	even := []DelegationSummary{
		{Arm: ArmBare, Runs: 1, RunsTotal: 12},
		{Arm: ArmGhosttree, Runs: 0, RunsTotal: 12},
	}
	if DelegationImbalanced(even) {
		t.Fatal("ein einzelner Lauf Unterschied ist keine Schieflage")
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

func TestDescribeExecErrorIncludesStderr(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'Invalid API key' >&2; exit 1")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("the helper command must fail for this test to mean anything")
	}

	described := describeExecError(err)

	// "exit status 1" allein ist als Fehlermeldung wertlos: bei 700 Laeufen
	// laesst sich damit nicht unterscheiden, ob die Zugangsdaten fehlen oder
	// der Container kaputt ist.
	if !strings.Contains(described, "Invalid API key") {
		t.Fatalf("stderr must survive into the failure message, got %q", described)
	}
}

func TestDescribeExecErrorPassesThroughAPlainError(t *testing.T) {
	if got := describeExecError(errors.New("no such file")); got != "no such file" {
		t.Fatalf("a non-exec error must survive unchanged, got %q", got)
	}
}

func TestDescribeExecErrorTruncatesAFloodOfStderr(t *testing.T) {
	cmd := exec.Command("sh", "-c", "head -c 20000 /dev/zero | tr '\\0' 'x' >&2; exit 1")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("the helper command must fail")
	}

	if len(describeExecError(err)) > 2000 {
		t.Fatal("a failing run must not paste kilobytes of stderr into every record")
	}
}

func TestParseStreamJSONSurfacesAnAPIError(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"Not logged in"}]},"error":"authentication_failed","is_api_error_message":true}
{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","usage":{"input_tokens":0,"output_tokens":0}}
`
	tr, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}

	// Ohne dieses Feld liefe der Lauf als Antwort ohne Formular durch und
	// wuerde als Enthaltung gewertet — ein Anmeldefehler saehe dann aus wie
	// ein Agent, der sich nicht festlegen wollte.
	if tr.AgentError == "" {
		t.Fatalf("an is_error result must be surfaced, got %+v", tr)
	}
	if !strings.Contains(tr.AgentError, "Not logged in") {
		t.Fatalf("the agent's own message must survive: %q", tr.AgentError)
	}
}

func TestParseStreamJSONLeavesASuccessfulRunUnflagged(t *testing.T) {
	stream := `{"type":"result","subtype":"success","is_error":false,"result":"fertig"}` + "\n"
	tr, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if tr.AgentError != "" {
		t.Fatalf("a clean run must not be flagged: %q", tr.AgentError)
	}
}

func TestDescribeExecErrorFallsBackToStdout(t *testing.T) {
	cmd := exec.Command("sh", "-c", `echo '{"type":"result","is_error":true,"result":"Not logged in"}'; exit 1`)
	out, err := cmd.Output()
	if err == nil {
		t.Fatal("the helper command must fail")
	}

	described := describeExecErrorWithOutput(err, out)

	// Claude Code schreibt seine Diagnose ins stream-json auf stdout, nicht
	// nach stderr; ohne diesen Rueckgriff bleibt "exit status 1" uebrig.
	if !strings.Contains(described, "Not logged in") {
		t.Fatalf("stdout must be consulted when stderr is empty, got %q", described)
	}
}

func TestDescribeExecErrorPrefersTheResultFieldOverRawJSON(t *testing.T) {
	stream := `{"type":"system","subtype":"init","cwd":"/work/repo"}
{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`
	cmd := exec.Command("sh", "-c", "cat <<'X'\n"+stream+"\nX\nexit 1")
	out, err := cmd.Output()
	if err == nil {
		t.Fatal("the helper command must fail")
	}

	described := describeExecErrorWithOutput(err, out)

	if !strings.HasSuffix(described, "Not logged in · Please run /login") {
		t.Fatalf("the result field must be used verbatim, got %q", described)
	}
	if strings.Contains(described, `"subtype"`) {
		t.Fatalf("raw JSON must not be pasted when a result field exists: %q", described)
	}
}

func TestWriteRawKeepsEveryRepetition(t *testing.T) {
	dir := t.TempDir()
	task := Task{ID: "np-01"}

	first, err := writeRaw(dir, Invocation{Task: task, Arm: ArmGhosttree, Repetition: 1}, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeRaw(dir, Invocation{Task: task, Arm: ArmGhosttree, Repetition: 2}, []byte("b"))
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		// Sonst ueberschreibt die zweite Wiederholung die erste, und genau die
		// Streuung zwischen beiden ist der Grund, ueberhaupt zu wiederholen.
		t.Fatalf("repetitions must not share a file name: %s", first)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("want two transcripts, got %d (%v)", len(entries), err)
	}
}

// TestExhaustedTurnBudgetIsAnOutcomeNotAFault holds the line the pilot found:
// four runs died on np-11, all of them arms without the ledger, all of them
// searching until the budget ran out. Counted as product failures they dropped
// out of the scoring — removing exactly the cases where an arm did not have the
// answer, which flatters the arm that gave up.
func TestExhaustedTurnBudgetIsAnOutcomeNotAFault(t *testing.T) {
	stream := `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":20,` +
		`"result":"Reached max turns"}` + "\n"
	transcript, err := parseStreamJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if !transcript.MaxTurnsExceeded {
		t.Fatal("an exhausted turn budget must be recognised as such")
	}
	if transcript.AgentError != "" {
		t.Fatalf("it is not an agent error: %q", transcript.AgentError)
	}
	if transcript.Turns != 20 {
		t.Fatalf("the effort still counts: %d", transcript.Turns)
	}
}
