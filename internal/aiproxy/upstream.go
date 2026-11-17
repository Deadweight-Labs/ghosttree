package aiproxy

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/netguard"
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
	return &http.Client{CheckRedirect: noRedirect, Transport: &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		DialContext:           netguard.PublicDialer(30 * time.Second),
	}}, nil
}

// noRedirect hands a redirect of the provider back as it is: following it
// would send the key to an address nobody entered.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// PublicAddress says whether an address is one an owner's connection may
// reach: global unicast and in none of the internal ranges (netguard).
func PublicAddress(ip netip.Addr) bool { return netguard.PublicAddress(ip) }
