package store

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRuntimeWriterFIFOAndCompletion(t *testing.T) {
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	entered, release := make(chan struct{}), make(chan struct{})
	var order []int
	first, err := w.admit(context.Background(), 0, func() error {
		close(entered)
		<-release
		order = append(order, 0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	requests := []*writerRequest{first}
	for i := 1; i <= 8; i++ {
		r, err := w.admit(context.Background(), 0, func() error {
			order = append(order, i)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	for _, r := range requests {
		select {
		case <-r.done:
			t.Error("acknowledged before execution completed")
		default:
		}
	}
	close(release)
	for _, r := range requests {
		if err := <-r.done; err != nil {
			t.Fatal(err)
		}
	}
	if want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}; !reflect.DeepEqual(order, want) {
		t.Fatalf("execution order = %v, want %v", order, want)
	}
}

func holdRuntimeWriter(t *testing.T, w *runtimeWriter, bytes int64) func() {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := w.admit(context.Background(), bytes, func() error { close(entered); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(release)
			if err := <-r.done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(finish)
	return finish
}

func TestRuntimeWriterAdmissionCountsActive(t *testing.T) {
	for _, kind := range []string{"operations", "bytes", "oversized", "negative"} {
		t.Run(kind, func(t *testing.T) {
			cfg := DefaultWriterConfig()
			cfg.MaxOperations = 1
			cfg.MaxBytes = 10
			if kind != "operations" {
				cfg.MaxOperations = 3
			}
			w, err := newRuntimeWriter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(w.close)
			release := holdRuntimeWriter(t, w, 5)
			bytes := int64(1)
			switch kind {
			case "bytes":
				bytes = 6
			case "oversized":
				bytes = 11
			case "negative":
				bytes = -1
			}
			var ran atomic.Bool
			_, err = w.admit(context.Background(), bytes, func() error { ran.Store(true); return nil })
			if err == nil {
				t.Error("admitted operation past its limit")
			}
			release()
			w.close()
			if ran.Load() {
				t.Error("rejected callback executed")
			}
		})
	}
}

func TestRuntimeWriterCanceledBeforeAdmission(t *testing.T) {
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran bool
	err = w.submit(ctx, 0, func() error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
}

func TestRuntimeWriterConfigRejectsNonpositiveLimits(t *testing.T) {
	for i := 0; i < 8; i++ {
		cfg := DefaultWriterConfig()
		value := 0
		if i >= 4 {
			value = -1
		}
		switch i % 4 {
		case 0:
			cfg.MaxOperations = value
		case 1:
			cfg.MaxBytes = int64(value)
		case 2:
			cfg.MaxBatch = value
		case 3:
			cfg.ReadConnections = value
		}
		w, err := newRuntimeWriter(cfg)
		if err == nil {
			w.close()
			t.Errorf("accepted invalid config %+v", cfg)
		}
	}
}

func TestRuntimeWriterDrainConcurrentClose(t *testing.T) {
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	release := holdRuntimeWriter(t, w, 0)
	var committed atomic.Int64
	var pending []*writerRequest
	for i := 0; i < 32; i++ {
		r, err := w.admit(context.Background(), 1, func() error { committed.Add(1); return nil })
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, r)
	}
	closed := make(chan struct{}, 8)
	for i := 0; i < cap(closed); i++ {
		go func() { w.close(); closed <- struct{}{} }()
	}
	for {
		w.mu.Lock()
		stopping := w.closed
		w.mu.Unlock()
		if stopping {
			break
		}
		runtime.Gosched()
	}
	select {
	case <-closed:
		t.Error("Close returned before accepted work drained")
	default:
	}
	if err := w.submit(context.Background(), 0, func() error { t.Error("ran after close"); return nil }); !errors.Is(err, ErrWriterClosed) {
		t.Errorf("admission during drain: %v", err)
	}
	release()
	for i := 0; i < cap(closed); i++ {
		<-closed
	}
	for _, r := range pending {
		if err := <-r.done; err != nil {
			t.Fatal(err)
		}
	}
	if committed.Load() != 32 {
		t.Fatalf("committed %d", committed.Load())
	}
	w.close()
}

func TestRuntimeWriterReleasesReservationsAndTypedErrors(t *testing.T) {
	w, err := newRuntimeWriter(WriterConfig{MaxOperations: 2, MaxBytes: 10, MaxBatch: 1, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	release := holdRuntimeWriter(t, w, 5)
	for _, test := range []struct {
		size int64
		want error
	}{{6, ErrWriterBytesFull}, {11, ErrWriterOversized}, {-1, ErrWriterInvalidPayload}} {
		_, err := w.admit(context.Background(), test.size, func() error { return nil })
		if !errors.Is(err, test.want) {
			t.Errorf("size %d: %v, want %v", test.size, err, test.want)
		}
	}
	r, err := w.admit(context.Background(), 5, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.admit(context.Background(), 0, func() error { return nil }); !errors.Is(err, ErrWriterOperationsFull) {
		t.Errorf("full: %v", err)
	}
	release()
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	if err := w.submit(context.Background(), 10, func() error { return nil }); err != nil {
		t.Fatalf("reservation leaked: %v", err)
	}
	if _, err := newRuntimeWriter(WriterConfig{}); !errors.Is(err, ErrWriterInvalidConfig) {
		t.Fatalf("config: %v", err)
	}
}
