package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Staleness could only ever be argued from age. An entry that hits on every
// search aged exactly like one nobody ever needed, so the only rule anyone
// could write was "active plans older than 90 days" — and after a distiller run
// the bootstrap has hundreds of entries and no basis on which to rank them.
func TestKnowledgeUseIsRecordedOnDeliveryAndOnSearchHit(t *testing.T) {
	s := openTest(t)
	ax := scope.Axes{Project: "github.com/x/y"}
	used, err := s.InsertKnowledge(Knowledge{Type: "decision", Title: "Use SQLite",
		Body: "One writer is sufficient.", Scope: ax, Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	unused, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "Forgotten",
		Body: "Nobody looks this up.", Scope: ax, Confidence: "trusted"})
	if err != nil {
		t.Fatal(err)
	}

	// Delivery counts: the bootstrap puts the entry in front of an agent, which
	// is the same kind of use as a search hit and the more common one.
	if _, err := s.KnowledgeForContext(ax); err != nil {
		t.Fatal(err)
	}
	// A search hit counts on top of it.
	if _, err := s.SearchKnowledgeForContext("SQLite", ax, 10); err != nil {
		t.Fatal(err)
	}

	hits, lastUsed, err := s.KnowledgeUsage(used)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 || lastUsed == "" {
		t.Fatalf("used entry has %d hits and last_used_at %q, want 2 (one delivery, one search hit)", hits, lastUsed)
	}
	otherHits, otherLast, err := s.KnowledgeUsage(unused)
	if err != nil {
		t.Fatal(err)
	}
	// The second entry was delivered by the bootstrap too, but never matched a
	// search: an entry that is only ever shipped and never sought after is
	// exactly what a ranked bootstrap needs to tell apart from one that earns
	// its place.
	if otherHits != 1 || otherLast == "" {
		t.Fatalf("unused entry has %d hits, want the single delivery", otherHits)
	}
}

// The operator views must not create the very traffic they are meant to
// observe: a preview or an admin search would otherwise mark everything as
// recently used and erase the signal.
func TestOperatorViewsDoNotCountAsUse(t *testing.T) {
	s := openTest(t)
	ax := scope.Axes{Project: "github.com/x/y"}
	k, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "Quiet", Body: "b", Scope: ax, Confidence: "staged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeForActivatedPreview(ax, activation.Context{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchAllKnowledge("Quiet", ax, 10); err != nil {
		t.Fatal(err)
	}
	hits, lastUsed, err := s.KnowledgeUsage(k)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || lastUsed != "" {
		t.Fatalf("operator views recorded %d hits (last %q), want none", hits, lastUsed)
	}
}

func TestKnowledgeUnusedSinceListsTheNeverDeliveredFirst(t *testing.T) {
	s := openTest(t)
	ax := scope.Axes{Project: "github.com/x/y"}
	stale, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "Never used", Body: "b", Scope: ax, Confidence: "trusted"})
	fresh, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "Recently used", Body: "b", Scope: ax, Confidence: "trusted"})
	if _, err := s.db.Exec(`UPDATE knowledge SET last_used_at=?, hit_count=5 WHERE id=?`,
		"2026-08-20T00:00:00Z", fresh); err != nil {
		t.Fatal(err)
	}

	got, err := s.KnowledgeUnusedSince("2026-08-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != stale {
		t.Fatalf("unused = %+v, want only the entry that was never delivered", got)
	}
	// An entry last used before the cutoff counts as unused too.
	got, err = s.KnowledgeUnusedSince("2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("unused before a later cutoff = %d, want both", len(got))
	}
}
