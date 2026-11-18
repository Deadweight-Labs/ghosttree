package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func TestAcceptLanguageTakesTheBestOfTheTwoLanguages(t *testing.T) {
	for header, want := range map[string]lang{
		"de":                        langDE,
		"de-DE,de;q=0.9,en;q=0.8":   langDE,
		"fr-CH, fr;q=0.9, de;q=0.5": langDE,
		"en-GB,en;q=0.9,de;q=0.4":   langEN,
		"en;q=0.3, de;q=0.8":        langDE,
		"de;q=0, en":                langEN,
		" EN ":                      langEN,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", header)
		if got, ok := acceptLanguage(r); !ok || got != want {
			t.Errorf("Accept-Language %q = %q, %v; want %q", header, got, ok, want)
		}
	}
	for _, header := range []string{"", "fr", "*", "ja;q=1, zh;q=0.5", "de;q=0"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", header)
		if got, ok := acceptLanguage(r); ok {
			t.Errorf("Accept-Language %q should name no language, got %q", header, got)
		}
	}
}

func TestResolveLanguageOrder(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/lang.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acct, err := st.AddAccount("alice", "", false)
	if err != nil {
		t.Fatal(err)
	}
	req := func(accept, cookie string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			r.Header.Set("Accept-Language", accept)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: langCookie, Value: cookie})
		}
		return r
	}
	mk := func(server string) *app {
		a := &app{store: st}
		WithLanguage(server)(a)
		return a
	}
	signedIn := func(a *app, r *http.Request) lang { return a.resolveLang(r, acct.ID) }
	anon := func(a *app, r *http.Request) lang { return a.resolveLang(r, "") }

	// Signed in without a choice: server setting, then Accept-Language, then en.
	if got := signedIn(mk("de"), req("en", "en")); got != langDE {
		t.Errorf("server de, no choice: %q", got)
	}
	if got := signedIn(mk(""), req("de-DE", "en")); got != langDE {
		t.Errorf("no server setting, browser de: %q", got)
	}
	if got := signedIn(mk(""), req("", "de")); got != langEN {
		t.Errorf("the cookie is not part of the order of a signed-in person: %q", got)
	}
	// The person's own choice beats everything.
	if err := st.SetAccountLanguage(acct.ID, "en"); err != nil {
		t.Fatal(err)
	}
	if got := signedIn(mk("de"), req("de", "de")); got != langEN {
		t.Errorf("the choice of the person must win: %q", got)
	}
	// Without a sign-in: cookie, Accept-Language, server setting, en.
	if got := anon(mk("de"), req("de", "en")); got != langEN {
		t.Errorf("cookie must win on the login page: %q", got)
	}
	if got := anon(mk("en"), req("de", "")); got != langDE {
		t.Errorf("Accept-Language must win over the server on the login page: %q", got)
	}
	if got := anon(mk("de"), req("", "")); got != langDE {
		t.Errorf("server setting when the browser says nothing: %q", got)
	}
	if got := anon(mk(""), req("fr", "")); got != langEN {
		t.Errorf("English when nobody says anything: %q", got)
	}
	if got := anon(mk(""), req("", "klingon")); got != langEN {
		t.Errorf("an unknown cookie value is no choice: %q", got)
	}
}

// langEnv is a server with an interactive account and a second one.
type langEnv struct {
	srv  *httptest.Server
	st   *store.Store
	base string
}

func newLangEnv(t *testing.T, server string) langEnv {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/lang.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, WithLanguage(server)))
	t.Cleanup(srv.Close)
	return langEnv{srv: srv, st: st, base: srv.URL}
}

// client signs the account in and sends the Accept-Language header with every request.
func (e langEnv) client(t *testing.T, account, accept string) *http.Client {
	t.Helper()
	c := loginInteractive(t, e.srv, e.st, account)
	c.Transport = acceptTransport{accept: accept}
	return c
}

type acceptTransport struct{ accept string }

