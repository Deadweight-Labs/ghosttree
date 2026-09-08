package store

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func TestWriterOwnsOnlyVisibleInput(t *testing.T) {
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	release := holdRuntimeWriter(t, w, 0)
	large := strings.Repeat("x", 8<<20)
	part := large[:4]
	backing := make([]byte, 8<<20)
	copy(backing, "data")
	view := backing[:4:4]
	sparse := make(map[string]string, 1<<16)
	sparse["key"] = part
	var result []any
	r, err := w.admitOwned(context.Background(), []any{part, view, sparse}, func(owned []any) error { result = owned; return nil })
	if err != nil {
		t.Fatal(err)
	}
	view[0] = '!'
	sparse["key"] = "changed"
	release()
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	if result[0].(string) != "xxxx" || string(result[1].([]byte)) != "data" || result[2].(map[string]string)["key"] != "xxxx" {
		t.Fatal("queued input changed with caller storage")
	}
	if unsafe.StringData(result[0].(string)) == unsafe.StringData(part) {
		t.Fatal("queued substring retains caller allocation")
	}
	if unsafe.SliceData(result[1].([]byte)) == unsafe.SliceData(view) || cap(result[1].([]byte)) != 4 {
		t.Fatal("queued bytes retain caller backing")
	}
}

func TestWriterDoesNotTraverseUnpassedSpareCapacity(t *testing.T) {
	large := strings.Repeat("unpassed", 1<<18)
	backing := []map[string]string{{"visible": "small"}, {"unpassed": large}}
	var group sync.WaitGroup
	group.Add(1)
	stop := make(chan struct{})
	go func() {
		defer group.Done()
		for {
			select {
			case <-stop:
				return
			default:
				backing[1]["unpassed"] = large
			}
		}
	}()
	defer func() { close(stop); group.Wait() }()
	for i := 0; i < 20; i++ {
		size, err := referencedPayloadBytes(backing[:1])
		if err != nil || size > 512 {
			t.Fatalf("size=%d err=%v", size, err)
		}
	}
}

func TestWriterRejectsBeforeCopying(t *testing.T) {
	cfg := DefaultWriterConfig()
	cfg.MaxOperations = 1
	w, err := newRuntimeWriter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	release := holdRuntimeWriter(t, w, 0)
	body := strings.Repeat("x", 8<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = w.admitOwned(context.Background(), []any{body}, func([]any) error { return nil })
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrWriterOperationsFull) {
		t.Fatal(err)
	}
	if after.TotalAlloc-before.TotalAlloc > 1<<20 {
		t.Fatal("copied large payload before rejecting admission")
	}
	release()
}

func TestWriterSizingRejectsUnsupportedBulkMapValuesWithoutCopy(t *testing.T) {
	input := map[string][8 << 20]byte{"large": {}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := referencedPayloadBytes(input)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrWriterInvalidPayload) {
		t.Fatalf("unsupported map: %v", err)
	}
	if after.TotalAlloc-before.TotalAlloc > 1<<20 {
		t.Fatal("copied map value solely to count it")
	}
}

func TestWriterOwnershipPreservesDefinedPointerType(t *testing.T) {
	type namedPointer *string
	value := "owned"
	input := namedPointer(&value)
	w, err := newRuntimeWriter(DefaultWriterConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	var got any
	r, err := w.admitOwned(t.Context(), []any{input}, func(p []any) error { got = p[0]; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
	pointer, ok := got.(namedPointer)
	if !ok {
		t.Fatalf("owned pointer type=%T, want namedPointer", got)
	}
	if pointer == input || *pointer != value {
		t.Fatal("defined pointer was not independently cloned")
	}
}
