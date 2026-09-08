package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestClientPreservesWriterRetryMetadata(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{"code": "writer_busy", "message": "store writer is busy; retry after 1 second", "retryable": true})
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL})
	_, err := c.CreateDocument(store.Document{Project: "p", Slug: "retry", Kind: "spec", Title: "retry"}, "body", "message")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || apiErr.Code != "writer_busy" {
		t.Fatalf("typed writer error lost: %v", err)
	}
	raw, err := json.Marshal(apiErr)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["retryable"] != true || fields["retry_after_seconds"] != float64(1) {
		t.Fatalf("retry metadata lost: %s", raw)
	}
	if calls.Load() != 1 {
		t.Fatalf("client silently repeated mutation %d times", calls.Load())
	}
}
