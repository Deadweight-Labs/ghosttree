package store

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestApplySessionDistillationIsGroundedAtomicAndIdempotent(t *testing.T) {
	s := openTest(t)
	sessionID, _ := s.UpsertSession(Session{Harness: "codex", ExternalID: "distill", Scope: scope.Axes{Project: "p"}})
	_ = s.AppendChunks(sessionID, []Chunk{{Seq: 4, Role: "assistant", Text: "We chose SQLite because one writer is enough.", Raw: `{}`}})
	items := []SessionDistilledItem{{Type: "decision", Title: "SQLite is sufficient", Body: "One writer is enough.", Quote: "chose SQLite", ChunkSeq: 4}}
	n, err := s.ApplySessionDistillation(sessionID, "digest", "v1", scope.Axes{Project: "p"}, items)
	if err != nil || n != 1 {
		t.Fatalf("inserted=%d err=%v", n, err)
	}
	n, err = s.ApplySessionDistillation(sessionID, "digest", "v1", scope.Axes{Project: "p"}, items)
	if err != nil || n != 0 {
		t.Fatalf("retry inserted=%d err=%v", n, err)
	}
	pending, _ := s.PendingKnowledge("", 10)
	if len(pending) != 1 || pending[0].Confidence != "quarantined" {
		t.Fatalf("pending=%+v", pending)
	}
	evidence, _ := s.EvidenceFor(pending[0].ID)
	if len(evidence) != 1 || evidence[0].ChunkSeq != 4 {
		t.Fatalf("evidence=%+v", evidence)
	}
	bad := []SessionDistilledItem{{Type: "note", Title: "hallucinated", Body: "x", Quote: "not in transcript", ChunkSeq: 4}}
	if _, err := s.ApplySessionDistillation(sessionID, "other", "v1", scope.Axes{Project: "p"}, bad); err == nil {
		t.Fatal("ungrounded item accepted")
	}
	if exists, _ := s.SessionDistillationExists(sessionID, "other", "v1"); exists {
		t.Fatal("failed distillation was checkpointed")
	}
}
