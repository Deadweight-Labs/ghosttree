package store

import (
	"context"
	"errors"
	"time"
)

const (
	WriterRejectClosed = iota
	WriterRejectOperations
	WriterRejectBytes
	WriterRejectOversized
	WriterRejectInvalidPayload
	WriterRejectCanceled
	WriterRejectInvalidConfig
	WriterRejectReasons
)

func WriterDurationBuckets() [12]float64 {
	return [12]float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .5, 1, 5, 30}
}
func WriterBatchBuckets() [12]float64 {
	return [12]float64{1, 2, 4, 8, 16, 32, 48, 64, 128, 256, 512, 1024}
}

type WriterHistogram struct {
	Buckets [12]uint64
	Count   uint64
	Sum     float64
}

func (h *WriterHistogram) observe(value float64, bounds [12]float64) {
	value = max(0, value)
	for i, bound := range bounds {
		if value <= bound {
			h.Buckets[i]++
		}
	}
	h.Count++
	h.Sum += value
}

type WriterBestEffortStats struct {
	Keys    int
	Bytes   int64
	Active  bool
	Dropped uint64
}

type WriterStats struct {
	Enabled                                                      bool
	Config                                                       WriterConfig
	Running, Draining                                            bool
	OutstandingOperations, ActiveOperations, QueuedOperations    int
	OutstandingBytes, ActiveBytes, QueuedBytes                   int64
	OperationsHighWater                                          int
	BytesHighWater                                               int64
	Admitted, Completed, Failed                                  uint64
	Rejected                                                     [WriterRejectReasons]uint64
	BestEffort                                                   [bestEffortKinds]WriterBestEffortStats
	QueueWait, CommitDuration, BatchSize                         WriterHistogram
	CommitErrors, Batches, BatchedOperations, FallbackOperations uint64
	DrainCount                                                   uint64
	DrainDuration                                                time.Duration
}

func (w *runtimeWriter) rejectLocked(err error) error {
	reason := -1
	switch {
	case errors.Is(err, ErrWriterClosed):
		reason = WriterRejectClosed
	case errors.Is(err, ErrWriterOperationsFull):
		reason = WriterRejectOperations
	case errors.Is(err, ErrWriterBytesFull):
		reason = WriterRejectBytes
	case errors.Is(err, ErrWriterOversized):
		reason = WriterRejectOversized
	case errors.Is(err, ErrWriterInvalidPayload):
		reason = WriterRejectInvalidPayload
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		reason = WriterRejectCanceled
	case errors.Is(err, ErrWriterInvalidConfig):
		reason = WriterRejectInvalidConfig
	}
	if reason >= 0 {
		w.metrics.Rejected[reason]++
	}
	return err
}

func (w *runtimeWriter) reject(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rejectLocked(err)
}

func (w *runtimeWriter) observeCommit(start time.Time, err error) {
	elapsed := time.Since(start)
	w.mu.Lock()
	w.metrics.CommitDuration.observe(elapsed.Seconds(), WriterDurationBuckets())
	if err != nil {
		w.metrics.CommitErrors++
	}
	w.mu.Unlock()
}

func (w *runtimeWriter) stats() WriterStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.metrics
	out.Enabled = true
	out.Config = w.cfg
	out.Running = w.running
	out.Draining = w.closed && w.running
	out.OutstandingOperations = w.operations
	out.OutstandingBytes = w.bytes
	out.ActiveOperations = w.activeOperations
	out.ActiveBytes = w.activeBytes
	out.QueuedOperations = w.operations - w.activeOperations
	out.QueuedBytes = w.bytes - w.activeBytes
	out.Batches = w.batches
	out.BatchedOperations = w.batchedOperations
	out.FallbackOperations = w.fallbackOperations
	for kind, batch := range w.bestEffort {
		out.BestEffort[kind] = WriterBestEffortStats{Keys: len(batch.usage) + len(batch.machines), Bytes: batch.bytes, Active: batch.active, Dropped: w.bestEffortDrops[kind].Load()}
	}
	if w.closed && w.running {
		out.DrainDuration = time.Since(w.drainStarted)
	}
	return out
}
