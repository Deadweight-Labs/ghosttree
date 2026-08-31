package storebench

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrQueueFull    = errors.New("writer queue full")
	ErrWriterClosed = errors.New("writer closed")
)

type QueueConfig struct {
	MaxOperations   int
	MaxBytes        int64
	MaxBatch        int
	GatherWindow    time.Duration
	ReadConnections int
}

type batchExecutor func(context.Context, []Operation) []error

type writeRequest struct {
	operation Operation
	result    chan error
}

type writer struct {
	config  QueueConfig
	execute batchExecutor
	queue   chan *writeRequest

	mu                 sync.Mutex
	closed             bool
	reservedOperations int
	reservedBytes      int64
	wg                 sync.WaitGroup
	batches            atomic.Int64
	batchedOperations  atomic.Int64
}

func newWriter(config QueueConfig, execute batchExecutor) *writer {
	w := &writer{config: config, execute: execute, queue: make(chan *writeRequest, config.MaxOperations)}
	w.wg.Add(1)
	go w.run()
	return w
}

func (w *writer) Submit(ctx context.Context, operation Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if operation.PayloadBytes < 0 {
		return fmt.Errorf("payload bytes must not be negative")
	}
	request := &writeRequest{operation: operation, result: make(chan error, 1)}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWriterClosed
	}
	if w.reservedOperations >= w.config.MaxOperations || w.reservedBytes+operation.PayloadBytes > w.config.MaxBytes {
		w.mu.Unlock()
		return ErrQueueFull
	}
	w.reservedOperations++
	w.reservedBytes += operation.PayloadBytes
	w.queue <- request
	w.mu.Unlock()
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *writer) Close() error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	w.wg.Wait()
	return nil
}

func (w *writer) stats() (int64, int64) {
	return w.batches.Load(), w.batchedOperations.Load()
}

func (w *writer) run() {
	defer w.wg.Done()
	var carry *writeRequest
	for {
		request, ok := carry, carry != nil
		carry = nil
		if !ok {
			request, ok = <-w.queue
		}
		if !ok {
			return
		}
		group := []*writeRequest{request}
		if request.operation.Kind == ChunksAppend && w.config.MaxBatch > 1 {
			group, carry = w.gatherChunks(group)
		}
		operations := make([]Operation, len(group))
		for i, item := range group {
			operations[i] = item.operation
		}
		errs := w.execute(context.Background(), operations)
		if len(errs) != len(group) {
			err := fmt.Errorf("writer executor returned %d results for %d operations", len(errs), len(group))
			errs = make([]error, len(group))
			for i := range errs {
				errs[i] = err
			}
		}
		if len(group) > 1 {
			w.batches.Add(1)
			w.batchedOperations.Add(int64(len(group)))
		}
		for i, item := range group {
			w.complete(item, errs[i])
		}
	}
}

func (w *writer) gatherChunks(group []*writeRequest) ([]*writeRequest, *writeRequest) {
	deadline := time.Now().Add(w.config.GatherWindow)
	for len(group) < w.config.MaxBatch {
		select {
		case next, ok := <-w.queue:
			if !ok {
				return group, nil
			}
			if next.operation.Kind != ChunksAppend {
				return group, next
			}
			group = append(group, next)
			continue
		default:
		}
		if w.config.GatherWindow <= 0 {
			return group, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return group, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case next, ok := <-w.queue:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if !ok {
				return group, nil
			}
			if next.operation.Kind != ChunksAppend {
				return group, next
			}
			group = append(group, next)
		case <-timer.C:
			return group, nil
		}
	}
	return group, nil
}

func (w *writer) complete(request *writeRequest, err error) {
	w.mu.Lock()
	w.reservedOperations--
	w.reservedBytes -= request.operation.PayloadBytes
	w.mu.Unlock()
	request.result <- err
}
