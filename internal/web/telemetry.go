package web

import (
	"log/slog"
	"net/http"
	"time"
)

// WithLogger is the logger of the web interface: one http_request line per page
// or stream, as the API writes, and what goes wrong while searching.
func WithLogger(l *slog.Logger) Option { return func(a *app) { a.logger = l } }

func (a *app) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// loggedRoutes are the routes of the interface that write an http_request line.
// Only routes whose path holds nothing secret belong here: others carry codes
// in the path (/join/{code}) or in the form (/ui/login/code).
var loggedRoutes = map[string]bool{"GET /ui/search": true, "GET /ui/search/stream": true}

// observed writes one http_request line when a request is done: the route, the
// path without the query (a search line stays out of the log), the status and
// how long it took.
func (a *app) observed(pattern string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		sw := &observedWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			a.log().Info("http_request", "event", "http_request", "method", r.Method, "route", pattern, "path", r.URL.EscapedPath(),
				"status", status, "duration_ms", float64(time.Since(started).Microseconds())/1000, "response_bytes", sw.bytes)
		}()
		h.ServeHTTP(sw, r)
	})
}

// observedWriter remembers the status and size of an answer; it passes Flush on,
// which the streams need.
type observedWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *observedWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *observedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *observedWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
