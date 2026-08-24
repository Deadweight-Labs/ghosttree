// Package requestledger verifies captured agent traces against the request-ledger contract.
package requestledger

import "fmt"

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
