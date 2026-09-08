package store

import (
	"context"
	"errors"
	"sync"
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
	next  *writerRequest
	run   func() error
	done  chan error
	bytes int64
}

type runtimeWriter struct {
	mu         sync.Mutex
	ready      *sync.Cond
	head, tail *writerRequest
	closed     bool
	done       chan struct{}
	cfg        WriterConfig
	operations int
	bytes      int64
}

func newRuntimeWriter(cfg WriterConfig) (*runtimeWriter, error) {
	if cfg.MaxOperations <= 0 || cfg.MaxBytes <= 0 || cfg.MaxBatch <= 0 || cfg.ReadConnections <= 0 {
		return nil, ErrWriterInvalidConfig
	}
	w := &runtimeWriter{done: make(chan struct{}), cfg: cfg}
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
		return nil, ErrWriterClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bytes < 0 {
		return nil, ErrWriterInvalidPayload
	}
	if bytes > w.cfg.MaxBytes {
		return nil, ErrWriterOversized
	}
	if w.operations >= w.cfg.MaxOperations {
		return nil, ErrWriterOperationsFull
	}
	if bytes > w.cfg.MaxBytes-w.bytes {
		return nil, ErrWriterBytesFull
	}
	r := &writerRequest{done: make(chan error, 1), bytes: bytes}
	w.operations++
	w.bytes += bytes
	prepare(r)
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
	defer close(w.done)
	for {
		w.mu.Lock()
		for w.head == nil && !w.closed {
			w.ready.Wait()
		}
		if w.head == nil {
			w.mu.Unlock()
			return
		}
		r := w.head
		w.head = r.next
		r.next = nil
		if w.head == nil {
			w.tail = nil
		}
		w.mu.Unlock()
		err := r.run()
		r.run = nil
		w.mu.Lock()
		w.operations--
		w.bytes -= r.bytes
		w.mu.Unlock()
		r.done <- err
	}
}

func (w *runtimeWriter) close() {
	w.mu.Lock()
	w.closed = true
	w.ready.Broadcast()
	w.mu.Unlock()
	<-w.done
}
