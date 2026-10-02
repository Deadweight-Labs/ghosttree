package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type peer struct {
	tls    bool
	remote string
	header map[string]string
}

func (p peer) post(h http.Handler, target, origin string, form url.Values, cookies ...*http.Cookie) *http.Response {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	req.RemoteAddr = p.remote
	if p.tls {
		req.TLS = &tls.ConnectionState{}
	}
	for k, v := range p.header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func sessionOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

// cycle logs in and out and returns both session cookies.
func cycle(t *testing.T, p peer, origin string, opts ...Option) (login, logout *http.Cookie) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, _ := st.AddPerson("alice")
	h := newApp(st, opts...)
	resp := p.post(h, "http://gt.test/ui/login", origin, url.Values{"token": {token}})
	if login = sessionOf(resp); login == nil {
		t.Fatalf("login set no session cookie, status %d", resp.StatusCode)
	}
	csrf := h.(*appHandler).app.sessions.values[login.Value].csrf
	resp = p.post(h, "http://gt.test/ui/logout", origin, url.Values{"csrf_token": {csrf}}, login)
	if logout = sessionOf(resp); logout == nil {
		t.Fatalf("logout set no clearing cookie, status %d", resp.StatusCode)
	}
	return login, logout
}

func checkCookies(t *testing.T, login, logout *http.Cookie, secure bool) {
	t.Helper()
	for name, c := range map[string]*http.Cookie{"login": login, "logout": logout} {
		if c.Secure != secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Errorf("%s cookie attributes wrong (want secure=%v): %+v", name, secure, c)
		}
	}
	if login.MaxAge <= 0 || logout.MaxAge >= 0 {
		t.Errorf("max-age login=%d logout=%d", login.MaxAge, logout.MaxAge)
	}
}

func TestCookieAttributesByDeployment(t *testing.T) {
	proxies := func(list string) Option {
		s, err := proxytrust.Parse(list)
		if err != nil {
			t.Fatal(err)
		}
		return WithTrustedProxies(s)
	}
	fwd := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "gt.test"}
	for _, tc := range []struct {
		name   string
		peer   peer
		origin string
		opts   []Option
		secure bool
	}{
		{"plain http stays usable", peer{remote: "192.0.2.10:5000"}, "http://gt.test", nil, false},
		{"direct tls", peer{tls: true, remote: "192.0.2.10:5000"}, "https://gt.test", nil, true},
		{"trusted proxy cidr", peer{remote: "198.51.100.4:5000", header: fwd}, "https://gt.test", []Option{proxies("198.51.100.0/24")}, true},
		{"loopback proxy", peer{remote: "127.0.0.1:5000", header: fwd}, "https://gt.test", nil, true},
		{"trusted proxy says http", peer{remote: "198.51.100.4:5000", header: map[string]string{"X-Forwarded-Proto": "http", "X-Forwarded-Host": "gt.test"}}, "http://gt.test", []Option{proxies("198.51.100.0/24")}, false},
		{"public url https, no headers", peer{remote: "203.0.113.9:5000"}, "https://gt.example.test", []Option{WithPublicURL("https://gt.example.test")}, true},
		{"public url http", peer{remote: "203.0.113.9:5000"}, "http://gt.test", []Option{WithPublicURL("http://gt.test")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login, logout := cycle(t, tc.peer, tc.origin, tc.opts...)
			checkCookies(t, login, logout, tc.secure)
		})
	}
}

func TestForwardedProtoFromUntrustedPeerDoesNotSwitchSecure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	trusted, _ := proxytrust.Parse("198.51.100.0/24")
	a := &app{proxies: trusted}
	for remote, want := range map[string]bool{
		"203.0.113.9:1":   false, // arbitrary client spoofing the header
		"198.51.101.4:1":  false, // next to the trusted network, not in it
		"198.51.100.4:1":  true,
		"127.0.0.1:1":     true,
		"not-an-addr":     false,
		"[2001:db8::1]:1": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "http://gt.test/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Proto", "https")
		if got := a.secureCookies(r); got != want {
			t.Errorf("remote %s: secure=%v want %v", remote, got, want)
		}
	}
	// Ambiguous or repeated headers never count, even from a trusted peer.
	for _, vals := range [][]string{{"https, http"}, {"https", "https"}, {"HTTPS "}, {"wss"}} {
		r := httptest.NewRequest(http.MethodGet, "http://gt.test/", nil)
		r.RemoteAddr = "198.51.100.4:1"
		r.Header["X-Forwarded-Proto"] = vals
		if a.secureCookies(r) {
			t.Errorf("header %q switched Secure on", vals)
		}
	}
	// An untrusted client that sends forwarding headers is refused outright by
	// the same-origin check, so no cookie is issued at all.
	token, _ := st.AddPerson("alice")
	h := newApp(st)
	resp := peer{remote: "203.0.113.9:1", header: map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "gt.test"}}.
		post(h, "http://gt.test/ui/login", "https://gt.test", url.Values{"token": {token}})
	if resp.StatusCode != http.StatusForbidden || sessionOf(resp) != nil {
		t.Fatalf("spoofed login status=%d cookie=%v", resp.StatusCode, sessionOf(resp))
	}
}

func TestSecureCookiesOverRealTLSServer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, _ := st.AddPerson("alice")
	h := newApp(st)
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
	resp.Body.Close()
	login := sessionOf(resp)
	if login == nil {
		t.Fatalf("no session cookie, status %d", resp.StatusCode)
	}
	csrf := h.(*appHandler).app.sessions.values[login.Value].csrf
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ui/logout", strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(login)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	logout := sessionOf(resp)
	if logout == nil {
		t.Fatalf("no clearing cookie, status %d", resp.StatusCode)
	}
	checkCookies(t, login, logout, true)
}

func TestPublicURLOriginIsAcceptedWithoutForwardingHeaders(t *testing.T) {
	a := &app{}
	WithPublicURL("https://gt.example.test")(a)
	r := httptest.NewRequest(http.MethodPost, "http://10.0.0.5:8474/ui/login", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Header.Set("Origin", "https://gt.example.test")
	if !a.sameOrigin(r) {
		t.Fatal("configured public origin rejected")
	}
	r.Header.Set("Origin", "https://evil.example.test")
	if a.sameOrigin(r) {
		t.Fatal("foreign origin accepted")
	}
}

func TestPublicURLDefaultPortIsDropped(t *testing.T) {
	a := &app{}
	WithPublicURL("https://gt.example.test:443")(a)
	r := httptest.NewRequest(http.MethodPost, "http://x/ui/login", nil)
	r.Header.Set("Origin", "https://gt.example.test")
	if !a.sameOrigin(r) {
		t.Fatal("browser origin without default port rejected")
	}
}
