package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTTLExpiry(t *testing.T) {
	c := NewTTL[string, int](time.Minute, 0)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatal("miss")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired entry returned")
	}
}

func TestTTLEviction(t *testing.T) {
	c := NewTTL[int, int](time.Minute, 2)
	c.Set(1, 1)
	c.Set(2, 2)
	c.Set(3, 3)
	if c.Len() != 2 {
		t.Fatalf("len %d", c.Len())
	}
}

func TestResponseCache(t *testing.T) {
	calls := 0
	h := NewResponseCache(time.Minute).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("hello"))
	}))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		if rec.Body.String() != "hello" {
			t.Fatal(rec.Body.String())
		}
		want := "HIT"
		if i == 0 {
			want = "MISS"
		}
		if rec.Header().Get("X-Cache") != want {
			t.Fatalf("call %d: X-Cache %q", i, rec.Header().Get("X-Cache"))
		}
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}
