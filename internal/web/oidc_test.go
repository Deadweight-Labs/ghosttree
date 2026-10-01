package web

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// fakeIdP ist ein IdP im Prozess: Discovery, JWKS, Authorize und Token mit
// PKCE-Prüfung. Die Felder erlauben, gezielt ein fehlerhaftes ID-Token
// auszustellen.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	secret string

	mu       sync.Mutex
	codes    map[string]fakeGrant
	subject  string
	email    string
	username string
	audience string        // leer = Client-ID
	nonce    string        // leer = Nonce der Anfrage
	lifetime time.Duration // 0 = 5 Minuten
	exchange int
}

type fakeGrant struct{ challenge, nonce, redirect string }

const testClientID = "ghosttree-test"

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key, secret: "s3cret", codes: map[string]fakeGrant{}, subject: "sub-1", email: "robin@example.test", username: "robin"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != testClientID ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("nonce") == "" || !strings.Contains(q.Get("scope"), "openid") {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	code := "code-" + q.Get("state")[:8]
	f.mu.Lock()
	f.codes[code] = fakeGrant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
	f.mu.Unlock()
	back, _ := url.Parse(q.Get("redirect_uri"))
	v := back.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok || id != testClientID || secret != f.secret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	grant, found := f.codes[r.FormValue("code")]
	delete(f.codes, r.FormValue("code"))
	sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	if !found || grant.redirect != r.FormValue("redirect_uri") || b64(sum[:]) != grant.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	f.exchange++
	nonce := grant.nonce
	if f.nonce != "" {
		nonce = f.nonce
	}
	aud := testClientID
	if f.audience != "" {
		aud = f.audience
	}
	life := f.lifetime
	if life == 0 {
		life = 5 * time.Minute
	}
	now := time.Now()
	idToken := f.sign(map[string]any{
		"iss": f.srv.URL, "sub": f.subject, "aud": aud, "nonce": nonce,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(life).Unix(),
		"email": f.email, "preferred_username": f.username,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idToken})
}

func (f *fakeIdP) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

type oidcEnv struct {
	idp    *fakeIdP
	web    *httptest.Server
	store  *store.Store
	dbPath string
	token  string // Token der Person "alice" (person:1), falls angelegt
}

// newOIDCEnv startet Web-UI und IdP. withAlice legt person:1 an (bestehende
// Instanz); sonst ist die Instanz leer.
func newOIDCEnv(t *testing.T, withAlice bool, opts ...Option) *oidcEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "oidc.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	env := &oidcEnv{idp: newFakeIdP(t), store: st, dbPath: dbPath}
	if withAlice {
		if env.token, err = st.AddPerson("alice"); err != nil {
			t.Fatal(err)
		}
	}
	var handler http.Handler
	env.web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(env.web.Close)
	all := append([]Option{WithOIDC(OIDCConfig{
		Issuer: env.idp.srv.URL, ClientID: testClientID, ClientSecret: env.idp.secret,
		RedirectURL: env.web.URL + "/ui/login/oidc/callback",
	})}, opts...)
	handler = New(st, all...)
	return env
}

func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// startFlow macht Schritt 1 und 2 und liefert die Callback-URL des IdP.
func (e *oidcEnv) startFlow(t *testing.T, b *http.Client, code string) *url.URL {
	t.Helper()
	resp := sameOriginPostForm(t, b, e.web.URL+"/ui/login/oidc", url.Values{"code": {code}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("start status=%d body=%s", resp.StatusCode, body(t, resp))
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if !strings.HasPrefix(loc, e.idp.srv.URL+"/authorize?") {
		t.Fatalf("start redirect=%q", loc)
	}
	resp, err := b.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status=%d", resp.StatusCode)
	}
	cb, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return cb
}

func (e *oidcEnv) callback(t *testing.T, b *http.Client, cb *url.URL) *http.Response {
	t.Helper()
	resp, err := b.Get(cb.String())
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *oidcEnv) signedIn(t *testing.T, b *http.Client) bool {
	t.Helper()
	resp, err := b.Get(e.web.URL + "/ui/requests")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (e *oidcEnv) claimCode(t *testing.T, account string) string {
	t.Helper()
	code, _, err := e.store.CreateAccountCode(store.CodeClaim, account)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestOIDCClaimFlowBindsPerson1ThenSignsInWithoutCode(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, env.claimCode(t, "alice")))
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/requests" {
		t.Fatalf("callback status=%d body=%s", resp.StatusCode, body(t, resp))
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	resp.Body.Close()
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie=%+v", session)
	}
	if !env.signedIn(t, b) {
		t.Fatal("session from OIDC login does not open the UI")
	}
	account, err := env.store.AccountByName("alice")
	if err != nil || account.ID != "person:1" || account.Email != "robin@example.test" {
		t.Fatalf("account=%+v err=%v", account, err)
	}
	// Zweiter Login derselben Identität braucht keinen Code und landet auf person:1.
	b2 := newBrowser(t)
	resp = env.callback(t, b2, env.startFlow(t, b2, ""))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !env.signedIn(t, b2) {
		t.Fatalf("second login status=%d", resp.StatusCode)
	}
}

