package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discoveryClient(t *testing.T, handler http.HandlerFunc) (*oidcClient, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := newOIDCClient(OIDCConfig{Issuer: srv.URL, ClientID: "x", RedirectURL: "http://x/cb"})
	if err != nil {
		t.Fatal(err)
	}
	return c, &hits
}

func TestFailedDiscoveryIsRememberedBriefly(t *testing.T) {
	c, hits := discoveryClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 500) })
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if _, _, err := c.ready(context.Background()); err == nil {
			t.Fatal("discovery of a broken IdP succeeded")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d discovery requests within the cache window, want 1", n)
	}
	now = now.Add(discoveryFailTTL + time.Second)
	c.ready(context.Background())
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d discovery requests after the window, want 2", n)
	}
}

func TestHangingDiscoveryBlocksNobodyElse(t *testing.T) {
	release := make(chan struct{})
	c, hits := discoveryClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		http.Error(w, "late", 500)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.ready(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatal("discovery never started")
	}
	// The lock is free while the request hangs ...
	if !c.mu.TryLock() {
		t.Fatal("the client lock is held across the discovery request")
	}
	c.mu.Unlock()
	// ... and a second caller leaves with its own deadline, without a second request.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := c.ready(ctx); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("waiter: err=%v after %v", err, time.Since(start))
	}
	if hits.Load() != 1 {
		t.Fatalf("%d discovery requests, want 1", hits.Load())
	}
	close(release)
	wg.Wait()
}
