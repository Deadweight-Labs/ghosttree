// Package requestledger verifies captured agent traces against the request-ledger contract.
package requestledger

import "fmt"

// MaxMutationResponseBytes is what one ledger mutation may answer with.
//
// The number is taken from what the tools actually return, not from a
// technical maximum. A criterion update carries an id, a number, a state and a
// count: measured at 121 bytes, and creation — the one reply that must list
// the new criterion ids — at 257. A kilobyte leaves room for a request with
// many criteria and still refuses the old behaviour, which answered every
// mutation with the whole request: 3862 bytes for the first criterion on a
// five-criterion request, and 4837 by the fourth, because the activity list
// grows with each call. That is the part that matters. The cost of using the
// ledger carefully rose the more carefully it was used, which made neglecting
// it the cheaper option.
const MaxMutationResponseBytes = 1024

type Trace struct {
	Name                string `json:"name"`
	Substantial         bool   `json:"substantial"`
	ExistingMatch       bool   `json:"existing_match"`
	SearchCalls         int    `json:"search_calls"`
	CreateCalls         int    `json:"create_calls"`
	PrimaryAssociations int    `json:"primary_associations"`
	EvidenceViolations  int    `json:"evidence_violations"`
	ResponseBytes       int    `json:"response_bytes"`
}

func Verify(t Trace, maxResponseBytes int) error {
	if t.Substantial && t.SearchCalls < 1 {
		return fmt.Errorf("%s: substantial work did not search the ledger", t.Name)
	}
	if !t.Substantial && t.CreateCalls != 0 {
		return fmt.Errorf("%s: trivial work created a request", t.Name)
	}
	if t.ExistingMatch && t.CreateCalls != 0 {
		return fmt.Errorf("%s: existing request was duplicated", t.Name)
	}
	if t.Substantial && t.PrimaryAssociations != 1 {
		return fmt.Errorf("%s: want exactly one primary association, got %d", t.Name, t.PrimaryAssociations)
	}
	if t.EvidenceViolations != 0 {
		return fmt.Errorf("%s: %d evidence violations", t.Name, t.EvidenceViolations)
	}
	if t.ResponseBytes > maxResponseBytes {
		return fmt.Errorf("%s: response %d exceeds budget %d", t.Name, t.ResponseBytes, maxResponseBytes)
	}
	return nil
}
