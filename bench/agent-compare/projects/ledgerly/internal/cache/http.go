package cache

import (
	"bytes"
	"net/http"
	"strconv"
	"time"
)

type response struct {
	status int
	header http.Header
	body   []byte
}

// ResponseCache caches successful GET responses by URL.
type ResponseCache struct {
	store *TTL[string, response]
	ttl   time.Duration
}

// NewResponseCache creates a response cache with the given TTL.
func NewResponseCache(ttl time.Duration) *ResponseCache {
	return &ResponseCache{store: NewTTL[string, response](ttl, 4096), ttl: ttl}
}

// Purge drops all cached responses.
func (rc *ResponseCache) Purge() { rc.store.Purge() }

// Wrap returns a handler that serves GET responses from the cache. Only 200
// responses are stored. Responses carry X-Cache: HIT or MISS.
func (rc *ResponseCache) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		key := r.URL.RequestURI()
		if cached, ok := rc.store.Get(key); ok {
			for k, v := range cached.header {
				w.Header()[k] = append([]string(nil), v...)
			}
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(cached.status)
			_, _ = w.Write(cached.body)
			return
		}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set("X-Cache", "MISS")
		w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(int(rc.ttl.Seconds())))
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusOK {
			h := w.Header().Clone()
			h.Del("X-Cache")
			rc.store.Set(key, response{status: rec.status, header: h, body: rec.buf.Bytes()})
		}
	})
}

type recorder struct {
	http.ResponseWriter
	status      int
	buf         bytes.Buffer
	wroteHeader bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	r.buf.Write(p)
	return r.ResponseWriter.Write(p)
}
