package requestledger

import (
	"encoding/json"
	"os"
	"testing"
)

func TestApprovedFixturesMeetContract(t *testing.T) {
	raw, err := os.ReadFile("fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var traces []Trace
	if err := json.Unmarshal(raw, &traces); err != nil {
		t.Fatal(err)
	}
	if len(traces) != 9 {
		t.Fatalf("fixture count = %d, want 9", len(traces))
	}
	// The budget is the measured one, and so are the fixture figures. They
	// used to be written by hand, all comfortably under a 2000-byte ceiling,
	// while the tools they describe were answering with 4837 bytes — a check
	// against invented numbers passes no matter what the code does.
	for _, trace := range traces {
		if err := Verify(trace, MaxMutationResponseBytes); err != nil {
			t.Error(err)
		}
	}
}

func TestVerifierRejectsMissingSearchAndDuplicate(t *testing.T) {
	bad := Trace{Name: "bad", Substantial: true, ExistingMatch: true, CreateCalls: 1, PrimaryAssociations: 1}
	if err := Verify(bad, MaxMutationResponseBytes); err == nil {
		t.Fatal("invalid trace accepted")
	}
}
