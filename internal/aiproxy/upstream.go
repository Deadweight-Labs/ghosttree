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
		return llm.NewHTTPClient(u, 0), nil
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("connection %s must use https", c.Name)
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	return &http.Client{Transport: &http.Transport{
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

// internalPrefixes are ranges that are not loopback or private by the
// standard library's reckoning but still internal: shared address space
// (carrier NAT, also used by overlay networks), benchmarking, the
// documentation ranges and IPv4 mapped or translated forms.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicAddress says whether an address is one an owner's connection may
// reach.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
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
