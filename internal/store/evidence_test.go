package store

import "testing"

func TestRecurrenceCountsDistinctSessions(t *testing.T) {
	s := openTest(t)
	kid, _ := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "ufw drops lan", Body: "b"})
	s1, _ := s.UpsertSession(Session{Harness: "claude-code", ExternalID: "a", StartedAt: "2026-08-23T00:00:00Z"})
	s2, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "b", StartedAt: "2026-08-23T00:00:00Z"})

	// Three findings, but only two independent sessions.
	if err := s.AddEvidence(kid, []Evidence{
		{SessionID: s1, ChunkSeq: 4, Quote: "ufw denied the packet"},
		{SessionID: s1, ChunkSeq: 9, Quote: "again after reboot"},
		{SessionID: s2, ChunkSeq: 2, Quote: "same on the other box"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Recurrence(kid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Recurrence = %d, want 2 (distinct sessions, not rows)", n)
	}
	if ev, _ := s.EvidenceFor(kid); len(ev) != 3 {
		t.Errorf("EvidenceFor = %d rows, want 3", len(ev))
	}
}

// A second distillation run over the same transcript must not inflate recurrence.
func TestAddEvidenceIsIdempotent(t *testing.T) {
	s := openTest(t)
	kid, _ := s.InsertKnowledge(Knowledge{Type: "note", Title: "t", Body: "b"})
	sid, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "x", StartedAt: "2026-08-23T00:00:00Z"})
	ev := []Evidence{{SessionID: sid, ChunkSeq: 1, Quote: "q"}}
	if err := s.AddEvidence(kid, ev); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvidence(kid, ev); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.EvidenceFor(kid); len(got) != 1 {
		t.Errorf("duplicate evidence stored %d rows, want 1", len(got))
	}
}
