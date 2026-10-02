package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

// loginInteractive meldet ein Konto über einen Login-Link an. Die Sitzung ist
// interaktiv und darf Verwaltungsformulare abschicken; login() mit eingefügtem
// Token darf das nicht.
func loginInteractive(t *testing.T, srv *httptest.Server, st *store.Store, account string) *http.Client {
	t.Helper()
	code, _, err := st.CreateAccountCode(store.CodeLogin, account)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login/code", url.Values{"code": {code}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login link status=%d body=%s", resp.StatusCode, body(t, resp))
	}
	client.Jar = cookieJar{cookies: resp.Cookies()}
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

func TestAppChromeKeepsEveryGlobalDestinationAndLogoutAvailable(t *testing.T) {
	srv, _, token := testWeb(t)
	client := login(t, srv, token)

	for _, path := range []string{
		"/ui/requests",
		"/ui/knowledge",
		"/ui/review",
		"/ui/sessions",
		"/ui/coord",
		"/ui/context",
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, resp.StatusCode)
		}
		html := body(t, resp)
		for _, want := range []string{
			`class="app-header"`,
			`class="app-brand" href="/ui/coord"`,
			`>Ghosttree</span>`,
			`href="/ui/requests"`,
			`href="/ui/knowledge"`,
			`href="/ui/review"`,
			`href="/ui/sessions"`,
			`href="/ui/coord"`,
			`href="/ui/context"`,
			`class="app-nav-more"`,
			`<summary>Mehr</summary>`,
			`method="post" action="/ui/logout"`,
			`name="csrf_token"`,
			`>alice</button>`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("GET %s chrome missing %q", path, want)
			}
		}
	}
}

func TestAppChromeMarksCoordinationCurrentOnlyOnCoordination(t *testing.T) {
	srv, _, token := testWeb(t)
	client := login(t, srv, token)

	resp, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	coordHTML := body(t, resp)
	if !strings.Contains(coordHTML, `class="app-nav-coordination" href="/ui/coord" aria-current="page"`) {
		t.Fatal("coordination navigation item is not marked current on the coordination page")
	}

	resp, err = client.Get(srv.URL + "/ui/requests")
	if err != nil {
		t.Fatal(err)
	}
	requestsHTML := body(t, resp)
	if strings.Contains(requestsHTML, `class="app-nav-coordination" href="/ui/coord" aria-current="page"`) {
		t.Fatal("coordination navigation item is marked current outside coordination")
	}
}

func TestAppChromeMarksTheCurrentSectionOnListAndDetailPages(t *testing.T) {
	srv, st, token := testWeb(t)
	client := login(t, srv, token)

	detail, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: "Current navigation", Scope: scope.Axes{Project: "p"}}, Criteria: []string{"active section"}})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.UpsertSession(store.Session{Harness: "codex", ExternalID: "nav-current", Scope: scope.Axes{Project: "p"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path, href, label string
	}{
		{"/ui/requests", "/ui/requests", "Requests"},
		{"/ui/requests/" + strconv.FormatInt(detail.Request.ID, 10), "/ui/requests", "Requests"},
		{"/ui/knowledge", "/ui/knowledge", "Knowledge"},
		{"/ui/review", "/ui/review", "Review"},
		{"/ui/sessions", "/ui/sessions", "Sessions"},
		{"/ui/sessions/" + strconv.FormatInt(sessionID, 10), "/ui/sessions", "Sessions"},
		{"/ui/context", "/ui/context", "Agent Context"},
	} {
		resp, err := client.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		html := body(t, resp)
		want := `href="` + tc.href + `" aria-current="page">` + tc.label + `</a>`
		if !strings.Contains(html, want) {
			t.Errorf("GET %s does not mark %s current", tc.path, tc.label)
		}
	}
}

func TestAppChromeGivesLogoutAnActionName(t *testing.T) {
	srv, _, token := testWeb(t)
	client := login(t, srv, token)
	resp, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	if html := body(t, resp); !strings.Contains(html, `aria-label="Sign out as alice"`) {
		t.Fatal("logout button must expose its action and signed-in identity")
	}
}

func TestAppChromeUsesNativeMobileDisclosureWithoutWrappingTheBar(t *testing.T) {
	srv, _, _ := testWeb(t)
	resp, err := http.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := body(t, resp)
	for _, want := range []string{
		".app-nav-more",
		"@media (max-width: 700px)",
		"white-space: nowrap",
		"grid-template-columns: auto minmax(0, 1fr) auto",
		".app-nav a.app-nav-mobile-current",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("mobile app chrome CSS missing %q", want)
		}
	}
}

