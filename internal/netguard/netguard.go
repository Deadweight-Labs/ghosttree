// Package netguard keeps requests that people outside the server's operators
// can point somewhere (a connection of an organization, a page a workflow's
// research wants to read) away from the server's own network: only public
// addresses, resolved once and dialled exactly, without proxy variables.
package netguard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// internalPrefixes are ranges that the standard library counts as global
// unicast but that are still internal, reserved, or forms that embed an IPv4
// address the dialer would reach: shared address space (carrier NAT, also
// used by overlay networks), the IETF and benchmarking ranges, the
// documentation ranges, the reserved 240/4 with the broadcast address, NAT64
// (well-known and local-use), 6to4, Teredo, the deprecated site-local range,
// IPv4-compatible addresses (::a.b.c.d), discard-only and ORCHID.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// PublicAddress says whether an address is public: global unicast and in
// none of internalPrefixes.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range internalPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ErrNotPublic: a name resolved to an address that is not public.
var ErrNotPublic = errors.New("not a public address")

// PublicDialer dials only public addresses: it resolves the name itself,
// refuses unless every answer is public and connects to exactly that address,
// so a second lookup cannot rebind it.
func PublicDialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%s has no address", host)
		}
		for _, ip := range ips {
			if !PublicAddress(ip) {
				return nil, fmt.Errorf("%s resolves to %s: %w", host, ip, ErrNotPublic)
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
}

// PublicClient is an HTTP client that reaches public addresses only, also
// after a redirect (each hop is dialled through the same check), follows at
// most maxRedirects of them and ignores proxy variables, which would route
// around the check.
func PublicClient(timeout time.Duration, maxRedirects int) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return http.ErrUseLastResponse
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to %s refused", req.URL.Scheme)
			}
			return nil
		},
		Transport: &http.Transport{
			Proxy:                 nil,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: timeout,
			DialContext:           PublicDialer(15 * time.Second),
		},
	}
}
