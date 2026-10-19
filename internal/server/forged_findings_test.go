package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A member cannot forge the review gate: a distilled+quarantined entry with a
// session_ref to her own session is hers to release, never to verify, and a
// session_ref to another account's session is refused (REQ-438 D, review 1).
func TestForgedDistilledEntriesCannotBypassTheReviewGate(t *testing.T) {
	f := accessAPI(t, true)
	forge := func(ref string) store.Knowledge {
		return store.Knowledge{Type: "note", Title: "forged " + ref, Body: "b", Scope: scope.Axes{Project: accProject},
			Origin: "distilled", Confidence: "quarantined", SessionRef: ref}
	}
	own := fmt.Sprintf("session:%d#1", f.id["s-mia"])
	foreign := fmt.Sprintf("session:%d#1", f.id["s-rex"])
	f.expect(t, "mia", 403, "POST", "/api/knowledge", forge(foreign))
	f.expect(t, "mia", 403, "POST", "/api/knowledge", forge("session:999999#1"))

	// The migration endpoint takes the same rule.
	f.expect(t, "mia", 403, "POST", "/api/migrated-knowledge", store.MigratedEntry{Knowledge: forge(foreign)})

	var saved store.Knowledge
	if err := json.Unmarshal([]byte(f.expect(t, "mia", 200, "POST", "/api/knowledge", forge(own))), &saved); err != nil {
		t.Fatal(err)
	}
	path := idPath("/api/knowledge/%d", saved.ID)
	// Held: nobody else sees it, and the owner cannot verify it herself.
	f.expect(t, "rex", 404, "GET", path, nil)
	f.expect(t, "lena", 404, "GET", path, nil)
	f.expect(t, "robin", 404, "GET", path, nil)
	f.expect(t, "mia", 403, "PATCH", path, map[string]string{"confidence": "verified"})
	f.expect(t, "mia", 403, "PATCH", path, map[string]string{"confidence": "trusted"})
	// Releasing puts it into the reviewers' queue, staged and unverified.
	f.expect(t, "mia", 204, "PATCH", path, map[string]string{"confidence": "staged"})
	f.expect(t, "rex", 200, "GET", path, nil)
	f.expect(t, "mia", 403, "PATCH", path, map[string]string{"confidence": "verified"})
	f.expect(t, "lena", 204, "PATCH", path, map[string]string{"confidence": "verified"})
}
