package store

import (
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func TestRuntimeDistillationMutationsUseAdmission(t *testing.T) {
	for _, op := range []string{"session", "request", "release", "release_preview", "batch", "usage", "close"} {
		t.Run(op, func(t *testing.T) {
			s := runtimeDomainStore(t)
			axes := scope.Axes{Project: "p"}
			sid, err := s.UpsertSession(Session{Harness: "test", ExternalID: "distill", Scope: axes})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AppendChunks(sid, []Chunk{{Seq: 1, Text: "grounded quote", Raw: "raw"}}); err != nil {
				t.Fatal(err)
			}
			items := []DistillBatchItem{{CustomID: "one", SessionID: sid, Digest: "digest", PromptVersion: "v1"}}
			batch, err := s.RecordDistillBatch("original", "test-model", items)
			if err != nil {
				t.Fatal(err)
			}
			if op == "release" || op == "release_preview" {
				if _, err := s.ApplySessionDistillation(sid, "digest", "v1", axes, nil); err != nil {
					t.Fatal(err)
				}
			}
			count := func() int {
				t.Helper()
				var n int
				if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM session_distillations)+(SELECT COUNT(*) FROM knowledge)+(SELECT COUNT(*) FROM knowledge_evidence)+(SELECT COUNT(*) FROM requests)+(SELECT COUNT(*) FROM request_sightings)+(SELECT COUNT(*) FROM distill_batches)+(SELECT COUNT(*) FROM distill_batch_items)").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := count()
			write := func() error {
				switch op {
				case "session":
					_, err := s.ApplySessionDistillation(sid, "digest", "v1", axes, []SessionDistilledItem{{Type: "note", Title: "learned", Body: "body", Quote: "grounded quote", ChunkSeq: 1}})
					return err
				case "request":
					_, err := s.ApplyRequestDistillation(sid, "digest", "v1", axes, []DistilledRequest{{Type: "change", Title: "requested", Body: "body", Quote: "grounded quote", ChunkSeq: 1}})
					return err
				case "release", "release_preview":
					_, err := s.ReleaseDistillations("v1", axes, op == "release_preview")
					return err
				case "batch":
					_, err := s.RecordDistillBatch("new", "test-model", items)
					return err
				case "usage":
					return s.RecordDistillBatchUsage(batch, "one", 100, 20)
				case "close":
					return s.CloseDistillBatch(batch, "collected")
				}
				panic("unknown operation")
			}
			release := holdRuntimeWriter(t, s.writer, 0)
			if err := write(); !errors.Is(err, ErrWriterOperationsFull) {
				t.Errorf("%s bypassed admission: %v", op, err)
			}
			release()
			if after := count(); after != before {
				t.Fatalf("rejected effects before=%d after=%d", before, after)
			}
			prompt, completion, err := s.DistillBatchUsage(batch)
			if err != nil || prompt != 0 || completion != 0 {
				t.Fatalf("rejected usage=%d/%d %v", prompt, completion, err)
			}
			open, err := s.OpenDistillBatches()
			if err != nil || len(open) != 1 || open[0].State != "open" {
				t.Fatalf("rejected batch state: %+v %v", open, err)
			}
			if err := write(); err != nil {
				t.Fatalf("%s after release: %v", op, err)
			}
			if op == "session" || op == "request" {
				after := count()
				if err := write(); err != nil {
					t.Fatal(err)
				}
				if count() != after {
					t.Fatal("retry duplicated result or provenance")
				}
			}
		})
	}
}
