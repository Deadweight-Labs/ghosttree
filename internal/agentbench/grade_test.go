package agentbench

import "testing"

func TestGradeCountsHitsContradictionsAndAbstentions(t *testing.T) {
	expected := 60
	no := false
	task := Task{
		ID: "t1", Repo: "r", Commit: "c", Prompt: "p",
		Category: CategoryLocalization,
		Facts: []FactSlot{
			{ID: "path", Type: SlotPath, Weight: 2, Accepted: []string{"internal/auth/refresh.go"}},
			{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected},
			{ID: "resign", Type: SlotBoolean, Weight: 1, ExpectedBool: &no},
			{ID: "note", Type: SlotString, Weight: 1, Accepted: []string{"toleranzfenster"}},
		},
	}
	path := "internal/auth/refresh.go"
	wrong := 30
	note := "Toleranzfenster"
	form := ResponseForm{Slots: map[string]SlotValue{
		"path":      {Path: &path},
		"tolerance": {Integer: &wrong},
		"note":      {String: &note},
		// "resign" fehlt: Enthaltung
	}}

	score := Grade(task, form)

	// Gesamtgewicht 5, richtig sind path (2) und note (1) — Recall 3/5.
	if score.FactRecall != 0.6 {
		t.Fatalf("fact recall: want 0.6, got %v", score.FactRecall)
	}
	if score.AnsweredSlots != 3 || score.CorrectSlots != 2 {
		t.Fatalf("answered/correct: got %d/%d", score.AnsweredSlots, score.CorrectSlots)
	}
	if score.AbstentionRate != 0.25 {
		t.Fatalf("abstention rate: want 0.25, got %v", score.AbstentionRate)
	}
	if score.ClaimPrecision < 0.666 || score.ClaimPrecision > 0.667 {
		t.Fatalf("claim precision: want 2/3, got %v", score.ClaimPrecision)
	}
}

func TestGradeMatchesIntegerRegardlessOfPhrasing(t *testing.T) {
	expected := 60
	task := Task{ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected}}}
	sixty := 60
	score := Grade(task, ResponseForm{Slots: map[string]SlotValue{"tolerance": {Integer: &sixty}}})
	if score.FactRecall != 1 {
		t.Fatalf("a typed integer must match whatever wording produced it, got %v", score.FactRecall)
	}
}

func TestGradeCountsAKnownContradiction(t *testing.T) {
	task := Task{ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{
			ID: "path", Type: SlotPath, Weight: 1,
			Accepted:    []string{"internal/auth/refresh.go"},
			Contradicts: []string{"internal/auth/jwt.go"},
		}}}
	wrong := "internal/auth/jwt.go"
	score := Grade(task, ResponseForm{Slots: map[string]SlotValue{"path": {Path: &wrong}}})
	if score.Contradictions != 1 {
		t.Fatalf("want one contradiction, got %d", score.Contradictions)
	}
	if score.ContradictRate != 1 {
		t.Fatalf("want a contradiction rate of 1, got %v", score.ContradictRate)
	}
}

func TestGradeDoesNotRewardSilence(t *testing.T) {
	expected := 60
	task := Task{ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected}}}

	silent := Grade(task, ResponseForm{Slots: map[string]SlotValue{}})

	// Schweigen haelt die Widerspruchsrate bei null — genau deshalb muss die
	// Enthaltungsrate mitberichtet werden, sonst gewinnt der stumme Arm.
	if silent.ContradictRate != 0 {
		t.Fatalf("silence cannot produce contradictions, got %v", silent.ContradictRate)
	}
	if silent.AbstentionRate != 1 {
		t.Fatalf("want a full abstention, got %v", silent.AbstentionRate)
	}
	if silent.FactRecall != 0 {
		t.Fatalf("silence earns no recall, got %v", silent.FactRecall)
	}
}

func TestGradeIgnoresSlotsTheTaskNeverAskedFor(t *testing.T) {
	expected := 60
	task := Task{ID: "t", Repo: "r", Commit: "c", Prompt: "p",
		Facts: []FactSlot{{ID: "tolerance", Type: SlotInteger, Weight: 1, ExpectedInt: &expected}}}
	extra := "geraten"
	score := Grade(task, ResponseForm{Slots: map[string]SlotValue{
		"tolerance": {Integer: &expected},
		"invented":  {String: &extra},
	}})
	if score.AnsweredSlots != 1 || score.TotalSlots != 1 {
		t.Fatalf("an invented slot must not enter the denominator: %+v", score)
	}
}
