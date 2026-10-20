// Package embedtest is a fake embedding server for tests. Its vectors are
// deterministic: every word of a text, after mapping synonyms to one concept,
// adds one to a dimension chosen by the word, and the result is left for the
// client to normalise. Two texts are close when they share words or
// concepts, which is enough to build "found by meaning, not by words" cases.
package embedtest

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// Server is a running fake. The zero value of every knob answers normally.
type Server struct {
	*httptest.Server

	// Dim is the vector length, 64 when zero. Set before the first request.
	Dim int
	// Concepts maps a lower-case word to the concept it stands for; words with
	// the same concept share a dimension.
	Concepts map[string]string

	// Fail answers every embedding request with 500.
	Fail atomic.Bool
	// Hang holds every embedding request until the client gives up or the server
	// closes.
	Hang atomic.Bool
	// Reject, when positive, answers 400 for a request holding an input longer
	// than that many characters, as a server does for a text over its context.
	Reject atomic.Int64
	// Delay is how long an embedding request takes.
	delay atomic.Int64
	// failOn and zeroOn hold a piece of text: a request with an input that
	// contains it is answered with 500, or its vector for that input is all
	// zeros (a vector nobody can use). Set them with FailOn and ZeroOn.
	failOn, zeroOn atomic.Value

	mu       sync.Mutex
	requests [][]string
	current  int
	peak     int
	healthy  atomic.Bool
	closed   chan struct{}
}

// New starts a fake server and closes it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{closed: make(chan struct{})}
	s.healthy.Store(true)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		close(s.closed)
		s.Server.Close()
	})
	return s
}

// SetDelay makes every embedding request take at least d.
func (s *Server) SetDelay(d time.Duration) { s.delay.Store(int64(d)) }

// FailOn makes the server answer 500 to every request that has an input
// containing text; the empty string switches it off.
func (s *Server) FailOn(text string) { s.failOn.Store(text) }

// ZeroOn makes the vector of every input containing text all zeros; the empty
// string switches it off.
func (s *Server) ZeroOn(text string) { s.zeroOn.Store(text) }

func contains(v *atomic.Value, text string) bool {
	sub, _ := v.Load().(string)
	return sub != "" && strings.Contains(text, sub)
}

// SetHealthy changes the answer of /health.
func (s *Server) SetHealthy(ok bool) { s.healthy.Store(ok) }

// Requests returns the inputs of every embedding request so far, one slice per
// request.
func (s *Server) Requests() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = append([]string(nil), r...)
	}
	return out
}

// Calls is the number of embedding requests so far.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Inputs is the number of texts embedded so far.
func (s *Server) Inputs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		n += len(r)
	}
	return n
}

// Peak is the highest number of embedding requests that were open at once.
func (s *Server) Peak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		if !s.healthy.Load() {
			http.Error(w, `{"error":{"message":"loading model"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/embeddings":
		s.embeddings(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) embeddings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Input) == 0 {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, in.Input)
	s.current++
	s.peak = max(s.peak, s.current)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.current--
		s.mu.Unlock()
	}()

	if s.Hang.Load() {
		select {
		case <-r.Context().Done():
		case <-s.closed:
		}
		return
	}
	if d := time.Duration(s.delay.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		}
	}
	if s.Fail.Load() {
		http.Error(w, `{"error":{"message":"model crashed"}}`, http.StatusInternalServerError)
		return
	}
	if limit := s.Reject.Load(); limit > 0 {
		for _, text := range in.Input {
			if int64(utf8.RuneCountInString(text)) > limit {
				http.Error(w, `{"error":{"message":"input is too large to process"}}`, http.StatusBadRequest)
				return
			}
		}
	}
	for _, text := range in.Input {
		if contains(&s.failOn, text) {
			http.Error(w, `{"error":{"message":"model crashed on this input"}}`, http.StatusInternalServerError)
			return
		}
	}
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]item, len(in.Input))
	for i, text := range in.Input {
		v := s.Vector(text)
		if contains(&s.zeroOn, text) {
			v = make([]float32, len(v))
		}
		data[i] = item{Index: i, Embedding: v}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"model": in.Model, "object": "list", "data": data})
}

var prefixWords = map[string]bool{"task": true, "search": true, "result": true, "query": true, "title": true, "text": true, "none": true}

// Vector is the unnormalised vector the server returns for a text.
func (s *Server) Vector(text string) []float32 {
	dim := s.Dim
	if dim <= 0 {
		dim = 64
	}
	v := make([]float32, dim)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if prefixWords[word] {
			continue
		}
		if c, ok := s.Concepts[word]; ok {
			word = c
		}
		h := fnv.New32a()
		h.Write([]byte(word))
		v[int(h.Sum32())%dim]++
	}
	if allZero(v) {
		v[0] = 1
	}
	return v
}

func allZero(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

// Wait polls until cond is true or the timeout passes.
func Wait(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
	return cond()
}
