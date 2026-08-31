package agentbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRejectsSlotWithoutExpectation(t *testing.T) {
	task := Task{
		ID:       "nurproxy-001",
		Repo:     "NurProxy",
		Commit:   "a1b2c3d",
		Prompt:   "Wo wird reauthentifiziert?",
		Category: CategoryLocalization,
		Exposure: Exposure{AnswerFactSeen: true},
		Facts: []FactSlot{
			{ID: "tolerance", Type: SlotInteger, Weight: 1},
		},
	}
	if err := task.Validate(); err == nil {
		t.Fatal("expected an error for a slot without an expectation")
	}
}

func TestValidateAcceptsCompleteTask(t *testing.T) {
	expected := 60
	task := Task{
		ID:       "nurproxy-001",
		Repo:     "NurProxy",
		Commit:   "a1b2c3d",
		Prompt:   "Wo wird reauthentifiziert?",
		Category: CategoryLocalization,
		Exposure: Exposure{AnswerFactSeen: true},
		Facts: []FactSlot{
			{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected},
		},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("expected a valid task, got %v", err)
	}
}

func TestValidateRejectsDuplicateSlotID(t *testing.T) {
	expected := 60
	task := Task{
		ID: "nurproxy-002", Repo: "NurProxy", Commit: "a1b2c3d", Prompt: "p",
		Category: CategoryLocalization,
		Facts: []FactSlot{
			{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected},
			{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected},
		},
	}
	if err := task.Validate(); err == nil {
		t.Fatal("expected duplicate slot ids to be rejected")
	}
}

func TestLoadTasksReadsJSONFiles(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"t1","repo":"NurProxy","commit":"a1b2c3d","prompt":"p",
	  "category":"localization","exposure":{"answer_fact_seen":true},
	  "facts":[{"id":"tolerance","type":"integer","weight":1,"expected_int":60}]}`
	if err := os.WriteFile(filepath.Join(dir, "t1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := LoadTasks(dir)
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}
	if tasks[0].Facts[0].ExpectedInt == nil || *tasks[0].Facts[0].ExpectedInt != 60 {
		t.Fatalf("expected_int did not survive decoding: %+v", tasks[0].Facts[0])
	}
}

func TestLoadTasksRejectsAnInvalidFile(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"t1","repo":"NurProxy","commit":"a1b2c3d","prompt":"p",
	  "category":"localization","facts":[{"id":"x","type":"integer","weight":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "t1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTasks(dir); err == nil {
		t.Fatal("expected an invalid task file to fail the load")
	}
}

func TestValidateAcceptsANegativeControlWithoutAcceptedValues(t *testing.T) {
	task := Task{ID: "n", Repo: "r", Commit: "c", Prompt: "p", Category: CategoryNegative,
		Facts: []FactSlot{{ID: "s", Type: SlotPath, Weight: 1,
			Contradicts: []string{"internal/provider/route53/route53.go"}}}}

	if err := task.Validate(); err != nil {
		t.Fatalf("a negative control has no right answer by construction: %v", err)
	}
}

func TestValidateRefusesANegativeControlWithoutContradictions(t *testing.T) {
	task := Task{ID: "n", Repo: "r", Commit: "c", Prompt: "p", Category: CategoryNegative,
		Facts: []FactSlot{{ID: "s", Type: SlotPath, Weight: 1}}}

	if err := task.Validate(); err == nil {
		t.Fatal("without the plausible inventions the category measures nothing")
	}
}

func TestValidateRefusesANegativeControlWithAnAnswer(t *testing.T) {
	task := Task{ID: "n", Repo: "r", Commit: "c", Prompt: "p", Category: CategoryNegative,
		Facts: []FactSlot{{ID: "s", Type: SlotPath, Weight: 1,
			Accepted: []string{"internal/provider/route53.go"}, Contradicts: []string{"x"}}}}

	if err := task.Validate(); err == nil {
		t.Fatal("a negative control with a right answer is not a negative control")
	}
}
