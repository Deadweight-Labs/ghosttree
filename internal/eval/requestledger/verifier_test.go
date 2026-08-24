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
	for _, trace := range traces {
		if err := Verify(trace, 2000); err != nil {
			t.Error(err)
		}
	}
}

func TestVerifierRejectsMissingSearchAndDuplicate(t *testing.T) {
	bad := Trace{Name: "bad", Substantial: true, ExistingMatch: true, CreateCalls: 1, PrimaryAssociations: 1}
	if err := Verify(bad, 2000); err == nil {
		t.Fatal("invalid trace accepted")
	}
}