func TestOIDCRejectsWrongState(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	cb := env.startFlow(t, b, env.claimCode(t, "alice"))
	q := cb.Query()
	q.Set("state", strings.Repeat("0", 64))
	cb.RawQuery = q.Encode()
	resp := env.callback(t, b, cb)
	if resp.StatusCode != http.StatusBadRequest || env.signedIn(t, b) || env.idp.exchange != 0 {
		t.Fatalf("status=%d exchanges=%d", resp.StatusCode, env.idp.exchange)
	}
	resp.Body.Close()
}

func TestOIDCRejectsCallbackFromAnotherBrowserAndReplay(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	cb := env.startFlow(t, b, env.claimCode(t, "alice"))
	// Ein anderer Browser hat das Flow-Cookie nicht (Login-CSRF).
	other := newBrowser(t)
	resp := env.callback(t, other, cb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || env.signedIn(t, other) {
		t.Fatalf("foreign browser status=%d", resp.StatusCode)
	}
	// Der Ablauf ist einmalig: nach Erfolg ist derselbe Callback tot.
	resp = env.callback(t, b, cb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use status=%d", resp.StatusCode)
	}
	again := newBrowser(t)
	again.Jar.SetCookies(mustURL(env.web.URL), b.Jar.Cookies(mustURL(env.web.URL+"/ui/login/oidc/callback")))
	resp = env.callback(t, again, cb)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replay status=%d", resp.StatusCode)
	}
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestOIDCRejectsBadIDTokens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(*fakeIdP)
	}{
		{"wrong nonce", func(f *fakeIdP) { f.nonce = "evil-nonce" }},
		{"wrong audience", func(f *fakeIdP) { f.audience = "another-client" }},
		{"expired", func(f *fakeIdP) { f.lifetime = -time.Hour }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOIDCEnv(t, true)
			tc.tamper(env.idp)
			b := newBrowser(t)
			code := env.claimCode(t, "alice")
			resp := env.callback(t, b, env.startFlow(t, b, code))
			text := body(t, resp)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Sign-in failed") {
				t.Fatalf("status=%d body=%s", resp.StatusCode, text)
			}
			if env.signedIn(t, b) || env.store.HasIdentities() {
				t.Fatal("a rejected token created a session or an identity")
			}
			// Der Claim-Code bleibt nutzbar, weil der Login scheiterte.
			if env.store.CodeKindFor(code) != store.CodeClaim {
				t.Fatal("claim code was consumed by a failed login")
			}
		})
	}
}

func TestOIDCUnknownIdentityWithoutInvitationIsRejectedWithAPage(t *testing.T) {
	env := newOIDCEnv(t, true)
	env.idp.subject = "stranger"
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, ""))
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "No ghosttree account for this identity") || !strings.Contains(text, "invitation") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, text)
	}
	if env.signedIn(t, b) {
		t.Fatal("stranger got a session")
	}
	if accounts, _ := env.store.ListAccounts(); len(accounts) != 1 {
		t.Fatalf("accounts=%+v", accounts)
	}
	// Ein erfundener Code ändert daran nichts.
	resp = env.callback(t, b, env.startFlow(t, b, "made-up"))
	text = body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Code not accepted") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, text)
	}
}

