package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func loginPageOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/login = %d", resp.StatusCode)
	}
	return body(t, resp)
}

func TestLoginPageWithoutOIDCHasCodeFieldAndNoProviderButton(t *testing.T) {
	srv, _, _ := testWeb(t)
	page := loginPageOf(t, srv)
	for _, want := range []string{
		`class="login"`, `<h1>Sign in</h1>`, `name="code"`, `action="/ui/login/code"`,
		`Code or login link`, `placeholder="e.g. 4f9k-2m7q-x8dp"`,
		`Paste a person token`, `name="token"`, `action="/ui/login"`,
		`href="/static/tokens.css"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("login page lacks %q", want)
		}
	}
	for _, bad := range []string{"/ui/login/oidc", "Continue with", " or<", `class="login-divider"`, "ZITADEL"} {
		if strings.Contains(page, bad) {
			t.Errorf("login page without OIDC shows %q", bad)
		}
	}
	if strings.Contains(page, `class="shell"`) {
		t.Error("login page must not sit inside the app shell")
	}
}

func TestLoginPageWithOIDCOffersProviderAndCodeFieldTogether(t *testing.T) {
	env := newOIDCEnv(t, false)
	resp, err := http.Get(env.web.URL + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	for _, want := range []string{
		`formaction="/ui/login/oidc"`, `formnovalidate`, `Continue with your identity provider`,
		`name="code"`, `class="login-divider"`, `Code or login link`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("OIDC login page lacks %q", want)
		}
	}
	if strings.Contains(page, "ZITADEL") {
		t.Error("provider name appears although none is configured")
	}
}

func TestLoginPageNamesTheConfiguredProvider(t *testing.T) {
	env := newOIDCEnv(t, false, func(a *app) { a.oidcName = "ZITADEL" })
	resp, err := http.Get(env.web.URL + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if !strings.Contains(page, "Continue with ZITADEL") || strings.Contains(page, "your identity provider") {
		t.Errorf("provider name not used: %s", page)
	}
}

func TestLoginPageDefaultButtonIsTheCodeSignIn(t *testing.T) {
	env := newOIDCEnv(t, false)
	resp, _ := http.Get(env.web.URL + "/ui/login")
	page := body(t, resp)
	// Enter im Feld löst den ersten Submit-Knopf aus; das ist "Sign in" an
	// /ui/login/code, nicht der Anbieterknopf.
	first := strings.Index(page, `<button`)
	if first < 0 || strings.Contains(page[first:first+strings.Index(page[first:], ">")], "formaction") {
		t.Errorf("the first submit button of the form must be the code sign-in, got %.200s", page[first:])
	}
}

// Enter im Codefeld postet an /ui/login/code. Auf einer OIDC-Instanz muss ein
// Claim-Code dort den Anbieter-Ablauf starten, nicht "invalid" melden.
func TestOIDCClaimCodeOnEnterStartsTheProviderFlow(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	cb := env.startFlowAt(t, b, "/ui/login/code", env.claimCode(t, "alice"))
	r := env.callback(t, b, cb)
	r.Body.Close()
	if !env.signedIn(t, b) {
		t.Fatal("the claim code entered with Enter did not sign alice in through the provider")
	}
}

func TestOIDCBootstrapAndInvitationCodesOnEnterStartTheProviderFlow(t *testing.T) {
	for _, kind := range []string{"bootstrap", "invitation"} {
		env := newOIDCEnv(t, kind == "invitation")
		var code string
		if kind == "invitation" {
			org, err := env.store.CreateOrg("person:1", "Alpha", "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if code, _, err = env.store.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0); err != nil {
				t.Fatal(err)
			}
		} else {
			var err error
			if code, _, err = env.store.EnsureBootstrapCode(); err != nil {
				t.Fatal(err)
			}
		}
		b := newBrowser(t)
		resp := sameOriginPostForm(t, b, env.web.URL+"/ui/login/code", url.Values{"code": {code}})
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, env.idp.srv.URL+"/authorize?") {
			t.Errorf("%s code with Enter: status=%d location=%q", kind, resp.StatusCode, loc)
		}
	}
}

func TestOIDCUnknownCodeOnEnterShowsTheErrorInTheForm(t *testing.T) {
	env := newOIDCEnv(t, true)
	resp := sameOriginPostForm(t, newBrowser(t), env.web.URL+"/ui/login/code", url.Values{"code": {"nope"}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, `role="alert"`) || strings.Contains(page, "Use the identity provider") {
		t.Fatalf("status=%d %s", resp.StatusCode, page)
	}
}

func TestPastedLinkWorksThroughTheProviderButtonToo(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	claim := env.claimCode(t, "alice")
	link := env.web.URL + "/ui/login/code?code=" + claim + "&pad=" + strings.Repeat("x", 200)
	cb := env.startFlowAt(t, b, "/ui/login/oidc", link)
	r := env.callback(t, b, cb)
	r.Body.Close()
	if !env.signedIn(t, b) {
		t.Fatal("a pasted link with a claim code did not sign in through the provider button")
	}
}

func TestPastedJoinLinkLeadsToTheJoinPageFromBothPaths(t *testing.T) {
	env := newOIDCEnv(t, true)
	join := strings.Repeat("ab", 32)
	for _, path := range []string{"/ui/login/code", "/ui/login/oidc"} {
		resp := sameOriginPostForm(t, newBrowser(t), env.web.URL+path, url.Values{"code": {"  " + env.web.URL + "/join/" + join + "  "}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/"+join {
			t.Errorf("%s: status=%d location=%q", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// Kein gültiger Einladungscode: kein Weiterleiten auf beliebige Pfade.
	resp := sameOriginPostForm(t, newBrowser(t), env.web.URL+"/ui/login/code", url.Values{"code": {env.web.URL + "/join/../evil"}})
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther && strings.Contains(resp.Header.Get("Location"), "evil") {
		t.Error("redirected to an arbitrary path")
	}
}

func TestLoginPageForAFreshInstanceAsksForBootstrapCodeAndName(t *testing.T) {
	st := mustStore(t)
	file := filepath.Join(t.TempDir(), "bootstrap-code")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithBootstrapFile(file)))
	defer srv.Close()
	page := loginPageOf(t, srv)
	for _, want := range []string{"Bootstrap code", "Your name", `name="name"`, `name="code"`, `action="/ui/login/code"`, `placeholder="e.g. robin"`} {
		if !strings.Contains(page, want) {
			t.Errorf("bootstrap login lacks %q", want)
		}
	}
	if strings.Contains(page, "Paste a person token") {
		t.Error("bootstrap login offers token paste")
	}
}

func TestLoginWithAWrongCodeShowsTheErrorInsideTheForm(t *testing.T) {
	srv, _, _ := testWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login/code", url.Values{"code": {"not-a-code"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	page := body(t, resp)
	for _, want := range []string{
		`role="alert"`, "That code isn&#39;t valid. Check it and try again, or ask for a new link.",
		`name="code"`, `<h1>Sign in</h1>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("error state lacks %q: %s", want, page)
		}
	}
	if strings.Contains(page, "not-a-code") {
		t.Error("the rejected code is echoed back")
	}
	if resp.Header.Get("Referrer-Policy") != "strict-origin" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", resp.Header)
	}
}

