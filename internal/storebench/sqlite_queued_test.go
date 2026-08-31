package storebench

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQueuedSQLiteMatchesCurrentSemantics(t *testing.T) {
	workload, expected := Generate(17, Scale{
		Projects: 1, Sessions: 4, ChunksPerSession: 3, GhostFiles: 8,
		GhostBodyBytes: 128, Documents: 2, DocumentRevisions: 2,
		DocumentBodyBytes: 256, MigrationArtifacts: 4, ReadEvery: 2,
	})
	backend, err := OpenQueuedSQLite(filepath.Join(t.TempDir(), "ghosttree.db"), QueueConfig{
		MaxOperations: 32, MaxBytes: 1 << 20, MaxBatch: 8,
		GatherWindow: time.Millisecond, ReadConnections: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx := context.Background()
	for _, operation := range workload.Operations {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatalf("%s %s: %v", operation.ID, operation.Kind, err)
		}
	}
	if err := backend.Verify(ctx, expected); err != nil {
		t.Fatal(err)
	}
	if got := backend.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("combined connections = %d, want 4", got)
	}
}

func TestCurrentAndQueuedSQLiteProduceEquivalentRunSummaries(t *testing.T) {
	workload, expected := Generate(117, Scale{Projects: 1, Sessions: 8, ChunksPerSession: 4,
		GhostFiles: 12, GhostBodyBytes: 128, Documents: 3, DocumentRevisions: 2,
		DocumentBodyBytes: 256, MigrationArtifacts: 8, ReadEvery: 3})
	current, err := OpenCurrentSQLite(filepath.Join(t.TempDir(), "current.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	queued, err := OpenQueuedSQLite(filepath.Join(t.TempDir(), "queued.db"), QueueConfig{
		MaxOperations: 64, MaxBytes: 1 << 20, MaxBatch: 8,
		GatherWindow: time.Millisecond, ReadConnections: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queued.Close()
	config := RunConfig{Concurrency: 8, ArrivalMultiplier: 10, RunID: "equivalence"}
	currentReport, err := Run(context.Background(), current, workload, expected, config)
	if err != nil {
		t.Fatal(err)
	}
	queuedReport, err := Run(context.Background(), queued, workload, expected, config)
	if err != nil {
		t.Fatal(err)
	}
	if currentReport.Operations != queuedReport.Operations || currentReport.Errors != 0 || queuedReport.Errors != 0 ||
		currentReport.VerificationError != "" || queuedReport.VerificationError != "" {
		t.Fatalf("current=%+v queued=%+v", currentReport, queuedReport)
	}
	for kind, currentKind := range currentReport.Kinds {
		queuedKind, ok := queuedReport.Kinds[kind]
		if !ok || currentKind.Operations != queuedKind.Operations || currentKind.Errors != queuedKind.Errors {
			t.Fatalf("kind %s: current=%+v queued=%+v", kind, currentKind, queuedKind)
		}
	}
}

func TestQueuedSQLiteBatchesSynchronizedChunkBurst(t *testing.T) {
	workload, expected := Generate(18, Scale{Projects: 1, Sessions: 6, ChunksPerSession: 2})
	backend, err := OpenQueuedSQLite(filepath.Join(t.TempDir(), "ghosttree.db"), QueueConfig{
		MaxOperations: 16, MaxBytes: 1 << 20, MaxBatch: 8,
		GatherWindow: 20 * time.Millisecond, ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	ctx := context.Background()
	var chunks []Operation
	var reads []Operation
	for _, operation := range workload.Operations {
		switch operation.Kind {
		case SessionUpsert:
			if err := backend.Execute(ctx, operation); err != nil {
				t.Fatal(err)
			}
		case ChunksAppend:
			chunks = append(chunks, operation)
		case SessionRead:
			reads = append(reads, operation)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, operation := range chunks {
		operation := operation
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := backend.Execute(ctx, operation); err != nil {
				t.Errorf("%s: %v", operation.ID, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	for _, operation := range reads {
		if err := backend.Execute(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	if err := backend.Verify(ctx, expected); err != nil {
		t.Fatal(err)
	}
	stats := backend.Stats()
	if stats.Batches == 0 || stats.BatchedOperations < 2 {
		t.Fatalf("stats = %+v, want a multi-operation batch", stats)
	}
}

func TestQueuedSQLiteRejectsWholeChunkBatchWhenOneSessionIsUnknown(t *testing.T) {
	backend, err := OpenQueuedSQLite(filepath.Join(t.TempDir(), "ghosttree.db"), QueueConfig{
		MaxOperations: 4, MaxBytes: 1 << 20, MaxBatch: 4, ReadConnections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	queued := backend.(*queuedSQLite)
	ctx := context.Background()
	if err := queued.Execute(ctx, Operation{ID: "session", Kind: SessionUpsert,
		Payload: SessionUpsertPayload{LogicalID: "known", ExternalID: "known", Project: "bench"}}); err != nil {
		t.Fatal(err)
	}
	results := queued.executeBatch(ctx, []Operation{
		{ID: "valid", Kind: ChunksAppend, Payload: ChunksAppendPayload{Session: "known",
			Chunks: []ChunkPayload{{Seq: 0, Role: "user", Text: "valid", Raw: "{}"}}}},
		{ID: "invalid", Kind: ChunksAppend, Payload: ChunksAppendPayload{Session: "missing",
			Chunks: []ChunkPayload{{Seq: 0, Role: "user", Text: "invalid", Raw: "{}"}}}},
	})
	if len(results) != 2 || results[0] == nil || results[1] == nil {
		t.Fatalf("results = %v, want both operations rejected", results)
	}
	chunks, err := queued.core.store.ReadSession(queued.core.sessions["known"], 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("valid sibling committed %d chunks", len(chunks))
	}
}
