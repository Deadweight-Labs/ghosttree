package server

import (
	"net/http/httptest"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
)

func TestDeviceClientAddrAndBaseURLTrustBoundary(t *testing.T) {
	set, _ := proxytrust.Parse("192.0.2.0/24")
	a := &api{proxies: set}
	r := httptest.NewRequest("POST", "http://internal:8474/api/device", nil)
	r.RemoteAddr = "192.0.2.10:1"
	r.Header.Add("X-Forwarded-For", "9.9.9.9")
	r.Header.Add("X-Forwarded-For", "198.51.100.4")
	if got := a.clientAddr(r); got != "198.51.100.4" {
		t.Fatalf("clientAddr=%s", got)
	}
	r.RemoteAddr = "203.0.113.5:1"
	if got := a.clientAddr(r); got != "203.0.113.5" {
		t.Fatalf("untrusted peer clientAddr=%s", got)
	}

	r = httptest.NewRequest("POST", "http://internal:8474/x", nil)
	r.RemoteAddr = "192.0.2.10:1"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "gt.example.test")
	if got := a.requestBaseURL(r); got != "https://gt.example.test" {
		t.Fatalf("base=%s", got)
	}
	r.Header.Add("X-Forwarded-Proto", "http") // duplicate lines are not believed
	if got := a.requestBaseURL(r); got != "http://gt.example.test" {
		t.Fatalf("duplicate proto must fall back to the request scheme, got %s", got)
	}
}
