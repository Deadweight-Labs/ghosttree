package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func distillSession(t *testing.T, s *Store, external, quote string, items []SessionDistilledItem) int64 {
	t.Helper()
	id, err := s.UpsertSession(Session{Harness: "codex", ExternalID: external, Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendChunks(id, []Chunk{{Seq: 1, Role: "assistant", Text: quote, Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionDistillation(id, "digest-"+external, "v4", scope.Axes{Project: "p"}, items); err != nil {
		t.Fatal(err)
	}
	return id
}

// The trust model rests on recurrence — a finding seen in several independent
// sessions is a defect, one seen once is an anecdote. Dropping a repeat as a
// duplicate threw that signal away: measured on production, 194 of 201 distilled
// entries had recurrence 1 and not one had 2.
func TestRepeatedFindingRaisesRecurrenceInsteadOfBeingDropped(t *testing.T) {
	s := openTest(t)
	item := SessionDistilledItem{Type: "pitfall", Title: "Redirects are validated too late",
		Body: "The client follows redirects before the host is checked.", Quote: "follows redirects", ChunkSeq: 1}
	distillSession(t, s, "a", "the client follows redirects first", []SessionDistilledItem{item})
	distillSession(t, s, "b", "again: it follows redirects before checking", []SessionDistilledItem{item})

	pending, _ := s.PendingKnowledge("", 10)
	if len(pending) != 1 {
		t.Fatalf("want one entry, got %d: %+v", len(pending), pending)
	}
	if n, _ := s.Recurrence(pending[0].ID); n != 2 {
		t.Errorf("recurrence = %d, want 2 independent sessions", n)
	}
}

// Exact titles only catch the easy half. The model is given the existing
// entries with their ids and can say which one a finding belongs to, which is
// what catches the same defect under a different name.
func TestModelCanAttachAFindingToAnExistingEntry(t *testing.T) {
	s := openTest(t)
	distillSession(t, s, "a", "the client follows redirects first", []SessionDistilledItem{{
		Type: "pitfall", Title: "Redirect validation occurs after the request",
		Body: "b", Quote: "follows redirects", ChunkSeq: 1}})
	pending, _ := s.PendingKnowledge("", 10)
	existing := pending[0].ID

	distillSession(t, s, "b", "redirected fetches are never revalidated", []SessionDistilledItem{{
		Type: "pitfall", Title: "Redirected fetches permit SSRF",
		Body: "Same defect, different words.", Quote: "never revalidated", ChunkSeq: 1, SameAs: existing}})

	pending, _ = s.PendingKnowledge("", 10)
	if len(pending) != 1 || pending[0].ID != existing {
		t.Fatalf("want the original entry only, got %+v", pending)
	}
	if n, _ := s.Recurrence(existing); n != 2 {
		t.Errorf("recurrence = %d, want 2", n)
	}
}

// A session that corroborated somebody else's finding must not take it down
// when it is reprocessed. Archiving keyed on evidence rather than authorship
// would retire an entry another session created.
func TestReprocessingDoesNotArchiveAnotherSessionsEntry(t *testing.T) {
	s := openTest(t)
	item := SessionDistilledItem{Type: "pitfall", Title: "Shared finding", Body: "b", Quote: "shared", ChunkSeq: 1}
	distillSession(t, s, "author", "shared thing", []SessionDistilledItem{item})
	second := distillSession(t, s, "witness", "shared thing again", []SessionDistilledItem{item})

	pending, _ := s.PendingKnowledge("", 10)
	if len(pending) != 1 {
		t.Fatalf("setup: %+v", pending)
	}
	original := pending[0].ID

	// The witness is released and processed again under a newer prompt.
	if _, err := s.ReleaseDistillations("v4", scope.Axes{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionDistillation(second, "digest-witness", "v5", scope.Axes{Project: "p"},
		[]SessionDistilledItem{item}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.KnowledgeByID(original)
	if after.Status != "active" {
		t.Errorf("the author's entry was archived by another session's rerun: %+v", after)
	}
	if n, _ := s.Recurrence(original); n != 2 {
		t.Errorf("recurrence = %d after reprocessing, want 2 — not inflated, not lost", n)
	}
}
