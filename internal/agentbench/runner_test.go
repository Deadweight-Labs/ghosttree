package agentbench

import (
	"context"
	"slices"
	"testing"
	"time"
)

func pilotTask(id string) Task {
	return Task{
		ID: id, Repo: "NurProxy", Commit: "c", Prompt: "p", Category: CategoryLocalization,
		Facts: []FactSlot{{ID: "s", Type: SlotString, Weight: 1, Accepted: []string{"x"}}},
	}
}

func TestBlockOrderIsDeterministic(t *testing.T) {
	arms := []ArmName{ArmClaudeNative, ArmGhosttree, ArmOracle}
	first := blockOrder(7, 0, 1, arms)
	again := blockOrder(7, 0, 1, arms)
	if !slices.Equal(first, again) {
		t.Fatalf("same seed must give the same order: %v vs %v", first, again)
	}
	if len(first) != len(arms) {
		t.Fatalf("permutation lost arms: %v", first)
	}
	for _, arm := range arms {
		if !slices.Contains(first, arm) {
			t.Fatalf("permutation dropped %q: %v", arm, first)
		}
	}
}

func TestBlockOrderVariesAcrossBlocks(t *testing.T) {
	arms := []ArmName{ArmClaudeNative, ArmGhosttree, ArmOracle}
	base := blockOrder(7, 0, 1, arms)
	varied := false
	for i := 1; i < 20; i++ {
		if !slices.Equal(base, blockOrder(7, i, 1, arms)) {
			varied = true
			break
		}
	}
	if !varied {
		t.Fatal("orders never vary across blocks; provider drift would favour one arm")
	}
}

func TestBlockOrderDoesNotMutateTheInput(t *testing.T) {
	arms := []ArmName{ArmClaudeNative, ArmGhosttree, ArmOracle}
	original := slices.Clone(arms)
	for i := 0; i < 20; i++ {
		blockOrder(3, i, 1, arms)
	}
	if !slices.Equal(arms, original) {
		t.Fatalf("blockOrder shuffled the caller's slice: %v", arms)
	}
}

func TestRunRecordsAProductFailureWithoutAborting(t *testing.T) {
	campaign := Campaign{
		RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: []ArmName{ArmGhosttree}, Repetitions: 1, Seed: 1,
	}
	agent := NewFakeAgent(nil) // liefert fuer jede Aufgabe einen Fehler
	records, err := Run(context.Background(), campaign, []Task{pilotTask("t1")}, agent)
	if err != nil {
		t.Fatalf("Run must not abort on a single failure: %v", err)
	}
	if len(records) != 1 || records[0].Failure != FailureProduct {
		t.Fatalf("expected one product failure, got %+v", records)
	}
}

func TestRunMarksAMissingFormAsScoringFailure(t *testing.T) {
	campaign := Campaign{
		RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: []ArmName{ArmGhosttree}, Repetitions: 1, Seed: 1,
	}
	agent := NewFakeAgent(map[string]string{"t1": "ich habe gearbeitet, aber kein Formular"})
	records, err := Run(context.Background(), campaign, []Task{pilotTask("t1")}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Failure != FailureScoring {
		t.Fatalf("a missing form is a scoring failure, got %q", records[0].Failure)
	}
}

func TestRunProducesOneRecordPerBlockCell(t *testing.T) {
	campaign := Campaign{
		RepoCommit: "c", KnowledgeCutoff: time.Unix(1, 0).UTC(),
		Arms: []ArmName{ArmClaudeNative, ArmGhosttree}, Repetitions: 3, Seed: 5,
	}
	tasks := []Task{pilotTask("t1"), pilotTask("t2")}
	answer := "```agentbench-form\n{\"slots\":{\"s\":{\"string\":\"x\"}}}\n```"
	agent := NewFakeAgent(map[string]string{"t1": answer, "t2": answer})

	records, err := Run(context.Background(), campaign, tasks, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2*2*3 {
		t.Fatalf("want 12 records (2 arms x 2 tasks x 3 repetitions), got %d", len(records))
	}
	for _, record := range records {
		if record.Failure != FailureNone {
			t.Fatalf("unexpected failure: %+v", record)
		}
		if record.Score.FactRecall != 1 {
			t.Fatalf("scored run should be perfect: %+v", record.Score)
		}
	}
}

func TestRunRefusesACampaignThatFailsValidation(t *testing.T) {
	campaign := Campaign{RepoCommit: "c"} // kein Cutoff
	if _, err := Run(context.Background(), campaign, nil, NewFakeAgent(nil)); err == nil {
		t.Fatal("Run must refuse an invalid campaign instead of producing numbers")
	}
}
