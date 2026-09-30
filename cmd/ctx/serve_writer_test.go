package main

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestServeWriterDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want store.WriterConfig
	}{
		{nil, store.DefaultWriterConfig()},
		{[]string{"--writer-max-operations=32", "--writer-max-bytes=335544320", "--writer-max-batch=16", "--writer-read-connections=2"}, store.WriterConfig{MaxOperations: 32, MaxBytes: 335544320, MaxBatch: 16, ReadConnections: 2}},
	} {
		cfg, err := parseServeConfig(tc.args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		got := reflect.ValueOf(cfg).FieldByName("Writer")
		if !got.IsValid() || !reflect.DeepEqual(got.Interface(), tc.want) {
			t.Fatalf("writer configuration=%v want=%+v", got, tc.want)
		}
	}
}

func TestServeWriterRejectsInvalidBounds(t *testing.T) {
	for _, flag := range []string{"writer-max-operations", "writer-max-bytes", "writer-max-batch", "writer-read-connections"} {
		for _, value := range []string{"0", "-1"} {
			if _, err := parseServeConfig([]string{"--" + flag + "=" + value}, io.Discard); err == nil || !strings.Contains(err.Error(), "positive finite") {
				t.Errorf("%s=%s: %v", flag, value, err)
			}
		}
	}
	if _, err := parseServeConfig([]string{"--writer-max-bytes=167772159"}, io.Discard); err == nil || !strings.Contains(err.Error(), "snapshot-max-logical-bytes") {
		t.Fatalf("writer below snapshot bound: %v", err)
	}
}
