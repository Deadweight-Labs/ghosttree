package proxytrust

import "testing"

func TestTrusts(t *testing.T) {
	s, err := Parse("192.0.2.0/24, 198.51.100.7 2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"127.0.0.1:1": true, "[::1]:1": true, "192.0.2.55:80": true, "192.0.3.1:80": false,
		"198.51.100.7:1": true, "198.51.100.8:1": false, "[2001:db8::5]:1": true,
		"[::ffff:192.0.2.9]:1": true, "203.0.113.1:1": false, "garbage": false,
	} {
		if got := s.Trusts(addr); got != want {
			t.Errorf("Trusts(%q)=%v want %v", addr, got, want)
		}
	}
	empty, _ := Parse("")
	if empty.Trusts("192.0.2.55:80") || empty.Configured() || !empty.Trusts("127.0.0.1:9") {
		t.Fatal("empty set must trust loopback only")
	}
	if _, err := Parse("not-an-ip"); err == nil {
		t.Fatal("invalid entry accepted")
	}
}

func TestClientAddress(t *testing.T) {
	s, _ := Parse("192.0.2.0/24, 2001:db8::/32")
	for name, tc := range map[string]struct {
		remote string
		xff    []string
		want   string
	}{
		"untrusted peer ignores xff":       {"203.0.113.5:1", []string{"198.51.100.9"}, "203.0.113.5"},
		"single proxy-added entry":         {"192.0.2.10:1", []string{"198.51.100.9"}, "198.51.100.9"},
		"spoofed first entry loses":        {"192.0.2.10:1", []string{"1.1.1.1, 198.51.100.9"}, "198.51.100.9"},
		"multiple lines, client line 1":    {"192.0.2.10:1", []string{"1.1.1.1", "198.51.100.9"}, "198.51.100.9"},
		"chain of two trusted proxies":     {"192.0.2.10:1", []string{"198.51.100.9, 192.0.2.77"}, "198.51.100.9"},
		"pass-through without proxy hop":   {"192.0.2.10:1", []string{"6.6.6.6"}, "6.6.6.6"},
		"garbage entry stops the walk":     {"192.0.2.10:1", []string{"1.1.1.1, nonsense"}, "192.0.2.10"},
		"all trusted -> leftmost":          {"192.0.2.10:1", []string{"192.0.2.5, 192.0.2.6"}, "192.0.2.5"},
		"no header -> peer":                {"192.0.2.10:1", nil, "192.0.2.10"},
		"ipv6 client":                      {"192.0.2.10:1", []string{"2001:db9::1"}, "2001:db9::1"},
		"ipv6 trusted hop":                 {"[2001:db8::1]:1", []string{"203.0.113.8, 2001:db8::2"}, "203.0.113.8"},
		"mapped client normalised":         {"192.0.2.10:1", []string{"::ffff:198.51.100.9"}, "198.51.100.9"},
		"mapped trusted peer":              {"[::ffff:192.0.2.10]:1", []string{"198.51.100.9"}, "198.51.100.9"},
		"zoned entry rejected":             {"192.0.2.10:1", []string{"fe80::1%eth0"}, "192.0.2.10"},
		"loopback peer trusted by default": {"127.0.0.1:1", []string{"198.51.100.9"}, "198.51.100.9"},
	} {
		if got := s.Client(tc.remote, tc.xff); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestNoneDisablesLoopbackAndParseRejections(t *testing.T) {
	s, err := Parse("none, 192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if s.Trusts("127.0.0.1:1") || s.Trusts("[::1]:1") || !s.Trusts("192.0.2.10:1") {
		t.Fatal("none must switch loopback off and keep listed proxies")
	}
	if got := s.Client("127.0.0.1:1", []string{"198.51.100.9"}); got != "127.0.0.1" {
		t.Fatalf("untrusted loopback must ignore xff, got %s", got)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "::ffff:192.0.2.0/120", "fe80::1%eth0", "192.0.2.1/33"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
