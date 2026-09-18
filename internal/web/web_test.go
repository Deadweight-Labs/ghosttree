package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func testWeb(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/web.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	token, err := st.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, st, token
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func login(t *testing.T, srv *httptest.Server, token string) *http.Client {
	t.Helper()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}, "next": {"/ui/requests"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status=%d body=%s", resp.StatusCode, body(t, resp))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie=%+v", cookies)
	}
	client.Jar = cookieJar{cookies: cookies}
	return client
}

func sameOriginPostForm(t *testing.T, client *http.Client, target string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type cookieJar struct{ cookies []*http.Cookie }

func (j cookieJar) SetCookies(*url.URL, []*http.Cookie) {}
func (j cookieJar) Cookies(*url.URL) []*http.Cookie     { return j.cookies }

func TestShellAuthAndNavigation(t *testing.T) {
	srv, _, token := testWeb(t)
	anon := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := anon.Get(srv.URL + "/ui/requests")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/login") {
		t.Fatalf("anonymous response=%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	client := login(t, srv, token)
	resp, _ = client.Get(srv.URL + "/ui/requests")
	html := body(t, resp)
	for _, want := range []string{"Requests", "Knowledge", "Review", "Sessions", "Agent Context", `data-clay-theme="ghosttree"`} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestBrowserSessionCarriesStablePrincipal(t *testing.T) {
	srv, _, token := testWeb(t)
	client := login(t, srv, token)
	resp, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); !strings.Contains(got, `data-principal="person:1"`) {
		t.Fatal("stable principal missing from rendered session")
	}
}

func TestLoginUsesFixedReturnTarget(t *testing.T) {
	srv, _, token := testWeb(t)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, next := range []string{
		`/ui/knowledge`,
		`/\evil.example`,
		`/%5cevil.example`,
		`/%2fevil.example`,
		`/%2F%2Fevil.example`,
		`//evil.example`,
		`https://evil.example`,
	} {
		resp := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}, "next": {next}})
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != "/ui/requests" {
			t.Errorf("next %q redirected to %q", next, got)
		}
	}
}

func TestLoginRejectsCrossOrigin(t *testing.T) {
	srv, _, token := testWeb(t)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"token": {token}}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/ui/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d", resp.StatusCode)
	}
}

func TestSameOriginTrustsForwardingOnlyFromLoopback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{name: "loopback proxy", remoteAddr: "127.0.0.1:43120", want: true},
		{name: "untrusted remote", remoteAddr: "192.0.2.10:43120", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://ghosttree.internal/ui/coord/send", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("Origin", "https://ghost.example")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-Host", "ghost.example")
			if got := sameOrigin(req); got != tc.want {
				t.Fatalf("sameOrigin=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameOriginRejectsMalformedForwardingFromLoopback(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://ghosttree.internal/ui/coord/send", nil)
	req.RemoteAddr = "127.0.0.1:43120"
	req.Header.Set("Origin", "http://ghosttree.internal")
	req.Header.Set("X-Forwarded-Proto", "")
	req.Header.Set("X-Forwarded-Host", "")
	if sameOrigin(req) {
		t.Fatal("present but empty forwarding headers fell back to the internal origin")
	}
}

func TestRequestsRenderCriteriaEvidenceAndEscapeHTML(t *testing.T) {
	srv, st, token := testWeb(t)
	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: `<script>alert(1)</script>`, Description: "Useful ledger", Scope: scope.Axes{Project: "p"}}, Criteria: []string{"observable result"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCriterionState(detail.Criteria[0].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "go test ./...", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	client := login(t, srv, token)
	resp, _ := client.Get(srv.URL + "/ui/requests/" + detail.Request.HumanID()[4:])
	html := body(t, resp)
	for _, want := range []string{"observable result", "go test ./...", "Useful ledger", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("request title was not escaped")
	}
}

func TestOperatorSectionsUseStoredData(t *testing.T) {
	srv, st, token := testWeb(t)
	runID, err := st.BeginMigration("p", map[string]string{"AGENTS.md": "digest-proof"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.InsertMigrated(store.MigratedEntry{Knowledge: store.Knowledge{Type: "decision", Title: "SQLite stays", Body: "Single writer", Scope: scope.Axes{Project: "p"}, Origin: "distilled", Confidence: "quarantined", SessionRef: "AGENTS.md"}, RunID: runID, Digest: "digest-proof", ItemKey: "sqlite", Quote: "use sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	// A pitfall, because the agent context view renders what is actually
	// delivered and only pushed types appear there.
	if _, err := st.InsertKnowledge(store.Knowledge{Type: "pitfall", Title: "SQLite runtime", Body: "Visible agent context", Scope: scope.Axes{Project: "p"}, Confidence: "trusted"}); err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "run-1", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendChunks(sessionID, []store.Chunk{{Seq: 1, Role: "user", Text: "ledger context", Raw: `{}`}}); err != nil {
		t.Fatal(err)
	}
	client := login(t, srv, token)
	for path, want := range map[string]string{
		"/ui/knowledge?q=SQLite": "SQLite stays",
		"/ui/review":             "SQLite stays",
		"/ui/sessions":           "run-1",
		"/ui/sessions/" + strconv.FormatInt(sessionID, 10): "ledger context",
		"/ui/context?project=p":                            "SQLite runtime",
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		got := body(t, resp)
		if resp.StatusCode != 200 || !strings.Contains(got, want) {
			t.Errorf("%s: status=%d missing %q in %s", path, resp.StatusCode, want, got)
		}
	}
	resp, _ := client.Get(srv.URL + "/ui/review")
	if got := body(t, resp); !strings.Contains(got, "digest-proof") || !strings.Contains(got, "use sqlite") {
		t.Errorf("review omitted migration proof: %s", got)
	}
}