func TestOIDCClaimCodeIsSingleUseAndExpires(t *testing.T) {
	env := newOIDCEnv(t, true)
	code := env.claimCode(t, "alice")
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	// Eine zweite Identität mit demselben Code: abgewiesen.
	env.idp.subject = "thief"
	b2 := newBrowser(t)
	resp = env.callback(t, b2, env.startFlow(t, b2, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || env.signedIn(t, b2) {
		t.Fatalf("reuse status=%d", resp.StatusCode)
	}

	env2 := newOIDCEnv(t, true)
	expired := env2.claimCode(t, "alice")
	// Ablauf über die Uhr der Datenbank: der Code ist 30 Minuten gültig.
	rawExec(t, env2, `UPDATE account_codes SET expires_at='2000-01-01T00:00:00Z'`)
	b3 := newBrowser(t)
	resp = env2.callback(t, b3, env2.startFlow(t, b3, expired))
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Code not accepted") || env2.signedIn(t, b3) {
		t.Fatalf("expired status=%d body=%s", resp.StatusCode, text)
	}
}

func TestOIDCBootstrapOnEmptyInstanceCreatesAdminAndRemovesFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bootstrap-code")
	env := newOIDCEnv(t, false, WithBootstrapFile(file))
	code, ok, err := env.store.EnsureBootstrapCode()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if err := os.WriteFile(file, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !env.signedIn(t, b) {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	accounts, _ := env.store.ListAccounts()
	if len(accounts) != 1 || accounts[0].Name != "robin" || !accounts[0].Admin {
		t.Fatalf("accounts=%+v", accounts)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("bootstrap file still there: %v", err)
	}
	// Nach dem ersten Konto ist der Code tot.
	env.idp.subject = "second"
	b2 := newBrowser(t)
	resp = env.callback(t, b2, env.startFlow(t, b2, code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || env.signedIn(t, b2) {
		t.Fatalf("second bootstrap status=%d", resp.StatusCode)
	}
}

func TestLoginLinkIsConfirmedByPostAndSingleUse(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	defer srv.Close()
	code, _, err := st.CreateAccountCode(store.CodeLogin, "alice")
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t)
	link := srv.URL + "/ui/login/code?code=" + code
	// Ein GET (Link-Vorschau) verbraucht den Code nicht und meldet nicht an.
	for i := 0; i < 2; i++ {
		resp, err := b.Get(link)
		if err != nil {
			t.Fatal(err)
		}
		text := body(t, resp)
		if resp.StatusCode != http.StatusOK || !strings.Contains(text, `method="post"`) || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("status=%d referrer=%q", resp.StatusCode, resp.Header.Get("Referrer-Policy"))
		}
	}
	if resp, _ := b.Get(srv.URL + "/ui/requests"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET signed in: %d", resp.StatusCode)
	}
	resp := sameOriginPostForm(t, b, srv.URL+"/ui/login/code", url.Values{"code": {code}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/requests" {
		t.Fatalf("post status=%d", resp.StatusCode)
	}
	if resp, _ := b.Get(srv.URL + "/ui/requests"); resp.StatusCode != http.StatusOK {
		t.Fatalf("session after link: %d", resp.StatusCode)
	}
	b2 := newBrowser(t)
	resp = sameOriginPostForm(t, b2, srv.URL+"/ui/login/code", url.Values{"code": {code}})
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Code not accepted") {
		t.Fatalf("reuse status=%d body=%s", resp.StatusCode, text)
	}
	// Ohne Same-Origin-Nachweis wird nichts eingelöst.
	c2, _, _ := st.CreateAccountCode(store.CodeLogin, "alice")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ui/login/code", strings.NewReader(url.Values{"code": {c2}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || st.CodeKindFor(c2) != store.CodeLogin {
		t.Fatalf("cross-site post status=%d", resp.StatusCode)
	}
}

func TestLocalBootstrapCodeCreatesFirstAccountWithoutIdP(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	code, _, _ := st.EnsureBootstrapCode()
	srv := httptest.NewServer(New(st))
	defer srv.Close()
	b := newBrowser(t)
	resp, _ := b.Get(srv.URL + "/ui/login/code?code=" + code)
	if text := body(t, resp); !strings.Contains(text, `name="name"`) {
		t.Fatalf("bootstrap page asks for no name: %s", text)
	}
	resp = sameOriginPostForm(t, b, srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"owner"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if a, err := st.AccountByName("owner"); err != nil || !a.Admin {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if resp, _ := b.Get(srv.URL + "/ui/requests"); resp.StatusCode != http.StatusOK {
		t.Fatalf("session: %d", resp.StatusCode)
	}
}

func TestAccountSessionDiesWhenAccountIsDisabled(t *testing.T) {
	env := newOIDCEnv(t, true)
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlow(t, b, env.claimCode(t, "alice")))
	resp.Body.Close()
	if !env.signedIn(t, b) {
		t.Fatal("not signed in")
	}
	rawExec(t, env, `UPDATE persons SET state='disabled'`)
	if env.signedIn(t, b) {
		t.Fatal("session survived account disable")
	}
}

func TestTokenPasteLoginStaysUntilAnIdentityExists(t *testing.T) {
	env := newOIDCEnv(t, true)
	anon := newBrowser(t)
	resp := sameOriginPostForm(t, anon, env.web.URL+"/ui/login", url.Values{"token": {env.token}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("paste before claim status=%d", resp.StatusCode)
	}
	page, _ := anon.Get(env.web.URL + "/ui/login")
	if text := body(t, page); !strings.Contains(text, `name="token"`) || !strings.Contains(text, "Sign in with OIDC") {
		t.Fatalf("login page before claim: %s", text)
	}
	b := newBrowser(t)
	r := env.callback(t, b, env.startFlow(t, b, env.claimCode(t, "alice")))
	r.Body.Close()
	resp = sameOriginPostForm(t, anon, env.web.URL+"/ui/login", url.Values{"token": {env.token}})
	text := body(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(text, "Token login is disabled") {
		t.Fatalf("paste after claim status=%d body=%s", resp.StatusCode, text)
	}
	page, _ = anon.Get(env.web.URL + "/ui/login")
	if text := body(t, page); strings.Contains(text, `name="token"`) {
		t.Fatal("login page still offers the token form after a claim")
	}
	// Der Token selbst gilt weiter für CLI und API.
	if _, ok := env.store.AuthenticatePrincipal(env.token); !ok {
		t.Fatal("token stopped working for the API")
	}
}

func TestWithoutOIDCTheLoginPageStaysAsBefore(t *testing.T) {
	srv, _, _ := testWeb(t)
	resp, _ := http.Get(srv.URL + "/ui/login")
	text := body(t, resp)
	if !strings.Contains(text, `name="token"`) || strings.Contains(text, "OIDC") {
		t.Fatalf("page=%s", text)
	}
	resp = sameOriginPostForm(t, newBrowser(t), srv.URL+"/ui/login/oidc", url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oidc start without config status=%d", resp.StatusCode)
	}
}

func TestSessionAndFlowCookiesAreSecureOnlyOverHTTPS(t *testing.T) {
	cookies := func(t *testing.T, srv *httptest.Server, client *http.Client, token string) (flow, session *http.Cookie) {
		t.Helper()
		resp := sameOriginPostForm(t, client, srv.URL+"/ui/login", url.Values{"token": {token}})
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie {
				session = c
			}
		}
		return nil, session
	}
	for _, tls := range []bool{false, true} {
		st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
		if err != nil {
			t.Fatal(err)
		}
		token, _ := st.AddPerson("alice")
		srv := httptest.NewUnstartedServer(New(st))
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if tls {
			srv.StartTLS()
			client = srv.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		} else {
			srv.Start()
		}
		_, session := cookies(t, srv, client, token)
		if session == nil || session.Secure != tls || !session.HttpOnly {
			t.Fatalf("tls=%v session cookie=%+v", tls, session)
		}
		srv.Close()
		st.Close()
	}

	// Mit https-Redirect-URL sind auch das Flow-Cookie und das Sitzungs-Cookie
	// Secure, selbst wenn ein Proxy davor TLS terminiert.
	st, err := store.Open(filepath.Join(t.TempDir(), "https.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idp := newFakeIdP(t)
	srv := httptest.NewServer(New(st, WithOIDC(OIDCConfig{
		Issuer: idp.srv.URL, ClientID: testClientID, ClientSecret: idp.secret,
		RedirectURL: "https://gt.example.test/ui/login/oidc/callback",
	})))
	defer srv.Close()
	resp := sameOriginPostForm(t, newBrowser(t), srv.URL+"/ui/login/oidc", url.Values{})
	resp.Body.Close()
	var flow *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == flowCookie {
			flow = c
		}
	}
	if flow == nil || !flow.Secure || !flow.HttpOnly || flow.SameSite != http.SameSiteLaxMode || flow.MaxAge <= 0 {
		t.Fatalf("flow cookie=%+v", flow)
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	good := OIDCConfig{Issuer: "https://id.example.test", ClientID: "c", RedirectURL: "https://gt.example.test/ui/login/oidc/callback"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (OIDCConfig{}).Validate(); err != nil {
		t.Fatalf("empty config must be fine: %v", err)
	}
	for name, mutate := range map[string]func(*OIDCConfig){
		"missing client id": func(c *OIDCConfig) { c.ClientID = "" },
		"missing redirect":  func(c *OIDCConfig) { c.RedirectURL = "" },
		"plain http issuer": func(c *OIDCConfig) { c.Issuer = "http://id.example.test" },
		"wrong callback":    func(c *OIDCConfig) { c.RedirectURL = "https://gt.example.test/cb" },
	} {
		c := good
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	local := OIDCConfig{Issuer: "http://127.0.0.1:9999", ClientID: "c", RedirectURL: "http://localhost:8474/ui/login/oidc/callback"}
	if err := local.Validate(); err != nil {
		t.Fatalf("loopback http: %v", err)
	}
}

// rawExec ändert die Datenbank an der Anwendung vorbei: Ablauf und
// Deaktivierung gibt es in der API noch nicht.
func rawExec(t *testing.T, env *oidcEnv, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
