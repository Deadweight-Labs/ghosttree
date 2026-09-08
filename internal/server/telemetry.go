package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type requestIDGenerator func() (string, error)

type requestRecord struct {
	requestID    string
	actor        string
	route        string
	errorClass   string
	errorMessage string
}

type requestRecordKey struct{}

type observedResponse struct {
	http.ResponseWriter
	status int
	bytes  int64
	record *requestRecord
}

func (w *observedResponse) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *observedResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *observedResponse) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *observedResponse) recordError(class, message string) {
	if w.record == nil || w.record.errorClass != "" {
		return
	}
	w.record.errorClass = classifyRequestError(w.status, class, message)
	w.record.errorMessage = w.record.errorClass
}

type observedBody struct {
	io.ReadCloser
	bytes int64
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes += int64(n)
	return n, err
}

type errorRecorder interface {
	recordError(class, message string)
}

func recordResponseError(w http.ResponseWriter, class, message string) {
	if recorder, ok := w.(errorRecorder); ok {
		recorder.recordError(class, message)
	}
}

func requestRecordFromContext(ctx context.Context) *requestRecord {
	record, _ := ctx.Value(requestRecordKey{}).(*requestRecord)
	return record
}

func (a *api) telemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := a.requestIDGenerator()
		if err != nil || requestID == "" {
			requestID = fallbackOperationID()
		}
		record := &requestRecord{requestID: requestID, route: "unmatched"}
		r = r.WithContext(context.WithValue(r.Context(), requestRecordKey{}, record))
		response := &observedResponse{ResponseWriter: w, record: record}
		var body *observedBody
		if r.Body != nil {
			body = &observedBody{ReadCloser: r.Body}
			r.Body = body
		}
		w.Header().Set("X-Request-ID", requestID)
		started := time.Now()
		a.metrics.begin()
		defer func() {
			panicValue := recover()
			status := response.status
			if status == 0 {
				if panicValue != nil {
					status = http.StatusInternalServerError
				} else {
					status = http.StatusOK
				}
			}
			if panicValue != nil && record.errorClass == "" {
				record.errorClass = "server"
			}
			if record.errorClass == "" && status >= http.StatusBadRequest {
				record.errorClass = classifyRequestError(status, "", "")
			}
			requestBytes := int64(0)
			if body != nil {
				requestBytes = body.bytes
			}
			duration := time.Since(started)
			a.metrics.observe(requestObservation{
				Method: r.Method, Route: record.route, Status: status, Duration: duration,
				RequestBytes: requestBytes, ResponseBytes: response.bytes, ErrorClass: record.errorClass,
			})
			if r.URL.Path != "/api/health" && r.URL.Path != "/metrics" {
				attributes := []any{
					"event", "http_request",
					"request_id", record.requestID,
					"method", r.Method,
					"route", record.route,
					"path", r.URL.EscapedPath(),
					"status", status,
					"duration_ms", float64(duration.Microseconds()) / 1000,
					"request_bytes", requestBytes,
					"response_bytes", response.bytes,
					"remote_ip", remoteIP(r.RemoteAddr),
				}
				if record.actor != "" {
					attributes = append(attributes, "actor", record.actor)
				}
				if record.errorClass != "" {
					attributes = append(attributes, "error_class", record.errorClass)
				}
				if record.errorMessage != "" {
					attributes = append(attributes, "error_message", record.errorMessage)
				}
				a.logger.Info("http_request", attributes...)
			}
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		next.ServeHTTP(response, r)
	})
}

func (a *api) captureRoute(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if record := requestRecordFromContext(r.Context()); record != nil {
			record.route = routePattern(pattern)
		}
		mux.ServeHTTP(w, r)
	})
}

func routePattern(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if _, route, found := strings.Cut(pattern, " "); found {
		return route
	}
	return pattern
}

func remoteIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	if net.ParseIP(address) != nil {
		return address
	}
	return ""
}

func classifyRequestError(status int, class, message string) string {
	if strings.Contains(message, "SQLITE_BUSY") || strings.Contains(strings.ToLower(message), "database is locked") {
		return "sqlite_busy"
	}
	switch class {
	case "auth", "not_found", "client", "server", "sqlite_busy", "writer_busy":
		return class
	}
	switch {
	case status == http.StatusUnauthorized:
		return "auth"
	case status == http.StatusNotFound:
		return "not_found"
	case status >= http.StatusInternalServerError:
		return "server"
	default:
		return "client"
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
