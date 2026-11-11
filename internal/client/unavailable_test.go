package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

const proxyErrorPage = `<!DOCTYPE html>
<html lang="en"><head><title>502 Bad Gateway</title><style>body{font-family:sans-serif}</style></head>
<body><h1>502 Bad Gateway</h1><p>The server returned an invalid or incomplete response.</p></body></html>`

// The page of a proxy whose server restarts used to reach the agent whole,
// markup and stylesheet included.
func TestAnErrorPageOfAProxyBecomesOneLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(proxyErrorPage))
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL})

	_, err := c.GetPain(1)
	if err == nil {
		t.Fatal("a 502 must be an error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "<>\n") || !strings.Contains(msg, "502 Bad Gateway") || !strings.Contains(msg, "Try again") {
		t.Fatalf("the read: %q", msg)
	}
	if strings.Contains(msg, "write") {
		t.Fatalf("a read says nothing about a write: %q", msg)
	}
	_, err = c.CreatePain("p", "t", "b", nil, 0)
	if err == nil || strings.ContainsAny(err.Error(), "<>\n") || !strings.Contains(err.Error(), "read the state before you repeat it") {
		t.Fatalf("the write: %v", err)
	}
}

func TestOnlyReadsAreRepeatedWhileTheServerIsNotThere(t *testing.T) {
	wait := readRetryWait
	readRetryWait = func(int) time.Duration { return 0 }
	defer func() { readRetryWait = wait }()

	var reads, writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(proxyErrorPage))
			return
		}
		if reads.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(proxyErrorPage))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(config.Config{ServerURL: srv.URL})
	if _, err := c.GetPain(1); err == nil || reads.Load() != 1 {
		t.Fatalf("without SetReadRetries a read is tried once: %v after %d tries", err, reads.Load())
	}
	reads.Store(0)
	c.SetReadRetries(2)
	if _, err := c.GetPain(1); err != nil || reads.Load() != 3 {
		t.Fatalf("the third try answers: %v after %d tries", err, reads.Load())
	}
	if _, err := c.CreatePain("p", "t", "b", nil, 0); err == nil || writes.Load() != 1 {
		t.Fatalf("a write is never repeated: %v after %d tries", err, writes.Load())
	}

	// A refusal of the server itself is an answer, not an absence.
	reads.Store(0)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		http.Error(w, "no such pain", http.StatusNotFound)
	}))
	defer refusing.Close()
	c = New(config.Config{ServerURL: refusing.URL})
	c.SetReadRetries(2)
	if _, err := c.GetPain(1); err == nil || reads.Load() != 1 {
		t.Fatalf("a 404 is not repeated: %v after %d tries", err, reads.Load())
	}
}

// A busy server answers with a JSON body and a code (an APIError, not a
// StatusError): search_busy and a 502, 503 or 504 with such a body are repeated by
// a read, as many times as SetReadRetries allows and not more. Another code, or a
// refusal that has a code but is not about being busy, is an answer.
func TestReadRetriesAnswersWithACode(t *testing.T) {
	wait := readRetryWait
	readRetryWait = func(n int) time.Duration { return 0 }
	defer func() { readRetryWait = wait }()

	cases := []struct {
		name   string
		status int
		body   string
		after  string
		tries  int32
	}{
		{"search_busy", http.StatusServiceUnavailable, `{"code":"search_busy","message":"too many searches at once; retry"}`, "", 3},
		{"502 with a code", http.StatusBadGateway, `{"code":"upstream","message":"x"}`, "", 3},
		{"504 with a code", http.StatusGatewayTimeout, `{"code":"upstream","message":"x"}`, "", 3},
		{"search_busy with Retry-After", http.StatusServiceUnavailable, `{"code":"search_busy","message":"x"}`, "1", 3},
		{"a refusal of the server", http.StatusConflict, `{"code":"state_conflict","message":"x"}`, "", 1},
		{"not found with a code", http.StatusNotFound, `{"code":"not_found","message":"x"}`, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tries atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tries.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if tc.after != "" {
					w.Header().Set("Retry-After", tc.after)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(config.Config{ServerURL: srv.URL})
			c.SetReadRetries(2)
			_, err := c.Search("word", "all", scope.Axes{}, 10)
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("the answer is an APIError, got %T %v", err, err)
			}
			if tries.Load() != tc.tries {
				t.Fatalf("%d tries, want %d", tries.Load(), tc.tries)
			}
			if tc.after != "" && ae.RetryAfterSeconds != 1 {
				t.Fatalf("Retry-After is not carried: %+v", ae)
			}
		})
	}

	// A write is not repeated whatever the code says.
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"code":"search_busy","message":"x"}`))
	}))
	defer srv.Close()
	c := New(config.Config{ServerURL: srv.URL})
	c.SetReadRetries(2)
	if _, err := c.CreatePain("p", "t", "b", nil, 0); err == nil || writes.Load() != 1 {
		t.Fatalf("a write is tried once: %v after %d tries", err, writes.Load())
	}
}

// The pause is the backoff, or the Retry-After of the server when that is longer,
// but never longer than maxRetryAfter.
func TestRetryPauseFollowsRetryAfterWithACap(t *testing.T) {
	wait := readRetryWait
	readRetryWait = func(n int) time.Duration { return time.Duration(n+1) * 100 * time.Millisecond }
	defer func() { readRetryWait = wait }()

	for _, tc := range []struct {
		err  error
		n    int
		want time.Duration
	}{
		{&APIError{Status: 503, Code: "search_busy"}, 0, 100 * time.Millisecond},
		{&APIError{Status: 503, Code: "search_busy", RetryAfterSeconds: 2}, 0, 2 * time.Second},
		{&APIError{Status: 503, Code: "search_busy", RetryAfterSeconds: 600}, 1, maxRetryAfter},
		{&StatusError{Status: 502}, 1, 200 * time.Millisecond},
	} {
		if got := retryPause(tc.err, tc.n); got != tc.want {
			t.Errorf("%v, repetition %d: pause %v, want %v", tc.err, tc.n, got, tc.want)
		}
	}
}
