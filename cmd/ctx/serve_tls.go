package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/acme/autocert"
)

const (
	envTLSACMEHost  = "GHOSTTREE_TLS_ACME_HOST"
	envTLSACMEDir   = "GHOSTTREE_TLS_ACME_DIR"
	envTLSACMEEmail = "GHOSTTREE_TLS_ACME_EMAIL"
)

// tlsConfig is the server's own TLS (DEC-55, the alternative to a reverse
// proxy): a certificate from Let's Encrypt for Host, fetched and renewed by
// autocert. The plain listener of --listen stays as it is, on loopback, for the
// health check and for ctx on the same machine; HTTPSAddr and HTTPAddr are added.
type tlsConfig struct {
	Host      string // DNS name of the server; empty switches the built-in TLS off
	Dir       string // certificate cache, private
	Email     string // optional contact address for Let's Encrypt
	HTTPSAddr string
	HTTPAddr  string
}

func (c tlsConfig) enabled() bool { return c.Host != "" }

// validate checks the host the way a certificate needs it: a DNS name, no IP
// address, no port, no path.
func (c tlsConfig) validate() error {
	if !c.enabled() {
		return nil
	}
	h := c.Host
	if h != strings.ToLower(h) || strings.ContainsAny(h, ":/@ ") || net.ParseIP(h) != nil || !strings.Contains(h, ".") ||
		strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || strings.Contains(h, "..") {
		return fmt.Errorf("--tls-acme-host must be the DNS name of this server (like ghosttree.example.com), got %q: Let's Encrypt issues no certificate for an address or a name without a dot", h)
	}
	if c.Dir == "" {
		return errors.New("--tls-acme-dir is needed to keep the certificate")
	}
	return nil
}

// acmeServers are the two extra listeners of the built-in TLS.
type acmeServers struct {
	https, http *http.Server
	manager     *autocert.Manager
}

// newACMEServers builds them: HTTPS with certificates for Host only (any other
// name in the handshake is refused), and plain HTTP that answers the ACME
// challenge and redirects everything else to https.
func newACMEServers(c tlsConfig, handler http.Handler) (*acmeServers, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("--tls-acme-dir: %w", err)
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(c.Host),
		Cache:      autocert.DirCache(c.Dir),
		Email:      c.Email,
	}
	https := newHTTPServer(c.HTTPSAddr, handler)
	https.TLSConfig = m.TLSConfig()
	https.TLSConfig.MinVersion = tls.VersionTLS12
	return &acmeServers{https: https, http: newHTTPServer(c.HTTPAddr, m.HTTPHandler(nil)), manager: m}, nil
}

// serveAllUntilCanceled runs the main listener and, when the built-in TLS is on,
// its two listeners, and stops all of them when ctx ends or one fails.
func serveAllUntilCanceled(ctx context.Context, main *http.Server, extra *acmeServers) error {
	if extra == nil {
		return serveUntilCanceled(ctx, main)
	}
	inner, stop := context.WithCancel(ctx)
	defer stop()
	errs := make(chan error, 3)
	go func() { errs <- serveUntilCanceled(inner, main) }()
	go func() { errs <- serveUntilCanceled(inner, extra.http) }()
	go func() { errs <- serveTLSUntilCanceled(inner, extra.https) }()
	var first error
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
		stop()
	}
	return first
}

// serveTLSUntilCanceled is serveUntilCanceled for a server whose certificates
// come from its TLSConfig.
func serveTLSUntilCanceled(ctx context.Context, srv *http.Server) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	return serveWithUntilCanceled(ctx, srv, func() error { return srv.Serve(tls.NewListener(ln, srv.TLSConfig)) })
}
