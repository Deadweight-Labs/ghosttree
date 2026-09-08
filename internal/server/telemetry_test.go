package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func newAuditHandler(t *testing.T, logs *bytes.Buffer, options ...Option) (http.Handler, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, err := st.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	base := []Option{
		WithLogger(slog.New(slog.NewJSONHandler(logs, nil))),
		WithBuildVersion("test"),
		withRequestIDGenerator(func() (string, error) { return "request-1", nil }),
	}
	return New(st, append(base, options...)...), token
}

func decodeAuditEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode log %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestAuditLogsRequestsWithoutSensitiveInputs(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs)
	body := strings.NewReader(`{"type":"note","title":"safe","body":"BODY_SECRET"}`)
	r := httptest.NewRequest("POST", "/api/knowledge?query=QUERY_SECRET", body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.RemoteAddr = "100.96.1.2:4321"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); got != "request-1" {
		t.Fatalf("id = %q", got)
	}
	line := logs.String()
	for _, forbidden := range []string{"BODY_SECRET", "QUERY_SECRET", token, "Authorization"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("leaked %q: %s", forbidden, line)
		}
	}
	events := decodeAuditEvents(t, &logs)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %s", len(events), line)
	}
	event := events[0]
	if event["event"] != "http_request" || event["actor"] != "alice" || event["route"] != "/api/knowledge" || event["path"] != "/api/knowledge" || event["remote_ip"] != "100.96.1.2" {
		t.Fatalf("event = %#v", event)
	}
}

func TestAuditDoesNotLogBodyValuesFromHandlerErrors(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs)
	const bodySecret = "BODY_DERIVED_MISSING_PATH"
	r := httptest.NewRequest(http.MethodPost, "/api/ghosts/move", strings.NewReader(
		`{"project":"audit","from":"`+bodySecret+`","to":"destination.go"}`,
	))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if strings.Contains(logs.String(), bodySecret) {
		t.Fatalf("body-derived error value leaked into audit log: %s", logs.String())
	}
	events := decodeAuditEvents(t, &logs)
	if len(events) != 1 || events[0]["error_class"] != "client" {
		t.Fatalf("events = %#v", events)
	}
}

func TestAuditCountsBytesAndDefaultsStatusToOK(t *testing.T) {
	var logs bytes.Buffer
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAPI(st,
		WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))),
		withRequestIDGenerator(func() (string, error) { return "request-1", nil }),
	)
	a.metrics = newMetricsRegistry(st, "test")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
		_, _ = r.Body.Read(make([]byte, 32))
	})
	r := httptest.NewRequest("POST", "/api/test", strings.NewReader("payload"))
	w := httptest.NewRecorder()
	a.telemetry(inner).ServeHTTP(w, r)
	event := decodeAuditEvents(t, &logs)[0]
	if event["status"] != float64(http.StatusOK) || event["request_bytes"] != float64(7) || event["response_bytes"] != float64(5) {
		t.Fatalf("event = %#v", event)
	}
}

func TestAuditLogsExactlyOnceForUnauthorizedAndAuthenticatedNotFound(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs)

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/api/knowledge", nil))
	missingRequest := httptest.NewRequest("GET", "/api/does-not-exist", nil)
	missingRequest.Header.Set("Authorization", "Bearer "+token)
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, missingRequest)

	events := decodeAuditEvents(t, &logs)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2: %s", len(events), logs.String())
	}
	if unauthorized.Code != http.StatusUnauthorized || events[0]["error_class"] != "auth" || events[0]["route"] != "unmatched" {
		t.Fatalf("unauthorized response=%d event=%#v", unauthorized.Code, events[0])
	}
	if missing.Code != http.StatusNotFound || events[1]["error_class"] != "not_found" || events[1]["route"] != "unmatched" || events[1]["actor"] != "alice" {
		t.Fatalf("missing response=%d event=%#v", missing.Code, events[1])
	}
}

func TestAuditReplacesRawErrorsWithBoundedMessage(t *testing.T) {
	var logs bytes.Buffer
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAPI(st, WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	a.metrics = newMetricsRegistry(st, "test")
	message := strings.Repeat("界", 600)
	a.telemetry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusBadRequest, message)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/test", nil))
	event := decodeAuditEvents(t, &logs)[0]
	got, _ := event["error_message"].(string)
	if got != "client" || strings.Contains(logs.String(), message) {
		t.Fatalf("error_message = %q, logs = %s", got, logs.String())
	}
}

func TestAuditUsesFallbackRequestID(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs, withRequestIDGenerator(func() (string, error) {
		return "", errors.New("entropy unavailable")
	}))
	r := httptest.NewRequest("GET", "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); !strings.HasPrefix(got, "fallback-") {
		t.Fatalf("id = %q", got)
	}
	if got := decodeAuditEvents(t, &logs)[0]["request_id"]; got != w.Header().Get("X-Request-ID") {
		t.Fatalf("logged id = %q, header = %q", got, w.Header().Get("X-Request-ID"))
	}
}

func TestAuditExcludesHealthAndMetricsLogs(t *testing.T) {
	var logs bytes.Buffer
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAPI(st,
		WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))),
		withRequestIDGenerator(func() (string, error) { return "request-1", nil }),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", a.metrics)
	h := a.telemetry(a.auth(a.captureRoute(mux)))
	for _, path := range []string{"/api/health", "/metrics"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}
	}
	if events := decodeAuditEvents(t, &logs); len(events) != 0 {
		t.Fatalf("probe events = %#v", events)
	}
	metrics := httptest.NewRecorder()
	a.metrics.ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `ghosttree_http_requests_total{method="GET",route="/api/health",status="200"} 1`) {
		t.Fatalf("health probe was not measured:\n%s", metrics.Body.String())
	}
}

func TestAuditOverwritesCallerRequestID(t *testing.T) {
	var logs bytes.Buffer
	h, token := newAuditHandler(t, &logs)
	r := httptest.NewRequest("GET", "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Request-ID", "caller-controlled")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Request-ID"); got != "request-1" {
		t.Fatalf("id = %q", got)
	}
	if strings.Contains(logs.String(), "caller-controlled") {
		t.Fatalf("caller request ID leaked into audit: %s", logs.String())
	}
}

func TestAuditRecordsPanicsAndDecrementsInFlight(t *testing.T) {
	var logs bytes.Buffer
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAPI(st, WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	h := a.telemetry(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/test", nil))
	}()
	events := decodeAuditEvents(t, &logs)
	if len(events) != 1 || events[0]["status"] != float64(http.StatusInternalServerError) || events[0]["error_class"] != "server" {
		t.Fatalf("events = %#v", events)
	}
	metrics := httptest.NewRecorder()
	a.metrics.ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "ghosttree_http_requests_in_flight 0\n") {
		t.Fatalf("in-flight metric did not recover:\n%s", metrics.Body.String())
	}
}
