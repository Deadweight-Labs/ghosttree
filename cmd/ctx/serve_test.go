package main

import (
	"net/http"
	"testing"
)

func TestHTTPServerHasTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("server timeouts are incomplete: %+v", srv)
	}
}