func (a acceptTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.accept != "" {
		r.Header.Set("Accept-Language", a.accept)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (e langEnv) page(t *testing.T, c *http.Client, path string) string {
	t.Helper()
	resp, err := c.Get(e.base + path)
	if err != nil {
		t.Fatal(err)
	}
	return body(t, resp)
}

func TestLanguageSwitchSavesOnTheAccountAndHoldsOnAnotherDevice(t *testing.T) {
	e := newLangEnv(t, "")
	phone := e.client(t, "alice", "en-US")
	page := e.page(t, phone, "/ui/inbox")
	if !strings.Contains(page, `<html lang="en"`) || !strings.Contains(page, "Sign out") {
		t.Fatal("the English browser must get English without a choice")
	}
	if !strings.Contains(page, `name="language" value="de" lang="de" aria-pressed="false"`) || !strings.Contains(page, `value="en" lang="en" aria-pressed="true"`) {
		t.Fatalf("the account menu lacks the switcher with EN pressed: %s", page)
	}
	resp := sameOriginPostForm(t, phone, e.base+"/ui/language", url.Values{"language": {"de"}, "csrf_token": {csrfOn(t, page)}, "back": {"/ui/coord?x=1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/coord?x=1" {
		t.Fatalf("the switch must return to the same page: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var set *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == langCookie {
			set = c
		}
	}
	if set == nil || set.Value != "de" || !set.HttpOnly || set.SameSite != http.SameSiteLaxMode || set.MaxAge <= 0 {
		t.Fatalf("language cookie = %+v", set)
	}
	after := e.page(t, phone, "/ui/inbox")
	if !strings.Contains(after, `<html lang="de"`) || !strings.Contains(after, "Abmelden") || strings.Contains(after, ">Sign out<") {
		t.Fatal("the page must switch at once")
	}
	if got := e.st.AccountLanguage(browserAccountID(t, e.st, "alice")); got != "de" {
		t.Fatalf("the choice is not on the account: %q", got)
	}
	// A new sign-in on another device with an English browser still gets German.
	laptop := e.client(t, "alice", "en-GB")
	if p := e.page(t, laptop, "/ui/inbox"); !strings.Contains(p, `<html lang="de"`) {
		t.Fatal("the choice must hold on another device")
	}
	if p := e.page(t, laptop, "/ui/profile"); !strings.Contains(p, `id="sprache"`) || !strings.Contains(p, "Sprache der Oberfläche") || !strings.Contains(p, `value="de" lang="de" aria-pressed="true">Deutsch<`) {
		t.Fatalf("the profile lacks the language section: %s", p)
	}
}

func browserAccountID(t *testing.T, st *store.Store, name string) string {
	t.Helper()
	acct, err := st.AccountByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return acct.ID
}

func TestLanguageSwitchRefusesForeignRequestsAndBadInput(t *testing.T) {
	e := newLangEnv(t, "")
	c := e.client(t, "alice", "")
	page := e.page(t, c, "/ui/inbox")
	token := csrfOn(t, page)
	id := browserAccountID(t, e.st, "alice")

	post := func(origin string, form url.Values) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, e.base+"/ui/language", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := post("https://evil.example", url.Values{"language": {"de"}, "csrf_token": {token}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("a foreign origin got %d", r.StatusCode)
	}
	if r := post("", url.Values{"language": {"de"}, "csrf_token": {token}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("no origin got %d", r.StatusCode)
	}
	if r := post(e.base, url.Values{"language": {"de"}, "csrf_token": {"wrong"}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("a wrong token got %d", r.StatusCode)
	}
	if r := post(e.base, url.Values{"language": {"fr"}, "csrf_token": {token}}); r.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown language got %d", r.StatusCode)
	}
	if got := e.st.AccountLanguage(id); got != "" {
		t.Fatalf("refused requests must not change the account, got %q", got)
	}
	// Without a token a signed-in browser only sets the cookie.
	r := post(e.base, url.Values{"language": {"de"}})
	if r.StatusCode != http.StatusSeeOther || e.st.AccountLanguage(id) != "" {
		t.Errorf("without a token: %d, account %q", r.StatusCode, e.st.AccountLanguage(id))
	}
	// The way back is a page of this site, never another one.
	for back, want := range map[string]string{
		"https://evil.example/": "/ui/profile",
		"//evil.example/x":      "/ui/profile",
		"/\\evil.example":       "/ui/profile",
		"/ui/logout":            "/ui/profile",
		"/ui/language":          "/ui/profile",
		"/elsewhere":            "/ui/profile",
		"/ui/issues?state=open": "/ui/issues?state=open",
	} {
		r := post(e.base, url.Values{"language": {"en"}, "csrf_token": {token}, "back": {back}})
		if got := r.Header.Get("Location"); got != want {
			t.Errorf("back %q went to %q, want %q", back, got, want)
		}
	}
}

func TestLoginPagesFollowTheSwitcherCookieAndAcceptLanguage(t *testing.T) {
	e := newLangEnv(t, "en")
	anon := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, accept, cookie string) string {
		req, _ := http.NewRequest(http.MethodGet, e.base+path, nil)
		if accept != "" {
			req.Header.Set("Accept-Language", accept)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: langCookie, Value: cookie})
		}
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return body(t, resp)
	}
	if p := get("/ui/login", "de-DE,de;q=0.9", ""); !strings.Contains(p, "Bei ghosttree anmelden") {
		t.Error("the login page must follow Accept-Language over the server setting")
	}
	if p := get("/ui/login", "de-DE", "en"); !strings.Contains(p, "Sign in to ghosttree") {
		t.Error("the cookie of the switcher must win on the login page")
	}
	p := get("/ui/login", "", "")
	if !strings.Contains(p, "Sign in to ghosttree") || !strings.Contains(p, `class="lang-pick"`) || !strings.Contains(p, `value="en" lang="en" aria-pressed="true"`) {
		t.Errorf("the login page lacks the switcher: %s", p)
	}
	if strings.Contains(p, `name="csrf_token"`) && strings.Contains(p, `action="/ui/language"`) && strings.Contains(p[strings.Index(p, `action="/ui/language"`):], `name="csrf_token" value=""`) {
		t.Error("the switcher of the login page must not carry a token")
	}
	// The switcher without a session sets the cookie and returns to the page it came from.
	req, _ := http.NewRequest(http.MethodPost, e.base+"/ui/language", strings.NewReader(url.Values{"language": {"de"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.base)
	req.Header.Set("Referer", e.base+"/ui/register?x=1")
	resp, err := anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/register?x=1" {
		t.Fatalf("anonymous switch: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == langCookie {
			cookie = c.Value
		}
	}
	if cookie != "de" {
		t.Fatalf("cookie = %q", cookie)
	}
}

func TestFirstSignInKeepsTheLanguageChosenOnTheLoginPage(t *testing.T) {
	e := newLangEnv(t, "en")
	id := browserAccountID(t, e.st, "alice")
	code, _, err := e.st.CreateAccountCode(store.CodeLogin, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	c.Jar = cookieJar{cookies: []*http.Cookie{{Name: langCookie, Value: "de"}}}
	resp := sameOriginPostForm(t, c, e.base+"/ui/login/code", url.Values{"code": {code}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in status %d", resp.StatusCode)
	}
	if got := e.st.AccountLanguage(id); got != "de" {
		t.Fatalf("the first sign-in must keep the choice of the login page, got %q", got)
	}
	// A later cookie never overwrites a choice the account has.
	if err := e.st.SetAccountLanguage(id, "en"); err != nil {
		t.Fatal(err)
	}
	code, _, _ = e.st.CreateAccountCode(store.CodeLogin, "alice")
	resp = sameOriginPostForm(t, c, e.base+"/ui/login/code", url.Values{"code": {code}})
	resp.Body.Close()
	if got := e.st.AccountLanguage(id); got != "en" {
		t.Fatalf("an existing choice must stay, got %q", got)
	}
}

func TestJoinPageSwitcherReturnsToTheInvitation(t *testing.T) {
	e := newPairEnv(t)
	anon := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	_, page := e.get(t, anon, "/join/"+e.code)
	if !strings.Contains(page, `action="/ui/language"`) || !strings.Contains(page, `name="back" value="/join/`+e.code+`"`) {
		t.Fatalf("the invitation page lacks a switcher that returns to it: %s", page)
	}
	// A page that must not leak its path (strict-origin) still returns to the invitation.
	req, _ := http.NewRequest(http.MethodPost, e.srv+"/ui/language", strings.NewReader(url.Values{"language": {"de"}, "back": {"/join/" + e.code}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.srv)
	resp, err := anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Location") != "/join/"+e.code {
		t.Fatalf("back = %q", resp.Header.Get("Location"))
	}
	// A malformed code is no page to return to.
	for _, back := range []string{"/join/zz", "/join/" + e.code + "/x", "/join/" + e.code + "?a=b"} {
		if got := safeBack(back); got != "" {
			t.Errorf("safeBack(%q) = %q", back, got)
		}
	}
}
