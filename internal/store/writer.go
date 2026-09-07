package store

import (
	"context"
	"errors"
	"sync"
)

var ErrWriterClosed = errors.New("store writer closed")

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
	next *writerRequest
	run  func() error
	done chan error
}

type runtimeWriter struct {
	mu         sync.Mutex
	ready      *sync.Cond
	head, tail *writerRequest
	closed     bool
	done       chan struct{}
}

func newRuntimeWriter(cfg WriterConfig) (*runtimeWriter, error) {
	w := &runtimeWriter{done: make(chan struct{})}
	w.ready = sync.NewCond(&w.mu)
	go w.work()
	return w, nil
}

func (w *runtimeWriter) admit(ctx context.Context, bytes int64, run func() error) (*writerRequest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrWriterClosed
	}
	r := &writerRequest{run: run, done: make(chan error, 1)}
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
		r.done <- r.run()
	}
}

func (w *runtimeWriter) close() {
	w.mu.Lock()
	w.closed = true
	w.ready.Broadcast()
	w.mu.Unlock()
	<-w.done
}
