package store

import (
	"context"
	"reflect"
)

func (w *runtimeWriter) admitChunks(ctx context.Context, batch ChunkBatch) (*writerRequest, error) {
	size, err := referencedPayloadBytes(batch)
	if err != nil {
		return nil, err
	}
	if w.chunkWrite == nil {
		return nil, ErrWriterInvalidConfig
	}
	return w.admitPrepared(ctx, size, func(r *writerRequest) {
		owned := cloneWriterValue(reflect.ValueOf(batch)).Interface().(ChunkBatch)
		r.chunks = &owned
	})
}

func (w *runtimeWriter) writeChunkGroup(group []*writerRequest) {
	batches := make([]ChunkBatch, len(group))
	for i, r := range group {
		batches[i] = *r.chunks
	}
	w.mu.Lock()
	w.batches++
	w.batchedOperations += uint64(len(group))
	w.mu.Unlock()
	err := w.chunkWrite(batches)
	if err != nil && len(group) > 1 {
		w.mu.Lock()
		w.fallbackOperations += uint64(len(group))
		w.mu.Unlock()
		for i, r := range group {
			oneErr := w.chunkWrite(batches[i : i+1])
			batches[i] = ChunkBatch{}
			w.complete(r, oneErr)
		}
		return
	}
	for i, r := range group {
		batches[i] = ChunkBatch{}
		w.complete(r, err)
	}
}
