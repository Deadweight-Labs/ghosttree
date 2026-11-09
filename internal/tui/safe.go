package tui

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

// clean makes text from outside (server answers, config, git) one printable
// line: control and format characters, which could carry terminal escapes,
// become blanks, and the length is capped.
func clean(s string, max int) string { return links.Clean(s, max) }

// LoginCode keeps only what a device code consists of (A-Z, 0-9, -), so a
// code from the server can neither carry escapes onto the screen nor into the
// clipboard. An empty result means the server sent no usable code.
func LoginCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}

// WebURL is webURL for the backend, which checks again before it starts a
// program.
func WebURL(raw string) bool { return webURL(raw) }

// webURL reports whether raw is an address a browser may be asked to open:
// http or https, a host, no blanks or control characters, and nothing an
// opener program could read as an option.
func webURL(raw string) bool {
	if raw == "" || strings.HasPrefix(raw, "-") {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}

// sameHost reports whether two addresses name the same host and port.
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}
