package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// REQ-603: der Rücksprung nach der Anmeldung nimmt nur interne Pfade.
func TestSafeNextRejectsOpenRedirects(t *testing.T) {
	good := []string{"/ui/inbox", "/ui/owner/repo/issues/603", "/ui/owner/repo/issues?q=is%3Aopen", "/ui/presets/repo/x?pr=1#a"}
	for _, raw := range good {
		if got := safeNext(raw); got != raw {
			t.Errorf("safeNext(%q) = %q, want unchanged", raw, got)
		}
	}
	bad := []string{
		"", "ui/inbox", "//evil.example/ui/x", "https://evil.example/ui/x", "/\\evil.example", "/ui/\\x",
		"javascript:alert(1)", "/ui/x\r\nSet-Cookie: a=b", "/ui/x‮y", "/ui/x​y", "/ui/ x", "/api/tokens",
		"/ui/login", "/ui/login/code?code=1", "/ui/logout", "/ui/../etc", "/ui/a/../../x", "/join/abc", "/ui/x\x00",
	}
	for _, raw := range bad {
		if got := safeNext(raw); got != "" {
			t.Errorf("safeNext(%q) = %q, want empty", raw, got)
		}
	}
}

func nextEnv(t *testing.T) (*httptest.Server, *http.Client, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, _ := st.AddPerson("robin")
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, client, token
}

func TestLoginReturnsToRequestedPage(t *testing.T) {
	srv, client, token := nextEnv(t)
	resp, err := client.Get(srv.URL + "/ui/overview?tab=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous request = %d %q, want redirect to /ui/login", resp.StatusCode, resp.Header.Get("Location"))
	}
	login := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
	login.Body.Close()
	if got := login.Header.Get("Location"); got != "/ui/overview?tab=x" {
		t.Fatalf("after login Location = %q, want the requested page", got)
	}
	// Einmalig: ein zweiter Login führt wieder zum Standardziel.
	client2 := &http.Client{CheckRedirect: client.CheckRedirect}
	second := sameOriginPostForm(t, client2, srv.URL+"/ui/login", url.Values{"token": {token}})
	second.Body.Close()
	if got := second.Header.Get("Location"); got != "/ui/overview" {
		t.Fatalf("login without remembered page = %q", got)
	}
}

func TestLoginNextParameterNeverLeavesTheSite(t *testing.T) {
	for _, evil := range []string{"https://evil.example/ui/x", "//evil.example/ui/x", "/\\evil.example", "/api/x"} {
		srv, client, token := nextEnv(t)
		resp, err := client.Get(srv.URL + "/ui/login?next=" + url.QueryEscape(evil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		login := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
		login.Body.Close()
		if got := login.Header.Get("Location"); got != "/ui/overview" {
			t.Errorf("next=%q led to %q, want the default page", evil, got)
		}
	}
	srv, client, token := nextEnv(t)
	resp, _ := client.Get(srv.URL + "/ui/login?next=" + url.QueryEscape("/ui/inbox"))
	resp.Body.Close()
	login := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
	login.Body.Close()
	if got := login.Header.Get("Location"); got != "/ui/inbox" {
		t.Errorf("valid next led to %q", got)
	}
}

func TestLoginDoesNotRememberPostRequests(t *testing.T) {
	srv, client, token := nextEnv(t)
	post := sameOriginPostForm(t, client, srv.URL+"/ui/requests/1/close", url.Values{})
	post.Body.Close()
	login := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
	login.Body.Close()
	if got := login.Header.Get("Location"); got != "/ui/overview" {
		t.Fatalf("a POST must not become a return target, got %q", got)
	}
}