func TestLoginWithAWrongTokenShowsTheSameErrorState(t *testing.T) {
	srv, _, _ := testWeb(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {"nope"}})
	page := body(t, resp)
	if !strings.Contains(page, `role="alert"`) || !strings.Contains(page, `<h1>Sign in</h1>`) {
		t.Errorf("token error state: %s", page)
	}
}

func TestCodePagesKeepStrictOriginAndTheCodeField(t *testing.T) {
	srv, _, _ := testWeb(t)
	resp, err := http.Get(srv.URL + "/ui/login/code?code=abc")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Referrer-Policy") != "strict-origin" {
		t.Errorf("Referrer-Policy=%q", resp.Header.Get("Referrer-Policy"))
	}
	page := body(t, resp)
	if !strings.Contains(page, `name="code"`) || !strings.Contains(page, `action="/ui/login/code"`) {
		t.Errorf("code page lacks its form: %s", page)
	}
}

func TestLoginAcceptsAPastedLoginLinkInTheCodeField(t *testing.T) {
	srv, st, _ := testWeb(t)
	code, _, err := st.CreateAccountCode("login", "alice")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/login/code", url.Values{"code": {srv.URL + "/ui/login/code?code=" + code}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("pasted link status=%d: %s", resp.StatusCode, body(t, resp))
	}
}

func TestLoginPageSendsStrictOriginAndNoStore(t *testing.T) {
	srv, _, _ := testWeb(t)
	resp, err := http.Get(srv.URL + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Referrer-Policy") != "strict-origin" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", resp.Header)
	}
}
