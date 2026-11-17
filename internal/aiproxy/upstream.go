package aiproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// UpstreamClient is the HTTP client for one call to a provider.
//
// A connection of the server (kept by the admin) may use https anywhere and
// plain http to this machine, as llm.json always could (a local model proxy).
//
// A connection of an organization or a profile is set by people who do not
// run the server. It must use https, and the address it resolves to must be
// public: never loopback, private, link-local or otherwise internal. Without
// that an owner could make the server send requests to services on its own
// network. The name is resolved once and the connection goes to exactly that
// address, so a second lookup cannot rebind it. Proxy variables are ignored,
// since they would route around the check.
func UpstreamClient(c store.AIConnection, u *url.URL) (*http.Client, error) {
	if c.OrgID == 0 {
		if err := llm.CheckBaseURL("connection "+c.Name, u.Scheme+"://"+u.Host); err != nil {
			return nil, err
		}
		c := llm.NewHTTPClient(u, 0)
		c.CheckRedirect = noRedirect
		return c, nil
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("connection %s must use https", c.Name)
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	return &http.Client{CheckRedirect: noRedirect, Transport: &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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
					return nil, fmt.Errorf("%s resolves to %s, which is not a public address", host, ip)
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
		},
	}}, nil
}

// noRedirect hands a redirect of the provider back as it is: following it
// would send the key to an address nobody entered.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

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

// PublicAddress says whether an address is one an owner's connection may
// reach: global unicast and in none of internalPrefixes.
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
