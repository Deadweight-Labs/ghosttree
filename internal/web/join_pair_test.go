package web

import (
	"bytes"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var (
	pairRE  = regexp.MustCompile(`[BCDFGHJKMNPQRTVWX34679]{4}-[BCDFGHJKMNPQRTVWX34679]{4}`)
	nonceRE = regexp.MustCompile(`name="nonce" value="([0-9a-f]+)"`)
)

type testClock struct{ t time.Time }

type pairEnv struct {
	hs      *httptest.Server
	srv     string
	st      *store.Store
	org     store.Org
	code    string // Einladung für ein noch nicht vorhandenes Konto
	annaTok string
	clock   *testClock
}

func newPairEnv(t *testing.T) pairEnv {
	t.Helper()
	srv, st, _, org, _ := joinWeb(t)
	annaTok, err := st.AddPerson("anna")
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: time.Now()}
	st.Device().SetClock(func() time.Time { return clock.t })
	st.Join().SetClock(func() time.Time { return clock.t })
	return pairEnv{hs: srv, srv: srv.URL, st: st, org: org, annaTok: annaTok, code: projectInvite(t, st, org, store.RoleMember), clock: clock}
}

// browser ist ein Browser mit Cookie-Speicher, der keinen Weiterleitungen folgt.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signInAs meldet ein vorhandenes Konto über einen Login-Link in diesem Browser an.
func (e pairEnv) signInAs(t *testing.T, c *http.Client, name string) {
	t.Helper()
	code, _, err := e.st.CreateAccountCode(store.CodeLogin, name)
	if err != nil {
		t.Fatal(err)
	}
	resp := sameOriginPostForm(t, c, e.srv+"/ui/login/code", url.Values{"code": {code}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", resp.StatusCode)
	}
}

func (e pairEnv) get(t *testing.T, c *http.Client, path string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(e.srv + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body(t, resp)
}

func (e pairEnv) pairOf(t *testing.T, c *http.Client, code string) string {
	t.Helper()
	resp, text := e.get(t, c, "/join/"+code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join page: %d", resp.StatusCode)
	}
	pair := pairRE.FindString(text)
	if pair == "" {
		t.Fatalf("no pairing code on the join page: %s", text)
	}
	return pair
}

func csrfOn(t *testing.T, text string) string {
	t.Helper()
	m := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no csrf token: %s", text)
	}
	return m[1]
}

// accept nimmt die Einladung im angemeldeten Browser an.
func (e pairEnv) accept(t *testing.T, c *http.Client, code string) *http.Response {
	t.Helper()
	_, page := e.get(t, c, "/join/"+code)
	resp := sameOriginPostForm(t, c, e.srv+"/join/"+code+"/accept", url.Values{"csrf_token": {csrfOn(t, page)}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("accept: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return resp
}

func (e pairEnv) pairPage(t *testing.T, c *http.Client) string {
	t.Helper()
	resp, text := e.get(t, c, "/join/pair")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair page: %d %s", resp.StatusCode, text)
	}
	return text
}

func loopClaim(pair, machine, addr string) store.JoinClaimRequest {
	return store.JoinClaimRequest{Addr: addr, Pair: pair, Machine: machine, Challenge: strings.Repeat("A", 43), State: "state-12345678", Port: 40123}
}

func (e pairEnv) claimLoop(t *testing.T, pair, machine string) {
	t.Helper()
	if _, err := e.st.Join().Claim(loopClaim(pair, machine, "127.0.0.1")); err != nil {
		t.Fatalf("claim: %v", err)
	}
}

func (e pairEnv) claimCode(t *testing.T, pair, machine string) store.JoinClaim {
	t.Helper()
	c, err := e.st.Join().Claim(store.JoinClaimRequest{Addr: "127.0.0.1", Pair: pair, Machine: machine})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return c
}

func (e pairEnv) decide(t *testing.T, c *http.Client, form url.Values) *http.Response {
	t.Helper()
	_, page := e.get(t, c, "/join/pair")
	form.Set("csrf_token", csrfOn(t, page))
	if form.Get("nonce") == "" {
		if m := nonceRE.FindStringSubmatch(page); m != nil {
			form.Set("nonce", m[1])
		}
	}
	return sameOriginPostForm(t, c, e.srv+"/join/pair/decide", form)
}

func (e pairEnv) poll(t *testing.T, deviceCode string) error {
	t.Helper()
	e.clock.t = e.clock.t.Add(time.Minute)
	_, _, err := e.st.Device().Poll(deviceCode)
	return err
}

func TestJoinPageShowsCommandAndPairingCodeBeforeAnyLoginAndKeepsItOnReload(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	resp, text := e.get(t, b, "/join/"+e.code)
	pair := pairRE.FindString(text)
	if pair == "" || !strings.Contains(text, "| sh -s -- --pair "+pair) || !strings.Contains(text, "Run only on your own machine.") {
		t.Fatalf("no command: %s", text)
	}
	if strings.Contains(text[strings.Index(text, "<pre"):strings.Index(text, "</pre>")], e.code) {
		t.Fatal("the invitation code is in the command")
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "gt_join" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || strings.Contains(cookie.Value, pair) || strings.Contains(cookie.Value, e.code) {
		t.Fatalf("cookie %+v", cookie)
	}
	// Dasselbe Cookie: dieselbe Sitzung, kein neues Cookie.
	resp2, text2 := e.get(t, b, "/join/"+e.code)
	if pairRE.FindString(text2) != pair || len(resp2.Cookies()) != 0 || e.st.Join().Sessions() != 1 {
		t.Fatalf("reload changed the session: %d cookies=%v", e.st.Join().Sessions(), resp2.Cookies())
	}
	// Das Öffnen verbraucht nichts.
	if _, err := e.st.PreviewInvitation(e.code, true); err != nil {
		t.Fatalf("the invitation was used: %v", err)
	}
}

func TestJoinInvalidInvitationsMakeNoSessionAndNoCookie(t *testing.T) {
	e := newPairEnv(t)
	for _, code := range []string{strings.Repeat("ab", 32), "short"} {
		resp, _ := e.get(t, browser(t), "/join/"+code)
		if resp.StatusCode != http.StatusNotFound || len(resp.Cookies()) != 0 {
			t.Fatalf("%s: %d %v", code, resp.StatusCode, resp.Cookies())
		}
	}
	if e.st.Join().Sessions() != 0 {
		t.Fatal("an invalid invitation created a session")
	}
}

// Installation vor der Anmeldung: der Installer wartet schon, dann meldet sich der Eingeladene an.
func TestJoinInstallerFirstThenSignInWithTheInvitation(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	pair := e.pairOf(t, b, e.code)
	e.claimLoop(t, pair, "philipps-laptop")
	// Anmeldung über die Join-Seite: neues Konto, Sitzung wird gebunden.
	resp := sameOriginPostForm(t, b, e.srv+"/ui/login/code", url.Values{"code": {e.code}, "name": {"philipp"}, "join": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("sign-in: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	text := e.pairPage(t, b)
	if !strings.Contains(text, "philipps-laptop") || !strings.Contains(text, "wants to connect") || !strings.Contains(text, `value="approve"`) {
		t.Fatalf("approval page: %s", text)
	}
	acct, _ := e.st.AccountByName("philipp")
	if v := e.st.Join().View(acct.ID); v.State != store.JoinClaimed {
		t.Fatalf("session %q", v.State)
	}
	// Freigabe: Loopback-Weg, 303 auf den beim Claim genannten Port.
	resp = e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"philipp"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "http://127.0.0.1:40123/callback?code=") || !strings.Contains(loc, "state=state-12345678") {
		t.Fatalf("approve: %d %q", resp.StatusCode, loc)
	}
}

// Anmeldung vor der Installation: das Konto ist gebunden, die Seite wartet, dann meldet sich das Gerät.
func TestJoinLoginFirstThenInstaller(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	text := e.pairPage(t, b)
	if !strings.Contains(text, pair) || strings.Contains(text, "wants to connect") || strings.Contains(text, `value="approve"`) {
		t.Fatalf("waiting page: %s", text)
	}
	// Ohne Gerät gibt es nichts freizugeben.
	if resp := e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "nonce": {"x"}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("early approve: %d", resp.StatusCode)
	}
	e.claimLoop(t, strings.ToLower(pair), "annas-laptop")
	text = e.pairPage(t, b)
	for _, want := range []string{"annas-laptop", "wants to connect", `value="approve"`, `value="deny"`, "Same network as this browser: <strong>yes</strong>"} {
		if !strings.Contains(text, want) {
			t.Errorf("approval page lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Your terminal shows") {
		t.Fatal("the loopback path asks for a typed code")
	}
	// Meta-Refresh steht im head und die Seite bietet "Check again".
	if i, j := strings.Index(text, `http-equiv="refresh"`), strings.Index(text, "<title>"); i < 0 || i > j || !strings.Contains(text, "Check again") {
		t.Fatalf("refresh not in head: %s", text)
	}
	if _, err := e.st.PreviewInvitation(e.code, true); err == nil {
		t.Fatal("invitation still valid after accept")
	}
}

func TestJoinSameNetworkLineSaysNoForAnotherNetwork(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	if _, err := e.st.Join().Claim(loopClaim(pair, "box", "203.0.113.5")); err != nil {
		t.Fatal(err)
	}
	if text := e.pairPage(t, b); !strings.Contains(text, "Same network as this browser: <strong>no</strong>") || !strings.Contains(text, "203.0.113.5") {
		t.Fatalf("page: %s", text)
	}
}

func TestJoinCodeFallbackNeedsTheTerminalCodeAndDeliversThroughTheDeviceFlow(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	claim := e.claimCode(t, pair, "annas-laptop")
	text := e.pairPage(t, b)
	if !strings.Contains(text, "Your terminal shows") || strings.Contains(text, claim.Confirm) || strings.Contains(text, claim.DeviceCode) {
		t.Fatalf("approval page: %s", text)
	}
	if err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatal(err)
	}
	resp := e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "confirm_code": {"WWWW"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	if err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatalf("a wrong code decided the flow: %v", err)
	}
	resp = e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "confirm_code": {strings.ToLower(claim.Confirm)}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("approve: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if err := e.poll(t, claim.DeviceCode); err != nil {
		t.Fatalf("token: %v", err)
	}
}

func TestJoinCodeFallbackThreeWrongCodesCompromiseTheSession(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	claim := e.claimCode(t, pair, "box")
	for i := 0; i < 3; i++ {
		e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "confirm_code": {"WWWW"}}).Body.Close()
	}
	if text := e.pairPage(t, b); !strings.Contains(text, "Another device tried to use this code") || !strings.Contains(text, "New code") {
		t.Fatalf("page: %s", text)
	}
	if err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDeviceUnknown) {
		t.Fatalf("flow alive: %v", err)
	}
}

func TestJoinSecondClaimShowsTheWarningAndOffersANewCode(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "mine")
	if _, err := e.st.Join().Claim(loopClaim(pair, "thief", "198.51.100.7")); err == nil {
		t.Fatal("second claim worked")
	}
	text := e.pairPage(t, b)
	if !strings.Contains(text, "Another device tried to use this code") || strings.Contains(text, "thief") || strings.Contains(text, `value="approve"`) {
		t.Fatalf("page: %s", text)
	}
	resp := sameOriginPostForm(t, b, e.srv+"/join/pair", url.Values{"csrf_token": {csrfOn(t, text)}})
	resp.Body.Close()
	if fresh := pairRE.FindString(e.pairPage(t, b)); fresh == "" || fresh == pair {
		t.Fatalf("no fresh code: %q", fresh)
	}
}

func TestJoinApprovalNeedsInteractiveSessionCSRFOriginConfirmationAndTheShownRequest(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	_, page := e.get(t, b, "/join/pair")
	csrf, nonce := csrfOn(t, page), nonceRE.FindStringSubmatch(page)[1]
	target := e.srv + "/join/pair/decide"
	good := func() url.Values {
		return url.Values{"csrf_token": {csrf}, "nonce": {nonce}, "decision": {"approve"}, "confirm_account": {"anna"}}
	}
	// Anonym.
	if resp := sameOriginPostForm(t, anonClient(), target, good()); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	// Eingefügtes Token: 403 beim Lesen der Seite und beim Entscheiden.
	pasted := login(t, e.hs, e.annaTok)
	if resp, _ := pasted.Get(e.srv + "/join/pair"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pasted token GET: %d", resp.StatusCode)
	}
	form := good()
	form.Set("csrf_token", renderedCSRFToken(t, pasted, e.srv+"/ui/orgs"))
	if resp := sameOriginPostForm(t, pasted, target, form); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pasted token POST: %d", resp.StatusCode)
	}
	// CSRF fehlt oder falsch; fremder Origin.
	for _, tok := range []string{"", "nope"} {
		f := good()
		f.Set("csrf_token", tok)
		if resp := sameOriginPostForm(t, b, target, f); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("csrf %q: %d", tok, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", target, strings.NewReader(good().Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := b.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
	// Kontobestätigung fehlt oder falsch: 400. Kennung fehlt oder alt: 409.
	for _, confirm := range []string{"", "ben"} {
		f := good()
		f.Set("confirm_account", confirm)
		if resp := sameOriginPostForm(t, b, target, f); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("confirm %q: %d", confirm, resp.StatusCode)
		}
	}
	for _, n := range []string{"", "stale"} {
		f := good()
		f.Set("nonce", n)
		if resp := sameOriginPostForm(t, b, target, f); resp.StatusCode != http.StatusConflict {
			t.Fatalf("nonce %q: %d", n, resp.StatusCode)
		}
	}
	if v := e.st.Join().View("person:2"); v.State != store.JoinClaimed {
		t.Fatalf("a refused request changed the state: %q", v.State)
	}
	// Alter Tab: nach Ablehnen und neuem Code gilt die alte Kennung nicht mehr.
	sameOriginPostForm(t, b, target, url.Values{"csrf_token": {csrf}, "nonce": {nonce}, "decision": {"deny"}}).Body.Close()
	sameOriginPostForm(t, b, e.srv+"/join/pair", url.Values{"csrf_token": {csrf}}).Body.Close()
	fresh := pairRE.FindString(e.pairPage(t, b))
	e.claimLoop(t, fresh, "other-box")
	if resp := sameOriginPostForm(t, b, target, good()); resp.StatusCode != http.StatusConflict {
		t.Fatalf("old tab approved a new device: %d", resp.StatusCode)
	}
}

func TestJoinAnotherAccountNeitherSeesNorDecidesAnnasSession(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "annas-laptop")
	e.st.AddPerson("ben")
	ben := browser(t)
	e.signInAs(t, ben, "ben")
	if text := e.pairPage(t, ben); strings.Contains(text, pair) || strings.Contains(text, "annas-laptop") {
		t.Fatalf("ben sees annas session: %s", text)
	}
	_, annaPage := e.get(t, b, "/join/pair")
	form := url.Values{"nonce": {nonceRE.FindStringSubmatch(annaPage)[1]}, "decision": {"approve"}, "confirm_account": {"ben"}}
	if resp := e.decide(t, ben, form); resp.StatusCode != http.StatusConflict {
		t.Fatalf("ben approve: %d", resp.StatusCode)
	}
	if v := e.st.Join().View("person:2"); v.State != store.JoinClaimed {
		t.Fatalf("ben decided: %q", v.State)
	}
}

func TestJoinConnectThisMachineForASignedInAccount(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	text := e.pairPage(t, b)
	if !strings.Contains(text, "Connect this machine") || strings.Contains(text, "Setup was interrupted") {
		t.Fatalf("start page: %s", text)
	}
	target, csrf := e.srv+"/join/pair", csrfOn(t, text)
	resp := sameOriginPostForm(t, b, target, url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || e.st.Join().Sessions() != 0 {
		t.Fatalf("unconfirmed: %d", resp.StatusCode)
	}
	resp = sameOriginPostForm(t, b, target, url.Values{"csrf_token": {csrf}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || e.st.Join().Sessions() != 1 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	waiting := e.pairPage(t, b)
	first := pairRE.FindString(waiting)
	for _, want := range []string{"| sh -s -- --pair " + first, "ctx login --server", "Open ghosttree", "Run only on your own machine."} {
		if !strings.Contains(waiting, want) {
			t.Errorf("waiting page lacks %q", want)
		}
	}
	resp = sameOriginPostForm(t, b, target, url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	if second := pairRE.FindString(e.pairPage(t, b)); resp.StatusCode != http.StatusSeeOther || second == first {
		t.Fatalf("renew: %d %s %s", resp.StatusCode, first, second)
	}
	if _, err := e.st.Join().Claim(loopClaim(first, "m", "1.1.1.1")); err != store.ErrJoinInvalid {
		t.Fatalf("old code: %v", err)
	}
}

func TestJoinPairPageAfterLossOfTheSessionSaysSetupWasInterrupted(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.clock.t = e.clock.t.Add(store.JoinSessionTTL + time.Minute) // oder ein Neustart
	_, text := e.get(t, b, "/join/pair?w=1")
	if !strings.Contains(text, "Setup was interrupted") || !strings.Contains(text, "Start again") {
		t.Fatalf("page: %s", text)
	}
	if _, text := e.get(t, b, "/join/pair"); strings.Contains(text, "interrupted") {
		t.Fatalf("a first visit looks interrupted: %s", text)
	}
}

func TestJoinApproveRefusesAMachineNameOfAnotherAccount(t *testing.T) {
	e := newPairEnv(t)
	e.st.AddPerson("ben")
	if _, _, err := e.st.CreateDeviceToken("person:3", "shared-name"); err != nil {
		t.Fatal(err)
	}
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "shared-name")
	resp := e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	if v := e.st.Join().View("person:2"); v.State != store.JoinClaimed {
		t.Fatalf("state %q", v.State)
	}
}

func TestJoinCommandNeedsHTTPSOrLoopbackAndIgnoresUntrustedForwardedHeaders(t *testing.T) {
	e := newPairEnv(t)
	// Ein Server, der keinem Proxy vertraut: X-Forwarded-* zählt nicht.
	none, _ := proxytrust.Parse("none")
	srv := httptest.NewServer(New(e.st, WithTrustedProxies(none)))
	t.Cleanup(srv.Close)
	get := func(host string, hdr map[string]string) string {
		req, _ := http.NewRequest("GET", srv.URL+"/join/"+e.code, nil)
		req.Host = host
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return body(t, resp)
	}
	if text := get("gt.example", nil); strings.Contains(text, "<pre") || !strings.Contains(text, "Valid until") {
		t.Fatalf("http command for a public host: %s", text)
	}
	// X-Forwarded-Proto von einem nicht vertrauten Absender zählt nicht.
	if text := get("gt.example", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "gt.example"}); strings.Contains(text, "<pre") {
		t.Fatalf("forwarded headers were believed: %s", text)
	}
	if text := get("localhost:8474", nil); !strings.Contains(text, "curl -fsSL http://localhost:8474/install.sh") {
		t.Fatalf("loopback command: %s", text)
	}
}

func TestJoinSignInThroughTheJoinPageBindsTheBrowsersSessionAndOnlyThen(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	pair := e.pairOf(t, b, e.code)
	resp := sameOriginPostForm(t, b, e.srv+"/ui/login/code", url.Values{"code": {e.code}, "name": {"philipp"}, "join": {"1"}})
	resp.Body.Close()
	acct, _ := e.st.AccountByName("philipp")
	if v := e.st.Join().View(acct.ID); v.State != store.JoinWaiting || v.Pair != pair {
		t.Fatalf("session %+v (page showed %s)", v, pair)
	}
	// Ohne Marker bleibt alles wie bisher.
	other := projectInvite(t, e.st, e.org, store.RoleMember)
	resp = sameOriginPostForm(t, anonClient(), e.srv+"/ui/login/code", url.Values{"code": {other}, "name": {"paula"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/ui/requests" {
		t.Fatalf("without marker: %s", resp.Header.Get("Location"))
	}
	// Ein Browser ohne Cookie (anderes Gerät) bekommt eine eigene neue Sitzung, nie die eines anderen.
	third := projectInvite(t, e.st, e.org, store.RoleMember)
	pairThird := e.pairOf(t, browser(t), third)
	resp = sameOriginPostForm(t, browser(t), e.srv+"/ui/login/code", url.Values{"code": {third}, "name": {"tina"}, "join": {"1"}})
	resp.Body.Close()
	tina, _ := e.st.AccountByName("tina")
	if v := e.st.Join().View(tina.ID); v.State != store.JoinWaiting || v.Pair == pairThird {
		t.Fatalf("cookie-less sign-in took over a session: %+v", v)
	}
}

func TestJoinOIDCSignInBindsTheSessionAndLandsOnThePairingPage(t *testing.T) {
	env := newOIDCEnv(t, true)
	env.store.SetAccessMode(store.AccessMode{Enforce: true})
	org, _ := env.store.CreateOrg("person:1", "Alpha", "alpha")
	if _, err := env.store.EnsureProject("person:1", joinProject); err != nil {
		t.Fatal(err)
	}
	code, _, err := env.store.CreateProjectInvitation("person:1", org.ID, joinProject, store.RoleMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t)
	page, _ := b.Get(env.web.URL + "/join/" + code)
	pair := pairRE.FindString(body(t, page))
	if pair == "" || !strings.Contains(joinPageText(t, b, env.web.URL+"/join/"+code), `name="join" value="1"`) {
		t.Fatalf("join page lacks pair or marker (%q)", pair)
	}
	// Der Installer ist schon da.
	if _, err := env.store.Join().Claim(loopClaim(pair, "box", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	env.idp.subject, env.idp.username, env.idp.email = "sub-philipp", "philipp", "philipp@example.test"
	resp := env.callback(t, b, env.startFlowWith(t, b, url.Values{"code": {code}, "join": {"1"}}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	acct, err := env.store.AccountByName("philipp")
	if err != nil {
		t.Fatal(err)
	}
	if v := env.store.Join().View(acct.ID); v.State != store.JoinClaimed || v.Machine != "box" {
		t.Fatalf("session %+v", v)
	}
}

func joinPageText(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	return body(t, resp)
}

func TestJoinPairWritesNoLogLineWithTheCodes(t *testing.T) {
	e := newPairEnv(t)
	var logs bytes.Buffer
	prevSlog, prevOut := slog.Default(), log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(prevOut) })
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	e.pairPage(t, b)
	if strings.Contains(logs.String(), pair) || strings.Contains(logs.String(), e.code) {
		t.Fatalf("a code reached a log: %s", logs.String())
	}
}

// publicEnv startet eine zweite Oberfläche auf demselben Store mit öffentlicher URL.
func (e pairEnv) publicEnv(t *testing.T, opts ...Option) pairEnv {
	t.Helper()
	var h http.Handler
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(hs.Close)
	h = New(e.st, append([]Option{WithPublicURL(hs.URL)}, opts...)...)
	out := e
	out.hs, out.srv = hs, hs.URL
	return out
}

// N5: hinter einer öffentlichen URL ohne benannte Proxys wäre "Same network" immer "yes".
func TestJoinSameNetworkLineIsOmittedWithPublicURLAndNoTrustedProxies(t *testing.T) {
	e := newPairEnv(t).publicEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	text := e.pairPage(t, b)
	if strings.Contains(text, "Same network") || strings.Contains(text, "different network") || !strings.Contains(text, "wants to connect") {
		t.Fatalf("page: %s", text)
	}
}

func TestJoinSameNetworkLineStaysWithPublicURLAndTrustedProxies(t *testing.T) {
	set, err := proxytrust.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	e := newPairEnv(t).publicEnv(t, WithTrustedProxies(set))
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	if text := e.pairPage(t, b); !strings.Contains(text, "Same network as this browser") {
		t.Fatalf("page: %s", text)
	}
}

// N3: im Rückfall steht bei fremdem Netz eine Warnung, und das Feld sagt, woher der Code kommt.
func TestJoinCodeFallbackWarnsOnAnotherNetworkAndNamesTheTerminal(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	if _, err := e.st.Join().Claim(store.JoinClaimRequest{Addr: "203.0.113.5", Pair: pair, Machine: "box"}); err != nil {
		t.Fatal(err)
	}
	text := e.pairPage(t, b)
	for _, want := range []string{"Same network as this browser: <strong>no</strong>", "This is a different network.", "Your terminal shows a 4-character code"} {
		if !strings.Contains(text, want) {
			t.Errorf("page lacks %q: %s", want, text)
		}
	}
}

// N2: mit Secure heißt der Cookie __Host-gt_join (Path=/, ohne Domain).
func TestJoinCookieUsesTheHostPrefixWhereSecureApplies(t *testing.T) {
	e := newPairEnv(t)
	h := New(e.st, WithPublicURL("https://gt.example.test"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://gt.example.test/join/"+e.code, nil)
	h.ServeHTTP(rec, req)
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		cookie = c
	}
	if cookie == nil || cookie.Name != "__Host-gt_join" || !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly {
		t.Fatalf("cookie %+v", cookie)
	}
	// Dasselbe Cookie bringt dieselbe Sitzung zurück; der Name ohne Präfix zählt nicht.
	before := e.st.Join().Sessions()
	req2 := httptest.NewRequest("GET", "https://gt.example.test/join/"+e.code, nil)
	req2.AddCookie(cookie)
	h.ServeHTTP(httptest.NewRecorder(), req2)
	req3 := httptest.NewRequest("GET", "https://gt.example.test/join/"+e.code, nil)
	req3.AddCookie(&http.Cookie{Name: "gt_join", Value: cookie.Value})
	h.ServeHTTP(httptest.NewRecorder(), req3)
	if n := e.st.Join().Sessions(); n != before+1 {
		t.Fatalf("sessions %d -> %d: the unprefixed cookie was accepted or the prefixed one ignored", before, n)
	}
}

// N6: Ablehnen im Loopback-Modus sagt dem Installer Bescheid (RFC 6749 4.1.2.1).
func TestJoinDenyRedirectsTheBrowserToTheInstallerWithAccessDenied(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	resp := e.decide(t, b, url.Values{"decision": {"deny"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "http://127.0.0.1:40123/callback?") ||
		!strings.Contains(loc, "error=access_denied") || !strings.Contains(loc, "state=state-12345678") || strings.Contains(loc, "code=") {
		t.Fatalf("deny: %d %q", resp.StatusCode, loc)
	}
}

func TestJoinCompromisedLoopbackPageOffersToStopTheInstaller(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "mine")
	e.st.Join().Claim(loopClaim(pair, "thief", "198.51.100.7"))
	text := e.pairPage(t, b)
	if !strings.Contains(text, `href="http://127.0.0.1:40123/callback?error=access_denied`) || strings.Contains(text, "thief") {
		t.Fatalf("page: %s", text)
	}
}

// R1: Der Cookie lebt so lange wie das Login-Fenster; Claim bei Minute 12 und
// Anmeldung bei Minute 20 behalten dieselbe Sitzung.
func TestJoinCookieOutlivesALateLoginAfterTheClaim(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	resp, _ := e.get(t, b, "/join/"+e.code)
	var maxAge int
	for _, c := range resp.Cookies() {
		if c.Name == "gt_join" {
			maxAge = c.MaxAge
		}
	}
	if maxAge != int(store.JoinMaxLifetime.Seconds()) {
		t.Fatalf("cookie max-age %d", maxAge)
	}
	pair := pairRE.FindString(joinPageText(t, b, e.srv+"/join/"+e.code))
	start := e.clock.t
	e.clock.t = start.Add(12 * time.Minute)
	e.claimLoop(t, pair, "late-box")
	e.clock.t = start.Add(20 * time.Minute)
	r := sameOriginPostForm(t, b, e.srv+"/ui/login/code", url.Values{"code": {e.code}, "name": {"lena"}, "join": {"1"}})
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/join/pair" {
		t.Fatalf("sign-in: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if text := e.pairPage(t, b); !strings.Contains(text, "late-box") || !strings.Contains(text, "wants to connect") || e.st.Join().Sessions() != 1 {
		t.Fatalf("binding lost: sessions=%d %s", e.st.Join().Sessions(), text)
	}
}

// R3: Sind alle Plätze belegt, steht statt des Befehls eine Zeile.
func TestJoinPageSaysSignInFirstWhenNoSlotIsFree(t *testing.T) {
	e := newPairEnv(t)
	for i := 0; i < 10; i++ {
		b := browser(t)
		pair := e.pairOf(t, b, e.code)
		if _, err := e.st.Join().Claim(loopClaim(pair, "m", "10."+string(rune('0'+i))+".0.1")); err != nil {
			t.Fatal(err)
		}
	}
	_, text := e.get(t, browser(t), "/join/"+e.code)
	if strings.Contains(text, "| sh -s -- --pair") || !strings.Contains(text, "Sign in first, then connect this machine.") {
		t.Fatalf("page: %s", text)
	}
}
