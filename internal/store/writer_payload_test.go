package store

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestReferencedPayloadCountsCompactVisibleOwnership(t *testing.T) {
	body := strings.Repeat("x", 1024)
	type nested struct {
		Items [][]*Chunk
		Raw   []byte
	}
	backing := make([]byte, 2, 4096)
	payload := nested{Items: [][]*Chunk{{{Raw: body}, {Raw: body}}}, Raw: backing}
	size, err := referencedPayloadBytes(payload)
	if err != nil {
		t.Fatal(err)
	}
	if size < 2+2*1024 {
		t.Fatalf("retained bytes undercounted: %d", size)
	}
	w, err := newRuntimeWriter(WriterConfig{MaxOperations: 2, MaxBytes: 2048, MaxBatch: 1, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if err := w.submit(t.Context(), size, func() error { t.Error("oversized payload ran"); return nil }); !errors.Is(err, ErrWriterOversized) {
		t.Fatal(err)
	}
	stringsWithSpare := []string{"small", body}
	size, err = referencedPayloadBytes(stringsWithSpare[:1])
	if err != nil || size >= 1024 {
		t.Fatalf("unpassed spare capacity was traversed: size=%d err=%v", size, err)
	}
}

func TestReferencedPayloadRejectsCyclesAndOverflow(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := referencedPayloadBytes(cyclic); !errors.Is(err, ErrWriterInvalidPayload) {
		t.Fatalf("cycle: %v", err)
	}
	counter := payloadCounter{bytes: math.MaxInt64 - 1}
	if err := counter.add(2); !errors.Is(err, ErrWriterInvalidPayload) {
		t.Fatalf("overflow: %v", err)
	}
	if _, err := referencedPayloadBytes(func() {}); !errors.Is(err, ErrWriterInvalidPayload) {
		t.Fatalf("unmeasurable closure: %v", err)
	}
}

func TestReferencedPayloadDoesNotCopyLargeBytes(t *testing.T) {
	raw := make([]byte, 8<<20)
	allocs := testing.AllocsPerRun(10, func() {
		size, err := referencedPayloadBytes(raw)
		if err != nil || size < int64(len(raw)) {
			t.Fatalf("size=%d err=%v", size, err)
		}
	})
	if allocs > 8 {
		t.Fatalf("unexpected allocations counting bytes: %.0f", allocs)
	}
}
