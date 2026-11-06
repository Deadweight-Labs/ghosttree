// Package api is ledgerly's public HTTP API (JSON over HTTP, versioned under
// /v1).
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/acme-billing/ledgerly/internal/cache"
	"github.com/acme-billing/ledgerly/internal/store"
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	store  *store.Store
	cache  *cache.ResponseCache
	logger *slog.Logger
	now    func() time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.logger = l } }

// WithClock sets the clock, for tests.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// NewServer builds the API server.
func NewServer(st *store.Store, opts ...Option) *Server {
	s := &Server{
		store:  st,
		cache:  cache.NewResponseCache(60 * time.Second),
		logger: slog.New(slog.DiscardHandler),
		now:    time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Handler returns the root HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		var h http.Handler = rt.Handler
		if rt.Cache {
			h = s.cache.Wrap(h)
		}
		mux.Handle(rt.Method+" "+rt.Pattern, h)
	}
	return s.recoverer(s.logRequests(mux))
}

// NewRouter is a shortcut for NewServer(st).Handler().
func NewRouter(st *store.Store) http.Handler { return NewServer(st).Handler() }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", s.now().Sub(start).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("panic", "value", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
