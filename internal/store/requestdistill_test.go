package store

import (
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func requestFilter(project string) requestdomain.SearchFilter {
	return requestdomain.SearchFilter{Scope: scope.Axes{Project: project}, Limit: 25}
}

func wishSession(t *testing.T, s *Store, external, said string, items []DistilledRequest) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "claude-code", ExternalID: external, Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Role: "user", Text: said, Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRequestDistillation(id, "d-"+external, "req-v1", scope.Axes{Project: "p"}, items); err != nil {
		t.Fatal(err)
	}
	return id
}

// A wish said in passing becomes a ledger entry with the words that produced it.
func TestDistilledWishLandsInTheLedgerWithItsQuote(t *testing.T) {
	s := openTest(t)
	wishSession(t, s, "a", "und exportieren als csv wär auch nice", []DistilledRequest{{
		Type: "feature", Title: "CSV-Export", Body: "Der Nutzer möchte exportieren können.",
		Quote: "exportieren als csv", ChunkSeq: 1}})

	page, err := s.SearchRequests(requestFilter("p"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("ledger = %+v, want one entry", page.Results)
	}
	got := page.Results[0].Request
	if got.Origin != "distilled" || got.State != "open" {
		t.Errorf("entry = %+v, want an open distilled request", got)
	}
	quotes, _ := s.RequestQuotes(got.ID)
	if len(quotes) != 1 || quotes[0].Quote != "exportieren als csv" {
		t.Errorf("quotes = %+v, want what was actually said", quotes)
	}
}

// Asking twice is the strongest signal that it was meant, so it must count
// rather than collapse into one indistinguishable entry.
func TestTheSameWishTwiceRaisesItsSightings(t *testing.T) {
	s := openTest(t)
	item := DistilledRequest{Type: "feature", Title: "CSV-Export", Body: "b", Quote: "export", ChunkSeq: 1}
	wishSession(t, s, "a", "export wär nice", []DistilledRequest{item})
	wishSession(t, s, "b", "wann kommt der export", []DistilledRequest{item})

	page, _ := s.SearchRequests(requestFilter("p"))
	if len(page.Results) != 1 {
		t.Fatalf("ledger = %d entries, want one", len(page.Results))
	}
	if n, _ := s.RequestSightings(page.Results[0].Request.ID); n != 2 {
		t.Errorf("sightings = %d, want 2 independent sessions", n)
	}
}

// A wish nobody voiced would become work somebody does.
func TestUngroundedWishIsRejectedWholesale(t *testing.T) {
	s := openTest(t)
	id, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "x", Scope: scope.Axes{Project: "p"}})
	_ = s.AppendChunks(id, []Chunk{{Seq: 1, Role: "user", Text: "mach mal weiter", Raw: `{}`}})
	if _, err := s.ApplyRequestDistillation(id, "d", "req-v1", scope.Axes{Project: "p"},
		[]DistilledRequest{{Type: "feature", Title: "Erfunden", Body: "b", Quote: "nicht gesagt", ChunkSeq: 1}}); err == nil {
		t.Fatal("ungrounded wish accepted")
	}
	page, _ := s.SearchRequests(requestFilter("p"))
	if len(page.Results) != 0 {
		t.Errorf("ledger = %+v, want nothing written", page.Results)
	}
}
