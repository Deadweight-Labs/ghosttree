// Package proxytrust decides whether the peer of a connection is a reverse
// proxy whose Forwarded/X-Forwarded-* headers may be believed.
package proxytrust

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Set is the list of trusted proxy networks. Loopback peers are trusted by
// default (a proxy on the same host) unless the list contains "none";
// everything else must be listed.
type Set struct {
	prefixes   []netip.Prefix
	noLoopback bool
}

// Parse reads a comma- or space-separated list of CIDRs or single addresses.
// An empty string yields a Set that trusts loopback only. The word "none"
// switches the implicit loopback trust off (use it when local port forwards
// such as ssh -L, socat or Docker make loopback peers untrustworthy). Prefix
// length 0, IPv4-mapped prefixes and zoned addresses are rejected.
func Parse(list string) (Set, error) {
	var s Set
	for _, field := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if strings.EqualFold(field, "none") {
			s.noLoopback = true
			continue
		}
		if strings.Contains(field, "%") {
			return Set{}, fmt.Errorf("trusted proxy %q: zoned addresses are not supported", field)
		}
		if p, err := netip.ParsePrefix(field); err == nil {
			if p.Bits() == 0 {
				return Set{}, fmt.Errorf("trusted proxy %q: /0 would trust every client", field)
			}
			if p.Addr().Is4In6() {
				return Set{}, fmt.Errorf("trusted proxy %q: write IPv4 networks as plain IPv4 CIDRs", field)
			}
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
// is trusted loopback or inside a configured network.
func (s Set) Trusts(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return s.TrustsAddr(a)
}

// TrustsAddr is Trusts for an already parsed address.
func (s Set) TrustsAddr(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if a.IsLoopback() {
		return !s.noLoopback
	}
	for _, p := range s.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Client returns the client address for a request from remoteAddr with the
// given X-Forwarded-For header lines. XFF is only consulted when the direct
// peer is trusted. All lines are joined and read right to left: trusted
// proxies are skipped and the first untrusted address is the client. An
// unparsable entry stops the walk and the last address seen is used, so
// forged garbage cannot move the result further left. The result is the
// direct peer when nothing usable is found.
func (s Set) Client(remoteAddr string, xff []string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap().WithZone("")
	if !s.TrustsAddr(peer) {
		return peer.String()
	}
	current := peer
	parts := strings.Split(strings.Join(xff, ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil || a.Zone() != "" {
			break
		}
		a = a.Unmap()
		current = a
		if !s.TrustsAddr(a) {
			break
		}
	}
	return current.String()
}

// Configured reports whether any non-loopback network is trusted.
func (s Set) Configured() bool { return len(s.prefixes) > 0 }
