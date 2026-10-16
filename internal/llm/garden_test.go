package llm

import (
	"errors"
	"net"
	"testing"
)

func stubResolver(t *testing.T, ips []net.IP, err error) {
	t.Helper()
	old := resolveHost
	resolveHost = func(string) ([]net.IP, error) { return ips, err }
	t.Cleanup(func() { resolveHost = old })
}

func TestCheckBaseURL(t *testing.T) {
	lo := net.ParseIP("127.0.0.1")
	other := net.ParseIP("203.0.113.7")
	cases := []struct {
		name string
		url  string
		ips  []net.IP
		err  error
		ok   bool
	}{
		{"https anywhere", "https://example.com", nil, errors.New("unused"), true},
		{"http localhost", "http://localhost:1", nil, errors.New("unused"), true},
		{"http loopback literal", "http://127.0.0.1:1", nil, errors.New("unused"), true},
		{"http ipv6 loopback", "http://[::1]:1", nil, errors.New("unused"), true},
		{"http host resolving only to loopback", "http://allgemein:18317", []net.IP{lo, net.ParseIP("::1")}, nil, true},
		{"http host with mixed resolution", "http://mixed:1", []net.IP{lo, other}, nil, false},
		{"http foreign host", "http://example.com", []net.IP{other}, nil, false},
		{"http host with failed lookup", "http://nowhere:1", nil, errors.New("no such host"), false},
		{"http host with empty answer", "http://empty:1", nil, nil, false},
		{"http foreign literal", "http://203.0.113.7", nil, errors.New("unused"), false},
		{"ftp loopback", "ftp://localhost", nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubResolver(t, c.ips, c.err)
			if got := checkBaseURL(c.url) == nil; got != c.ok {
				t.Fatalf("checkBaseURL(%q) allowed=%v, want %v", c.url, got, c.ok)
			}
		})
	}
}
