package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func headersOf(t *testing.T, h http.Handler, path string, secure bool) http.Header {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://gt.test"+path, nil)
	req.RemoteAddr = "203.0.113.5:4000"
	if secure {
		req.TLS = &tls.ConnectionState{}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Header
}

// Every answer of the web interface carries the same protective headers; HSTS only
// where the connection is https.
func TestAnswersCarryProtectiveHeaders(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := newApp(st)
	for _, path := range []string{"/ui/login", "/join/" + strings.Repeat("ab", 32), "/static/join.js", "/install.sh", "/ui/mermaid-frame", "/favicon.ico", "/nothing-here"} {
		got := headersOf(t, h, path, false)
		for name, want := range map[string]string{
			"X-Content-Type-Options":     "nosniff",
			"Cross-Origin-Opener-Policy": "same-origin-allow-popups",
			"Permissions-Policy":         "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()",
			"Strict-Transport-Security":  "",
		} {
			if got.Get(name) != want {
				t.Errorf("%s %s = %q, want %q", path, name, got.Get(name), want)
			}
		}
		if rp := got.Get("Referrer-Policy"); rp == "" || rp == "unsafe-url" || rp == "no-referrer-when-downgrade" {
			t.Errorf("%s Referrer-Policy = %q", path, rp)
		}
		if secure := headersOf(t, h, path, true).Get("Strict-Transport-Security"); secure != "max-age=31536000" {
			t.Errorf("%s over https: Strict-Transport-Security = %q", path, secure)
		}
	}
	csp := headersOf(t, h, "/ui/login", false).Get("Content-Security-Policy")
	for _, want := range []string{"form-action 'self'", "base-uri 'none'", "frame-ancestors 'none'", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP of the sign-in page lacks %q: %s", want, csp)
		}
	}
	// A page of the join flow has its own policy and keeps it strict too.
	join := headersOf(t, h, "/join/"+strings.Repeat("ab", 32), false).Get("Content-Security-Policy")
	if !strings.Contains(join, "form-action 'self'") {
		t.Errorf("CSP of the join page lacks form-action: %s", join)
	}
}

// The identity provider is the one other place a form may lead to.
func TestFormActionNamesTheIdentityProvider(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idp := newFakeIdP(t)
	h := newApp(st, WithOIDC(OIDCConfig{Issuer: idp.srv.URL, ClientID: testClientID, ClientSecret: idp.secret,
		RedirectURL: "https://gt.example.test/ui/login/oidc/callback"}))
	csp := headersOf(t, h, "/ui/login", false).Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self' "+idp.srv.URL) {
		t.Errorf("form-action does not name the identity provider: %s", csp)
	}
}
