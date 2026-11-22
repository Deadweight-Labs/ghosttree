package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteLimiterLimitsOneAccountAndNotTheOthers(t *testing.T) {
	l := newWriteLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < int(writeBurst); i++ {
		release, _, ok := l.admit("person:1", 1)
		if !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
		release()
	}
	if _, wait, ok := l.admit("person:1", 1); ok || wait <= 0 {
		t.Fatalf("over the burst: ok=%v wait=%v", ok, wait)
	}
	if _, _, ok := l.admit("person:2", 1); !ok {
		t.Fatal("another account must not be limited")
	}
	// Nach einer Sekunde sind wieder writeRate Einheiten da.
	now = now.Add(time.Second)
	if _, _, ok := l.admit("person:1", 1); !ok {
		t.Fatal("the bucket did not refill")
	}
}

func TestWriteLimiterCapsParallelRequestsAndWeighsBigBodies(t *testing.T) {
	l := newWriteLimiter()
	var releases []func()
	for i := 0; i < writeInflight; i++ {
		release, _, ok := l.admit("person:1", 1)
		if !ok {
			t.Fatalf("parallel request %d refused", i)
		}
		releases = append(releases, release)
	}
	if _, _, ok := l.admit("person:1", 1); ok {
		t.Fatal("more parallel writes than allowed")
	}
	releases[0]()
	if _, _, ok := l.admit("person:1", 1); !ok {
		t.Fatal("a finished request must free its place")
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader(strings.Repeat("a", 4<<20)))
	if cost := writeCost(r); cost < 8 {
		t.Fatalf("a 4 MiB body costs %v, want at least 8", cost)
	}
}

func TestAPIAnswers429WithRetryAfterWhenAccountWritesTooFast(t *testing.T) {
	srv, token := newTestServer(t)
	var last *http.Response
	for i := 0; i < int(writeBurst)+50; i++ {
		r, _ := http.NewRequest("POST", srv.URL+"/api/context/relevant", strings.NewReader(`{"q":"x"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		last = res
		if res.StatusCode == http.StatusTooManyRequests {
			break
		}
	}
	if last.StatusCode != http.StatusTooManyRequests || last.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d, retry-after %q; want 429 with Retry-After", last.StatusCode, last.Header.Get("Retry-After"))
	}
}
