package main

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuiltInTLSNeedsADNSNameAndKeepsItsDirectoryPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	for _, host := range []string{"192.0.2.10", "localhost", "Example.com", "gt.example.com:443", "https://gt.example.com", "gt..example.com", ".example.com"} {
		if err := (tlsConfig{Host: host, Dir: dir}).validate(); err == nil {
			t.Errorf("%q accepted", host)
		}
	}
	if err := (tlsConfig{Host: "gt.example.com", Dir: dir}).validate(); err != nil {
		t.Fatal(err)
	}
	if err := (tlsConfig{}).validate(); err != nil {
		t.Errorf("off must be valid: %v", err)
	}
	if _, err := newACMEServers(tlsConfig{Host: "gt.example.com", Dir: dir, HTTPSAddr: ":0", HTTPAddr: ":0"}, http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("certificate directory: %v %v", fi, err)
	}
}

func TestBuiltInTLSIssuesOnlyForItsOwnNameAndRedirectsPlainHTTP(t *testing.T) {
	s, err := newACMEServers(tlsConfig{Host: "gt.example.com", Dir: filepath.Join(t.TempDir(), "acme"), HTTPSAddr: ":0", HTTPAddr: ":0"}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	// A handshake for another name never reaches Let's Encrypt.
	if _, err := s.https.TLSConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example.org"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("another host: %v", err)
	}
	if s.https.TLSConfig.MinVersion < tls.VersionTLS12 {
		t.Error("TLS older than 1.2 allowed")
	}
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://gt.example.com/ui/?a=1", nil))
	if rec.Code < 300 || rec.Code > 399 || rec.Header().Get("Location") != "https://gt.example.com/ui/?a=1" {
		t.Errorf("plain http: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestServeConfigWithBuiltInTLSSetsThePublicURLAndRefusesAnotherOne(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	cfg, err := parseServeConfig([]string{"--db", db, "--tls-acme-host", "gt.example.com"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://gt.example.com" || cfg.TLS.Dir != filepath.Join(filepath.Dir(db), "acme") {
		t.Errorf("public url %q, dir %q", cfg.PublicURL, cfg.TLS.Dir)
	}
	if _, err := parseServeConfig([]string{"--db", db, "--tls-acme-host", "gt.example.com", "--public-url", "https://other.example.com"}, io.Discard); err == nil {
		t.Error("a public URL for another name was accepted")
	}
	if _, err := parseServeConfig([]string{"--db", db, "--tls-acme-host", "10.0.0.1"}, io.Discard); err == nil {
		t.Error("an IP address was accepted")
	}
	if cfg, err := parseServeConfig([]string{"--db", db}, io.Discard); err != nil || cfg.TLS.enabled() {
		t.Errorf("without a host TLS must stay off: %v", err)
	}
}

func TestServeAllStopsEveryListenerWhenTheContextEnds(t *testing.T) {
	s, err := newACMEServers(tlsConfig{Host: "gt.example.com", Dir: filepath.Join(t.TempDir(), "acme"), HTTPSAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	main := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveAllUntilCanceled(ctx, main, s) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the listeners did not stop")
	}
}

func TestServeAllReportsAListenerThatCannotStart(t *testing.T) {
	s, err := newACMEServers(tlsConfig{Host: "gt.example.com", Dir: filepath.Join(t.TempDir(), "acme"), HTTPSAddr: "127.0.0.1:0", HTTPAddr: "256.0.0.1:1"}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveAllUntilCanceled(context.Background(), newHTTPServer("127.0.0.1:0", http.NotFoundHandler()), s)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error for an address that cannot be bound")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("one failed listener left the others running")
	}
}