func TestAppChromeUsesGermanMobileOverflowLabel(t *testing.T) {
	templateBytes, err := files.ReadFile("templates/pages.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(templateBytes)
	if !strings.Contains(template, `<summary>Mehr</summary>`) || !strings.Contains(template, `aria-label="Weitere Ziele"`) {
		t.Fatal("mobile overflow navigation must use consistent German labels")
	}
	if strings.Contains(template, `<summary>More</summary>`) {
		t.Fatal("mobile overflow navigation retains the English placeholder label")
	}
}

func TestBrowserSessionCarriesStablePrincipal(t *testing.T) {
	srv, _, token := testWeb(t)
	client := login(t, srv, token)
	resp, err := client.Get(srv.URL + "/ui/coord")
	if err != nil {
		t.Fatal(err)
	}
	got := body(t, resp)
	if !strings.Contains(got, `alice`) || strings.Contains(got, `data-principal="person:1"`) {
		t.Fatal("browser session must render the human label without exposing its stable identifier")
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
			if got := (&app{}).sameOrigin(req); got != tc.want {
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
	if (&app{}).sameOrigin(req) {
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

func TestWebSessionEndsWhenTokenOrAccountStopsBeingValid(t *testing.T) {
	srv, st, _ := testWeb(t)
	st.AddAccount("bob", "", false)
	status := func(c *http.Client) (int, string) {
		resp, err := c.Get(srv.URL + "/ui/requests")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	issue := func(spec store.TokenSpec) (*http.Client, int64) {
		token, info, err := st.CreateToken("bob", spec)
		if err != nil {
			t.Fatal(err)
		}
		return login(t, srv, token), info.ID
	}

	valid, _ := issue(store.TokenSpec{})
	for i := 0; i < 2; i++ {
		if code, _ := status(valid); code != http.StatusOK {
			t.Fatalf("valid session request %d: %d", i, code)
		}
	}

	revoked, id := issue(store.TokenSpec{})
	if code, _ := status(revoked); code != http.StatusOK {
		t.Fatalf("before revoke: %d", code)
	}
	if err := st.RevokeToken(id); err != nil {
		t.Fatal(err)
	}
	if code, loc := status(revoked); code != http.StatusSeeOther || loc != "/ui/login" {
		t.Fatalf("revoked session: %d %q", code, loc)
	}

	expiring, id := issue(store.TokenSpec{})
	if _, err := st.DB().Exec(`UPDATE api_tokens SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if code, loc := status(expiring); code != http.StatusSeeOther || loc != "/ui/login" {
		t.Fatalf("expired session: %d %q", code, loc)
	}

	disabled, _ := issue(store.TokenSpec{})
	if _, err := st.DB().Exec(`UPDATE persons SET state='disabled' WHERE name='bob'`); err != nil {
		t.Fatal(err)
	}
	if code, loc := status(disabled); code != http.StatusSeeOther || loc != "/ui/login" {
		t.Fatalf("disabled account session: %d %q", code, loc)
	}
	if code, loc := status(valid); code != http.StatusSeeOther || loc != "/ui/login" {
		t.Fatalf("sibling session of disabled account: %d %q", code, loc)
	}
}

// Chromium sends "Origin: null" on a form POST when the form page carries
// Referrer-Policy: no-referrer. The code pages must therefore use a policy
// under which the browser sends the real origin, while "null" and foreign
// origins stay rejected.
func TestCodePageReferrerPolicyKeepsRealOriginAndCodeOutOfReferer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(New(st))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ui/login/code?code=abc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// strict-origin: Origin stays a real origin, Referer never carries the path.
	if got := resp.Header.Get("Referrer-Policy"); got != "strict-origin" {
		t.Fatalf("Referrer-Policy=%q, want strict-origin", got)
	}
}

func TestCodeSubmitRejectsNullAndForeignOrigin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "orig.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	defer srv.Close()
	for _, origin := range []string{"null", "https://evil.example", ""} {
		code, _, err := st.CreateAccountCode(store.CodeLogin, "alice")
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ui/login/code", strings.NewReader(url.Values{"code": {code}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: status=%d, want 403", origin, resp.StatusCode)
		}
	}
}
