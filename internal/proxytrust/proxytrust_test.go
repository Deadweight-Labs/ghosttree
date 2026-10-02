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
