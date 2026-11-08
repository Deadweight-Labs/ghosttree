package web

import (
	"net/http"
	"net/url"
	"testing"
)

// REQ-603 Kriterium 7, OIDC-Weg: wer ohne Anmeldung eine Seite öffnet, kommt
// nach dem Weg über den Identitätsanbieter auf genau diese Seite zurück. Die
// Adresse bleibt dabei im Cookie, nie in der URL zum Anbieter, und gilt einmal.
func TestOIDCLoginReturnsToRequestedPage(t *testing.T) {
	env := newOIDCEnv(t, true)
	// Die Identität ist an alice gebunden; danach braucht der Login keinen Code.
	first := newBrowser(t)
	resp := env.callback(t, first, env.startFlow(t, first, env.claimCode(t, "alice")))
	resp.Body.Close()

	b := newBrowser(t)
	anon, err := b.Get(env.web.URL + "/ui/requests?state=open#x")
	if err != nil {
		t.Fatal(err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther || anon.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous request = %d %q, want redirect to /ui/login", anon.StatusCode, anon.Header.Get("Location"))
	}
	cb := env.startFlow(t, b, "")
	// Die Rücksprungadresse steht nicht in der Adresse zum Anbieter.
	if cb.Query().Get("next") != "" || cb.RawQuery != "" && containsAny(cb.RawQuery, "requests", "next=") {
		t.Fatalf("the return page travels in the callback address: %s", cb)
	}
	done := env.callback(t, b, cb)
	done.Body.Close()
	if done.StatusCode != http.StatusSeeOther || done.Header.Get("Location") != "/ui/requests?state=open" {
		t.Fatalf("after the OIDC login: %d Location=%q, want the requested page", done.StatusCode, done.Header.Get("Location"))
	}
	if !env.signedIn(t, b) {
		t.Fatal("the session of the OIDC login does not open the UI")
	}
	// Einmalig: der nächste Login derselben Maschine führt zum Standardziel.
	b2 := newBrowser(t)
	again := env.callback(t, b2, env.startFlow(t, b2, ""))
	again.Body.Close()
	if got := again.Header.Get("Location"); got != "/ui/overview" {
		t.Fatalf("login without a remembered page = %q, want /ui/overview", got)
	}
}

func TestOIDCLoginTakesTheNextParameterOfTheLoginPageButOnlyInternalPaths(t *testing.T) {
	env := newOIDCEnv(t, true)
	first := newBrowser(t)
	resp := env.callback(t, first, env.startFlow(t, first, env.claimCode(t, "alice")))
	resp.Body.Close()

	for next, want := range map[string]string{
		"/ui/inbox":                   "/ui/inbox",
		"https://evil.example/ui/x":   "/ui/overview",
		"//evil.example/ui/x":         "/ui/overview",
		"/ui/login/oidc/callback?x=1": "/ui/overview",
	} {
		b := newBrowser(t)
		page, err := b.Get(env.web.URL + "/ui/login?next=" + url.QueryEscape(next))
		if err != nil {
			t.Fatal(err)
		}
		page.Body.Close()
		done := env.callback(t, b, env.startFlow(t, b, ""))
		done.Body.Close()
		if got := done.Header.Get("Location"); got != want {
			t.Errorf("next=%q: after the OIDC login Location=%q, want %q", next, got, want)
		}
	}
}

func TestOIDCLoginIgnoresAForgedReturnCookie(t *testing.T) {
	env := newOIDCEnv(t, true)
	first := newBrowser(t)
	resp := env.callback(t, first, env.startFlow(t, first, env.claimCode(t, "alice")))
	resp.Body.Close()

	for _, evil := range []string{"https://evil.example/ui/x", "//evil.example/ui/x", "/api/tokens", "/ui/login"} {
		b := newBrowser(t)
		base, _ := url.Parse(env.web.URL)
		b.Jar.SetCookies(base, []*http.Cookie{{Name: nextCookie, Value: url.QueryEscape(evil), Path: "/ui/login"}})
		done := env.callback(t, b, env.startFlow(t, b, ""))
		done.Body.Close()
		if got := done.Header.Get("Location"); got != "/ui/overview" {
			t.Errorf("cookie %q led to %q, want the default page", evil, got)
		}
	}
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				return true
			}
		}
	}
	return false
}
