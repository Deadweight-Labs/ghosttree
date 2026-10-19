package llm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// newHTTPClient builds the client for one request. https goes through the
// ordinary transport. Plain http is only ever allowed for a host on this
// machine (see checkBaseURL), and that check happens at load time while the
// connection happens later, so the transport repeats it at dial time: it
// resolves the name itself, refuses unless every answer is loopback, and
// connects to that exact address (no second lookup to rebind). It also ignores
// HTTP_PROXY and friends, which would carry the key off the machine in the
// clear. The Host header is left alone, so a proxy that routes by it still works.
func newHTTPClient(u *url.URL, timeout time.Duration) *http.Client {
	if u == nil || u.Scheme != "http" {
		return &http.Client{Timeout: timeout}
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			ips, err := loopbackTargets(addr)
			if err != nil {
				return nil, err
			}
			_, port, _ := net.SplitHostPort(addr)
			// A name can list ::1 and 127.0.0.1 while the server listens on
			// one of them, so try each address, all of them loopback.
			var last error
			for _, ip := range ips {
				conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}}
}

// loopbackTargets resolves addr's host now and returns its addresses, or an
// error if any answer is not loopback or nothing resolves.
func loopbackTargets(addr string) ([]net.IP, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if host == "localhost" {
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	} else if ips, err = resolveHost(host); err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("refusing plain http to %s: no address", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("refusing plain http to %s: resolves to %s, not loopback", host, ip)
		}
	}
	return ips, nil
}
