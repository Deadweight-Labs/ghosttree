package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/config"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
)

// outage makes the first `down` connection attempts of the client fail with
// err, as a network that is briefly gone does, and then connects for real. It
// counts the attempts.
func outage(c *Client, down int32, err error) *atomic.Int32 {
	var tries atomic.Int32
	dialer := &net.Dialer{}
	c.http = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if tries.Add(1) <= down {
				return nil, err
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}}
	return &tries
}

func fastRetries(t *testing.T) {
	t.Helper()
	wait := readRetryWait
	readRetryWait = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { readRetryWait = wait })
}

func answeringServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// REQ-436 AC-6: a short outage of the network (a name that does not resolve in
// time, a connection that is refused) is waited out by a read with a growing
// pause, and given up after the repetitions the client was allowed.
func TestReadWaitsOutAShortOutageOfTheNetwork(t *testing.T) {
	fastRetries(t)
	srv := answeringServer(t)
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &net.OpError{Op: "connect", Err: syscall.ECONNREFUSED}}
	for _, tc := range []struct {
		name  string
		err   error
		tries int32
	}{
		{"connection refused", refused, 3},
		{"DNS timeout", &net.DNSError{Err: "i/o timeout", Name: "ghosttree.example.invalid", IsTimeout: true}, 3},
		{"DNS temporary failure", &net.DNSError{Err: "server misbehaving", Name: "ghosttree.example.invalid", IsTemporary: true}, 3},
		{"a name that does not exist", &net.DNSError{Err: "no such host", Name: "ghosttree.example.invalid", IsNotFound: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(config.Config{ServerURL: srv.URL})
			c.SetReadRetries(2)
			tries := outage(c, 2, tc.err)
			_, err := c.GetPain(1)
			if tc.tries == 3 && err != nil {
				t.Fatalf("the third try connects, the read must not fail: %v", err)
			}
			if tc.tries == 1 && err == nil {
				t.Fatal("a name that does not exist was waited out")
			}
			if tries.Load() != tc.tries {
				t.Fatalf("%d attempts, want %d", tries.Load(), tc.tries)
			}
		})
	}

	// More outage than repetitions: the client gives up with the network's
	// error, after exactly 1 + SetReadRetries attempts.
	c := New(config.Config{ServerURL: srv.URL})
	c.SetReadRetries(2)
	tries := outage(c, 100, refused)
	if _, err := c.GetPain(1); !errors.Is(err, syscall.ECONNREFUSED) || tries.Load() != 3 {
		t.Fatalf("giving up: %v after %d attempts", err, tries.Load())
	}

	// Without repetitions a read is tried once.
	c = New(config.Config{ServerURL: srv.URL})
	tries = outage(c, 1, refused)
	if _, err := c.GetPain(1); err == nil || tries.Load() != 1 {
		t.Fatalf("no retries set: %v after %d attempts", err, tries.Load())
	}
}

// A port nobody listens on refuses for real: the same waiting-out, no
// simulation of the error.
func TestReadGivesUpAfterARealRefusedConnection(t *testing.T) {
	fastRetries(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c := New(config.Config{ServerURL: "http://" + addr})
	c.SetReadRetries(2)
	var tries atomic.Int32
	dialer := &net.Dialer{}
	c.http = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		tries.Add(1)
		return dialer.DialContext(ctx, n, a)
	}}}
	if err := c.Health(); !errors.Is(err, syscall.ECONNREFUSED) || tries.Load() != 3 {
		t.Fatalf("health: %v after %d attempts", err, tries.Load())
	}
}

// A write is never repeated after a network error: nobody knows whether it
// arrived. A request creation with an idempotency key is the exception, the
// server answers its second try with the request the first one made.
func TestWritesAreRepeatedOnlyWithAnIdempotencyKey(t *testing.T) {
	fastRetries(t)
	srv := answeringServer(t)
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

	c := New(config.Config{ServerURL: srv.URL})
	c.SetReadRetries(2)
	tries := outage(c, 2, refused)
	if _, err := c.CreatePain("p", "t", "b", nil, 0); err == nil || tries.Load() != 1 {
		t.Fatalf("a pain is written once: %v after %d attempts", err, tries.Load())
	}

	c = New(config.Config{ServerURL: srv.URL})
	c.SetReadRetries(2)
	tries = outage(c, 2, refused)
	if _, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "x"}}); err == nil || tries.Load() != 1 {
		t.Fatalf("a request without a key is written once: %v after %d attempts", err, tries.Load())
	}

	c = New(config.Config{ServerURL: srv.URL})
	c.SetReadRetries(2)
	tries = outage(c, 2, refused)
	if _, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "x"}, IdempotencyKey: "k-1"}); err != nil || tries.Load() != 3 {
		t.Fatalf("a request with a key waits the outage out: %v after %d attempts", err, tries.Load())
	}

	// A client that was allowed no repetitions repeats nothing, key or not.
	c = New(config.Config{ServerURL: srv.URL})
	tries = outage(c, 1, refused)
	if _, err := c.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "change", Title: "x"}, IdempotencyKey: "k-2"}); err == nil || tries.Load() != 1 {
		t.Fatalf("no retries set: %v after %d attempts", err, tries.Load())
	}
}

// The commands take their repetitions from the process default; a client that
// sits in front of a person waiting (the hooks) never does.
func TestNewTakesTheDefaultRetriesAndNewWithTimeoutDoesNot(t *testing.T) {
	t.Cleanup(func() { SetDefaultReadRetries(0) })
	if c := New(config.Config{}); c.readRetries != 0 {
		t.Fatalf("default before it is set: %d", c.readRetries)
	}
	SetDefaultReadRetries(2)
	if c := New(config.Config{}); c.readRetries != 2 {
		t.Fatalf("New: %d", c.readRetries)
	}
	if c := NewWithTimeout(config.Config{}, time.Second); c.readRetries != 0 {
		t.Fatalf("NewWithTimeout repeats: %d", c.readRetries)
	}
}
