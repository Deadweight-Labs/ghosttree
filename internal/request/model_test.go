package request

import "testing"

func TestHumanIDs(t *testing.T) {
	if got := (Request{ID: 42}).HumanID(); got != "REQ-42" {
		t.Fatalf("request ID = %q", got)
	}
	if got := (Criterion{RequestID: 42, Number: 3}).HumanID(); got != "AC-42.3" {
		t.Fatalf("criterion ID = %q", got)
	}
}

func TestRuleErrorCode(t *testing.T) {
	err := NewRuleError("open_criteria", "cannot complete", "satisfy criteria", []string{"AC-42.1"})
	if Code(err) != "open_criteria" {
		t.Fatalf("code = %q", Code(err))
	}
}
