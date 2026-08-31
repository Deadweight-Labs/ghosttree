package storebench

import (
	"context"
	"errors"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type queuedSQLite struct {
	core   *currentSQLite
	reader *store.Store
	writer *writer
}

func OpenQueuedSQLite(path string, config QueueConfig) (Backend, error) {
	if config.MaxOperations <= 0 || config.MaxBytes <= 0 || config.MaxBatch <= 0 || config.ReadConnections <= 0 || config.GatherWindow < 0 {
		return nil, fmt.Errorf("queue limits, batch size, and read connections must be positive and gather window non-negative")
	}
	writable, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	reader, err := store.OpenReadOnly(path, config.ReadConnections)
	if err != nil {
		_ = writable.Close()
		return nil, err
	}
	core := &currentSQLite{store: writable, sessions: map[string]int64{}, documents: map[string]int64{}, migrations: map[string]int64{}}
	backend := &queuedSQLite{core: core, reader: reader}
	backend.writer = newWriter(config, backend.executeBatch)
	return backend, nil
}

func (b *queuedSQLite) Name() string { return "sqlite_queued" }

func (b *queuedSQLite) Execute(ctx context.Context, operation Operation) error {
	if readKind(operation.Kind) {
		return b.core.executeOn(ctx, b.reader, operation)
	}
	return b.writer.Submit(ctx, operation)
}

func (b *queuedSQLite) executeBatch(ctx context.Context, operations []Operation) []error {
	results := make([]error, len(operations))
	if len(operations) > 1 {
		batches := make([]store.ChunkBatch, len(operations))
		for i, operation := range operations {
			payload, ok := operation.Payload.(ChunksAppendPayload)
			if !ok {
				results[i] = fmt.Errorf("batched operation %s has payload %T", operation.ID, operation.Payload)
				continue
			}
			sessionID, err := b.core.id(b.core.sessions, payload.Session)
			if err != nil {
				results[i] = err
				continue
			}
			chunks := make([]store.Chunk, len(payload.Chunks))
			for n, chunk := range payload.Chunks {
				chunks[n] = store.Chunk{Seq: chunk.Seq, Role: chunk.Role, Text: chunk.Text, Raw: chunk.Raw}
			}
			batches[i] = store.ChunkBatch{SessionID: sessionID, Chunks: chunks}
		}
		for _, prepareErr := range results {
			if prepareErr != nil {
				for i := range results {
					results[i] = prepareErr
				}
				return results
			}
		}
		err := b.core.store.AppendChunkBatches(batches)
		for i := range results {
			results[i] = err
		}
		return results
	}
	results[0] = b.core.executeOn(ctx, b.core.store, operations[0])
	return results
}

func (b *queuedSQLite) Verify(ctx context.Context, expected Expected) error {
	return verifySQLite(ctx, b.reader, b.core.sessions, b.core.documents, b.core.migrations, expected)
}

func (b *queuedSQLite) Stats() BackendStats {
	writable := b.core.store.RuntimeStats()
	readers := b.reader.DB().Stats()
	batches, operations := b.writer.stats()
	return BackendStats{MaxOpenConnections: writable.DB.MaxOpenConnections + readers.MaxOpenConnections,
		WaitCount:      writable.DB.WaitCount + readers.WaitCount,
		WaitDurationNS: writable.DB.WaitDuration.Nanoseconds() + readers.WaitDuration.Nanoseconds(),
		DatabaseBytes:  writable.DatabaseBytes, WALBytes: writable.WALBytes, SHMBytes: writable.SHMBytes,
		Batches: batches, BatchedOperations: operations}
}

func (b *queuedSQLite) Close() error {
	return errors.Join(b.writer.Close(), b.reader.Close(), b.core.store.Close())
}

func readKind(kind Kind) bool {
	switch kind {
	case GhostRead, GhostTree, DocumentRead, MigrationRead, SessionRead:
		return true
	default:
		return false
	}
}
