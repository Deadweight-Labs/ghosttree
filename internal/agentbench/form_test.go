package agentbench

import (
	"errors"
	"testing"
)

func TestParseFormTakesTheLastBlock(t *testing.T) {
	out := "denke nach\n" +
		"```agentbench-form\n{\"slots\":{\"tolerance\":{\"integer\":30}}}\n```\n" +
		"korrigiere mich\n" +
		"```agentbench-form\n{\"slots\":{\"tolerance\":{\"integer\":60}}}\n```\n"
	form, err := ParseForm(out)
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	value, ok := form.Slots["tolerance"]
	if !ok || value.Integer == nil || *value.Integer != 60 {
		t.Fatalf("expected the last block to win, got %+v", form.Slots)
	}
}

func TestParseFormReportsMissingBlock(t *testing.T) {
	if _, err := ParseForm("keine antwort"); !errors.Is(err, ErrNoForm) {
		t.Fatalf("expected ErrNoForm, got %v", err)
	}
}

func TestParseFormReportsUnterminatedBlock(t *testing.T) {
	if _, err := ParseForm("```agentbench-form\n{\"slots\":{}}"); !errors.Is(err, ErrNoForm) {
		t.Fatalf("expected ErrNoForm for an unterminated block, got %v", err)
	}
}

func TestSlotValueAbstentionIsDistinctFromFalse(t *testing.T) {
	no := false
	answered := SlotValue{Boolean: &no}
	if answered.IsAbstention() {
		t.Fatal("a boolean false is an answer, not an abstention")
	}
	if !(SlotValue{}).IsAbstention() {
		t.Fatal("an empty slot value is an abstention")
	}
}

func TestParseFormAcceptsAnEmptySlotMap(t *testing.T) {
	form, err := ParseForm("```agentbench-form\n{\"explanation\":\"weiss nicht\"}\n```")
	if err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if form.Slots == nil {
		t.Fatal("Slots must never be nil, callers index into it")
	}
}
