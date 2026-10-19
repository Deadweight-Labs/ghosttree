package store

import (
	"errors"
	"testing"
)

// The relaxed gate for a list without a project covers only knowledge and
// requests, whose lists cut themselves down to the caller's own global entries.
// Any other resource goes through the matrix as before (security review of PR
// #101, finding 6).
func TestGateListWithoutProjectIsRelaxedOnlyForKnowledgeAndRequests(t *testing.T) {
	st := accessFixture(t)
	st.SetAccessMode(AccessMode{Enforce: true})
	nora := st.Access(Principal{ID: "person:6", Label: "nora"})
	for _, res := range []Resource{ResKnowledge, ResRequest} {
		if err := nora.GateList("", res, true); err != nil {
			t.Errorf("%s: %v", res, err)
		}
	}
	for _, res := range []Resource{ResDocument, ResGhost, ResAgents, ResMembers, ResTranscript, ResSnapshot, ResRoom} {
		if err := nora.GateList("", res, true); !errors.Is(err, ErrAccessNotFound) {
			t.Errorf("%s without a project: %v, want not found", res, err)
		}
	}
}
