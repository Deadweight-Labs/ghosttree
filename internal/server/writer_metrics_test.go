package server

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestMetricsExposeRuntimeWriterAdmissionAndDrain(t *testing.T) {
	cfg := store.DefaultWriterConfig()
	cfg.MaxOperations = 1
	st, err := store.OpenRuntime(filepath.Join(t.TempDir(), "metrics.db"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	metrics := newMetricsRegistry(st, "writer-test")
	release := holdHTTPWriter(t, st)
	if _, err := st.UpsertSession(store.Session{Harness: "test", ExternalID: "rejected"}); !errors.Is(err, store.ErrWriterOperationsFull) {
		t.Fatal(err)
	}
	scrape := func() string {
		w := httptest.NewRecorder()
		metrics.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		return w.Body.String()
	}
	body := scrape()
	for _, want := range []string{
		"ghosttree_writer_running 1\n",
		"ghosttree_writer_draining 0\n",
		"ghosttree_writer_outstanding_operations 1\n",
		"ghosttree_writer_active_operations 1\n",
		"ghosttree_writer_queued_operations 0\n",
		"ghosttree_writer_operations_high_water 1\n",
		"ghosttree_writer_admitted_total 1\n",
		"ghosttree_writer_completed_total 0\n",
		"ghosttree_writer_rejected_total{reason=\"operations_full\"} 1\n",
		"ghosttree_writer_max_bytes 268435456\n",
		"ghosttree_reader_max_open_connections 3\n",
		"ghosttree_writer_queue_wait_seconds_count 1\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	release()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	body = scrape()
	for _, want := range []string{
		"ghosttree_writer_running 0\n",
		"ghosttree_writer_draining 0\n",
		"ghosttree_writer_outstanding_operations 0\n",
		"ghosttree_writer_completed_total 1\n",
		"ghosttree_writer_commit_duration_seconds_count 1\n",
		"ghosttree_writer_drain_total 1\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing after drain %q", want)
		}
	}
	if strings.Contains(body, "rejected\"") || strings.Contains(body, "barrier\"") {
		t.Error("payload identity leaked into metric labels")
	}
}
