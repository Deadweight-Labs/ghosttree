package store

import (
	"path/filepath"
	"testing"
)

// A queued claim is first in, first out with a claim that follows: of the two,
// the first one takes the entries, whether or not anyone waited for it.
func TestQueuedContextDeliveriesAreClaimedBeforeALaterClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghosttree.db")
	s, err := OpenRuntime(path, DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.QueueContextDeliveries("person:1", "sess", DeliveredByBootstrap, []ContextDelivery{{KnowledgeID: 1}, {KnowledgeID: 2}})
	fresh, err := s.ClaimContextDeliveries("person:1", "sess", DeliveredByRelevance, []ContextDelivery{{KnowledgeID: 2}, {KnowledgeID: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].KnowledgeID != 3 {
		t.Fatalf("fresh = %v, want only entry 3: entry 2 was already taken by the queued claim", fresh)
	}
}
