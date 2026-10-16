package llm

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientRefusesDialToNonLoopbackAtConnectTime(t *testing.T) {
	// The name passed the load-time check, then DNS answers differently.
	stubResolver(t, []net.IP{net.ParseIP("203.0.113.7")}, nil)
	u, _ := url.Parse("http://allgemein:18317/v1/messages")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), strings.NewReader("{}"))
	_, err := newHTTPClient(req.URL, 2*time.Second).Do(req)
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("want refusal for non-loopback answer, got %v", err)
	}
}

func TestHTTPClientRefusesMixedAnswerAtConnectTime(t *testing.T) {
	stubResolver(t, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("203.0.113.7")}, nil)
	if _, err := loopbackTargets("allgemein:1"); err == nil {
		t.Fatal("mixed answer must be refused")
	}
}

func TestHTTPClientConnectsToLoopbackNameKeepsHostHeaderAndIgnoresEnvProxy(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	// An environment proxy that would swallow the request if it were honoured.
	proxyHit := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHit = true }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	stubResolver(t, []net.IP{net.ParseIP("127.0.0.1")}, nil)
	req, _ := http.NewRequest(http.MethodGet, "http://allgemein:"+port+"/x", nil)
	resp, err := newHTTPClient(req.URL, 5*time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if proxyHit {
		t.Fatal("HTTP_PROXY from the environment was used for plain http")
	}
	if gotHost != "allgemein:"+port {
		t.Fatalf("Host header changed: %q", gotHost)
	}
}

func TestHTTPSClientKeepsDefaultTransport(t *testing.T) {
	u, _ := url.Parse("https://api.anthropic.com")
	if c := newHTTPClient(u, time.Second); c.Transport != nil {
		t.Fatal("https must use the default transport")
	}
}

func TestHTTPClientFallsBackToSecondLoopbackAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// ::1 first, where nothing listens on this port; the server is on 127.0.0.1.
	stubResolver(t, []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, nil)
	req, _ := http.NewRequest(http.MethodGet, "http://allgemein:"+port+"/x", nil)
	resp, err := newHTTPClient(req.URL, 5*time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
