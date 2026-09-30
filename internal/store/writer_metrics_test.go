package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestWriterStatisticsTrackReservationsFailuresAndDrain(t *testing.T) {
	cfg := DefaultWriterConfig()
	cfg.MaxOperations = 2
	cfg.MaxBytes = 20
	w, err := newRuntimeWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	release := holdRuntimeWriter(t, w, 7)
	failure := errors.New("transaction failure")
	second, err := w.admit(context.Background(), 5, func() error { return failure })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.admit(context.Background(), 0, func() error { return nil }); !errors.Is(err, ErrWriterOperationsFull) {
		t.Fatal(err)
	}
	if _, err := w.admit(context.Background(), 21, func() error { return nil }); !errors.Is(err, ErrWriterOversized) {
		t.Fatal(err)
	}
	if _, err := w.admitOwned(context.Background(), []any{make(chan int)}, func([]any) error { return nil }); !errors.Is(err, ErrWriterInvalidPayload) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.admit(ctx, 0, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s := w.stats()
	if s.OutstandingOperations != 2 || s.ActiveOperations != 1 || s.QueuedOperations != 1 || s.OutstandingBytes != 12 || s.ActiveBytes != 7 || s.QueuedBytes != 5 || s.BytesHighWater != 12 || s.OperationsHighWater != 2 {
		t.Fatalf("reservation statistics=%+v", s)
	}
	for _, reason := range []int{WriterRejectOperations, WriterRejectOversized, WriterRejectInvalidPayload, WriterRejectCanceled} {
		if s.Rejected[reason] != 1 {
			t.Errorf("reject reason=%d count=%d", reason, s.Rejected[reason])
		}
	}
	done := make(chan struct{})
	go func() { w.close(); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for !w.stats().Draining {
		if time.Now().After(deadline) {
			t.Fatal("drain transition missing")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("drain completed with accepted work blocked")
	default:
	}
	release()
	if err := <-second.done; !errors.Is(err, failure) {
		t.Fatal(err)
	}
	<-done
	w.close()
	s = w.stats()
	if s.Running || s.Draining || s.OutstandingOperations != 0 || s.OutstandingBytes != 0 || s.ActiveOperations != 0 || s.ActiveBytes != 0 || s.Admitted != 2 || s.Completed != 2 || s.Failed != 1 || s.CommitErrors != 1 || s.CommitDuration.Count != 2 || s.QueueWait.Count != 2 || s.DrainCount != 1 || s.DrainDuration <= 0 {
		t.Fatalf("final statistics=%+v", s)
	}
}

func TestWriterStatisticsContainOnlyFixedSizeState(t *testing.T) {
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Array:
			check(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Type)
			}
		case reflect.Map, reflect.Slice, reflect.String, reflect.Pointer, reflect.Interface:
			t.Fatalf("unbounded stats type %s", typ)
		}
	}
	check(reflect.TypeFor[WriterStats]())
	var h WriterHistogram
	bounds := WriterDurationBuckets()
	for _, value := range []float64{0, .005, .5, 31} {
		h.observe(value, bounds)
	}
	if h.Count != 4 || h.Sum != 31.505 || h.Buckets[0] != 1 || h.Buckets[3] != 2 || h.Buckets[8] != 3 || h.Buckets[11] != 3 {
		t.Fatalf("cumulative histogram=%+v", h)
	}
	copy := WriterDurationBuckets()
	copy[0] = 99
	if WriterDurationBuckets()[0] != .0001 {
		t.Fatal("caller changed shared histogram boundaries")
	}
}
