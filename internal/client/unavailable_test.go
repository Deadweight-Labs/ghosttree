package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
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
