package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTranscript(t *testing.T, dir, name, answer string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	line := `{"type":"result","num_turns":7,"total_cost_usd":0.25,"result":` +
		mustJSON(t, answer) + "}\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	quoted := `"`
	for _, r := range s {
		switch r {
		case '"':
			quoted += `\"`
		case '\n':
			quoted += `\n`
		case '\\':
			quoted += `\\`
		default:
			quoted += string(r)
		}
	}
	return quoted + `"`
}

// TestRegradeTurnsAWrongJudgementIntoARightOne is the whole point: the agent
// answered with the JSON tag instead of the Go field name. Both name the same
// field; only the accepted list was too narrow.
func TestRegradeTurnsAWrongJudgementIntoARightOne(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "t1.jsonl",
		"fertig\n```agentbench-form\n{\"slots\":{\"f\":{\"string\":\"certificate_exports\"}}}\n```")
	records := []RunRecord{{TaskID: "t1", Arm: ArmBare, Transcript: Transcript{RawPath: path}}}
	widened := []Task{{ID: "t1", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "f", Type: SlotString, Weight: 1,
			Accepted: []string{"CertificateExports", "certificate_exports"}}}}}

	out, err := Regrade(records, widened)
	if err != nil {
		t.Fatal(err)
	}

	if out[0].Score.FactRecall != 1 {
		t.Fatalf("the widened list must score the recorded answer: %+v", out[0].Score)
	}
	if out[0].Transcript.Turns != 7 || out[0].Transcript.CostUSD != 0.25 {
		t.Fatalf("effort must be carried over from the transcript: %+v", out[0].Transcript)
	}
}

// TestRegradeLeavesProductFailuresAlone guards the line between correcting a
// judgement and inventing a result. A run that never produced an answer must
// not become a hit because the task definition changed.
func TestRegradeLeavesProductFailuresAlone(t *testing.T) {
	records := []RunRecord{{TaskID: "t1", Arm: ArmBare,
		Failure: FailureProduct, FailureMsg: "401"}}
	tasks := []Task{{ID: "t1", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "f", Type: SlotString, Weight: 1, Accepted: []string{"x"}}}}}

	out, err := Regrade(records, tasks)
	if err != nil {
		t.Fatal(err)
	}

	if out[0].Failure != FailureProduct || out[0].Score.FactRecall != 0 {
		t.Fatalf("a failed run must stay failed: %+v", out[0])
	}
}

func TestRegradeRefusesARunWithoutATranscript(t *testing.T) {
	records := []RunRecord{{TaskID: "t1", Arm: ArmBare}}
	tasks := []Task{{ID: "t1", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "f", Type: SlotString, Weight: 1, Accepted: []string{"x"}}}}}

	if _, err := Regrade(records, tasks); err == nil {
		t.Fatal("without evidence there is nothing to regrade from")
	}
}
