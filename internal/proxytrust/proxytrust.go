// Package proxytrust decides whether the peer of a connection is a reverse
// proxy whose Forwarded/X-Forwarded-* headers may be believed.
package proxytrust

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Set is the list of trusted proxy networks. Loopback peers are always
// trusted (a proxy on the same host); everything else must be listed.
type Set struct{ prefixes []netip.Prefix }

// Parse reads a comma- or space-separated list of CIDRs or single addresses.
// An empty string yields a Set that trusts loopback only.
func Parse(list string) (Set, error) {
	var s Set
	for _, field := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if p, err := netip.ParsePrefix(field); err == nil {
			s.prefixes = append(s.prefixes, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(field)
		if err != nil {
			return Set{}, fmt.Errorf("trusted proxy %q is neither a CIDR nor an IP address", field)
		}
		s.prefixes = append(s.prefixes, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return s, nil
}

// Trusts reports whether remoteAddr (host:port as in http.Request.RemoteAddr)
// is loopback or inside a configured network.
func (s Set) Trusts(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	for _, p := range s.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Configured reports whether any non-loopback network is trusted.
func (s Set) Configured() bool { return len(s.prefixes) > 0 }
