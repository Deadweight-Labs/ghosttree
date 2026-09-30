package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrWriterClosed         = errors.New("store writer closed")
	ErrWriterOperationsFull = errors.New("store writer operations full; retry later")
	ErrWriterBytesFull      = errors.New("store writer bytes full; retry later")
	ErrWriterOversized      = errors.New("store writer operation exceeds byte limit")
	ErrWriterInvalidConfig  = errors.New("invalid store writer configuration")
	ErrWriterInvalidPayload = errors.New("invalid store writer payload size")
)

type WriterConfig struct {
	MaxOperations   int
	MaxBytes        int64
	MaxBatch        int
	ReadConnections int
}

func DefaultWriterConfig() WriterConfig {
	return WriterConfig{MaxOperations: 1024, MaxBytes: 256 << 20, MaxBatch: 64, ReadConnections: 3}
}

type writerRequest struct {
	enqueued time.Time
	next     *writerRequest
	run      func() error
	done     chan error
	bytes    int64
	chunks   *ChunkBatch
}

type runtimeWriter struct {
	metrics                                        WriterStats
	running                                        bool
	activeOperations                               int
	activeBytes                                    int64
	drainStarted                                   time.Time
	mu                                             sync.Mutex
	ready                                          *sync.Cond
	head, tail                                     *writerRequest
	closed                                         bool
	done                                           chan struct{}
	cfg                                            WriterConfig
	operations                                     int
	bytes                                          int64
	chunkWrite                                     func([]ChunkBatch) error
	bestEffortWrite                                func(int, bestEffortBatch) error
	bestEffort                                     [bestEffortKinds]bestEffortBatch
	bestEffortDrops                                [bestEffortKinds]atomic.Uint64
	batches, batchedOperations, fallbackOperations uint64
}

func newRuntimeWriter(cfg WriterConfig) (*runtimeWriter, error) {
	if cfg.MaxOperations <= 0 || cfg.MaxBytes <= 0 || cfg.MaxBatch <= 0 || cfg.ReadConnections <= 0 {
		return nil, ErrWriterInvalidConfig
	}
	w := &runtimeWriter{done: make(chan struct{}), cfg: cfg, running: true}
	w.ready = sync.NewCond(&w.mu)
	go w.work()
	return w, nil
}

func (w *runtimeWriter) admit(ctx context.Context, bytes int64, run func() error) (*writerRequest, error) {
	return w.admitPrepared(ctx, bytes, func(r *writerRequest) { r.run = run })
}

func (w *runtimeWriter) admitPrepared(ctx context.Context, bytes int64, prepare func(*writerRequest)) (*writerRequest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, w.rejectLocked(ErrWriterClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, w.rejectLocked(err)
	}
	if bytes < 0 {
		return nil, w.rejectLocked(ErrWriterInvalidPayload)
	}
	if bytes > w.cfg.MaxBytes {
		return nil, w.rejectLocked(ErrWriterOversized)
	}
	if w.operations >= w.cfg.MaxOperations {
		return nil, w.rejectLocked(ErrWriterOperationsFull)
	}
	if bytes > w.cfg.MaxBytes-w.bytes {
		return nil, w.rejectLocked(ErrWriterBytesFull)
	}
	r := &writerRequest{done: make(chan error, 1), bytes: bytes}
	w.operations++
	w.bytes += bytes
	w.metrics.Admitted++
	w.metrics.OperationsHighWater = max(w.metrics.OperationsHighWater, w.operations)
	w.metrics.BytesHighWater = max(w.metrics.BytesHighWater, w.bytes)
	prepare(r)
	r.enqueued = time.Now()
	if w.tail == nil {
		w.head = r
	} else {
		w.tail.next = r
	}
	w.tail = r
	w.ready.Signal()
	return r, nil
}

func (w *runtimeWriter) submit(ctx context.Context, bytes int64, run func() error) error {
	r, err := w.admit(ctx, bytes, run)
	if err != nil {
		return err
	}
	return <-r.done
}

func (w *runtimeWriter) work() {
	defer func() {
		w.mu.Lock()
		w.running = false
		if w.closed {
			w.metrics.DrainDuration = time.Since(w.drainStarted)
		}
		w.mu.Unlock()
		close(w.done)
	}()
	for {
		w.mu.Lock()
		for w.head == nil && w.nextBestEffort() < 0 && !w.closed {
			w.ready.Wait()
		}
		if kind := w.nextBestEffort(); w.head == nil && kind >= 0 {
			w.bestEffort[kind].active = true
			batch := w.bestEffort[kind]
			w.mu.Unlock()
			w.runBestEffort(kind, batch)
			continue
		}
		if w.head == nil {
			w.mu.Unlock()
			return
		}
		r := w.pop()
		group := []*writerRequest{r}
		if r.chunks != nil {
			for len(group) < w.cfg.MaxBatch && w.head != nil && w.head.chunks != nil {
				group = append(group, w.pop())
			}
		}
		w.mu.Unlock()
		if r.chunks != nil {
			w.writeChunkGroup(group)
		} else {
			started := time.Now()
			err := r.run()
			w.observeCommit(started, err)
			w.complete(r, err)
		}
	}
}

func (w *runtimeWriter) pop() *writerRequest {
	r := w.head
	w.activeOperations++
	w.activeBytes += r.bytes
	w.metrics.QueueWait.observe(time.Since(r.enqueued).Seconds(), WriterDurationBuckets())
	w.head = r.next
	r.next = nil
	if w.head == nil {
		w.tail = nil
	}
	return r
}

func (w *runtimeWriter) complete(r *writerRequest, err error) {
	r.run = nil
	r.chunks = nil
	w.mu.Lock()
	w.operations--
	w.activeOperations--
	w.activeBytes -= r.bytes
	w.metrics.Completed++
	if err != nil {
		w.metrics.Failed++
	}
	w.bytes -= r.bytes
	w.mu.Unlock()
	r.done <- err
}

func (w *runtimeWriter) close() {
	w.mu.Lock()
	if !w.closed {
		w.drainStarted = time.Now()
		w.metrics.DrainCount++
	}
	w.closed = true
	w.ready.Broadcast()
	w.mu.Unlock()
	<-w.done
}
