package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The search page and its stream write one http_request line with its
// route, path and duration, and never the search line; a stream can still flush.
func TestWebRequestsAreLoggedWithoutTheQuery(t *testing.T) {
	var logs bytes.Buffer
	a := &app{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	h := a.observed("GET /ui/search/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("the stream cannot flush")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: done\n\n"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ui/search/stream?q=secret+words", nil))
	line := logs.String()
	for _, want := range []string{`"msg":"http_request"`, `"route":"GET /ui/search/stream"`, `"path":"/ui/search/stream"`, `"status":200`, `"duration_ms":`} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %s: %s", want, line)
		}
	}
	if strings.Contains(line, "secret") {
		t.Errorf("the search line is in the log: %s", line)
	}
	// Only routes without secrets in their path are logged.
	for route := range loggedRoutes {
		if !strings.HasPrefix(route, "GET /ui/search") {
			t.Errorf("%s is logged", route)
		}
	}
}
