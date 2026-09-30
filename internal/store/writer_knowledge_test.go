package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/activation"
)

func TestRuntimeKnowledgeMutationsUseAdmission(t *testing.T) {
	for _, op := range []string{"insert", "activation", "update", "update_by", "staleness", "evidence", "regression", "backfill"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			kind := "pitfall"
			if op == "activation" {
				kind = "instruction"
			}
			if op == "staleness" {
				kind = "plan"
			}
			id, err := s.InsertKnowledge(Knowledge{Type: kind, Title: "original", Body: "body", Person: "author", ObservedAt: "2020-01-01T00:00:00Z"})
			if err != nil {
				t.Fatal(err)
			}
			sid, err := s.UpsertSession(Session{Harness: "test", ExternalID: "evidence"})
			if err != nil {
				t.Fatal(err)
			}
			if op == "backfill" {
				if _, err := s.db.Exec("UPDATE knowledge SET observed_at='' WHERE id=?", id); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.KnowledgeByID(id)
			if err != nil {
				t.Fatal(err)
			}
			write := func() error {
				switch op {
				case "insert":
					_, err := s.InsertKnowledge(Knowledge{Type: "note", Title: "new", Body: "new"})
					return err
				case "activation":
					return s.SetActivation(id, activation.Rule{Paths: []string{"internal/**"}})
				case "update":
					return s.UpdateKnowledge(id, map[string]string{"title": "edited"})
				case "update_by":
					return s.UpdateKnowledgeBy(id, map[string]string{"title": "edited"}, "editor")
				case "staleness":
					_, err := s.ApplyStaleness(time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("offset", 3600)), 24*time.Hour)
					return err
				case "evidence":
					return s.AddEvidence(id, []Evidence{{SessionID: sid, ChunkSeq: 1, Quote: "quote"}})
				case "regression":
					return s.SetRegressionCover(id, "covered", "TestSomething")
				case "backfill":
					_, err := s.BackfillObservedAt()
					return err
				}
				panic("unknown operation")
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			if err := write(); !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed queue: %v", op, err)
			}
			release()
			after, err := s.KnowledgeByID(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Title != before.Title || after.Status != before.Status || after.ObservedAt != before.ObservedAt || after.RegressionState != "" || len(after.Activation.Paths) != 0 {
				t.Fatalf("rejected %s changed knowledge", op)
			}
			var count int
			if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM knowledge)+(SELECT COUNT(*) FROM knowledge_evidence)+(SELECT COUNT(*) FROM knowledge_versions)").Scan(&count); err != nil || count != 1 {
				t.Fatalf("rejected effects=%d err=%v", count, err)
			}
			if err := write(); err != nil {
				t.Fatalf("%s after release: %v", op, err)
			}
		})
	}
}
