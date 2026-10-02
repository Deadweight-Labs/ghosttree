package web

import (
	"bytes"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var pairRE = regexp.MustCompile(`[BCDFGHJKMNPQRTVWX34679]{4}-[BCDFGHJKMNPQRTVWX34679]{4}`)

// pairWeb: wie joinWeb, dazu ein Konto "anna" mit Einladung und ihre angemeldete Sitzung.
type pairEnv struct {
	srv     string
	hs      *httptest.Server
	annaTok string
	st      *store.Store
	anna    *http.Client
	org     store.Org
	code    string
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
	return pairEnv{hs: srv, annaTok: annaTok, srv: srv.URL, st: st, anna: loginInteractive(t, srv, st, "anna"), org: org,
		code: projectInvite(t, st, org, store.RoleMember), clock: clock}
}

func (e pairEnv) csrf(t *testing.T, c *http.Client) string {
	t.Helper()
	return renderedCSRFToken(t, c, e.srv+"/join/pair")
}

func (e pairEnv) get(t *testing.T, c *http.Client, path string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(e.srv + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body(t, resp)
}

// accept nimmt die Einladung an und gibt den Paarungscode der Seite zurück.
func (e pairEnv) accept(t *testing.T) string {
	t.Helper()
	_, page := e.get(t, e.anna, "/join/"+e.code)
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	resp := sameOriginPostForm(t, e.anna, e.srv+"/join/"+e.code+"/accept", url.Values{"csrf_token": {csrf}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("accept: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, pair := e.get(t, e.anna, "/join/pair")
	return pairRE.FindString(pair)
}

func (e pairEnv) claim(t *testing.T, pair, machine string) store.JoinClaim {
	t.Helper()
	c, err := e.st.Join().Claim("203.0.113.5", pair, machine, "203.0.113.5")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return c
}

func (e pairEnv) decide(t *testing.T, c *http.Client, decision string, confirm string) *http.Response {
	t.Helper()
	form := url.Values{"csrf_token": {e.csrf(t, c)}, "decision": {decision}}
	if confirm != "" {
		form.Set("confirm_account", confirm)
	}
	return sameOriginPostForm(t, c, e.srv+"/join/pair/decide", form)
}

// poll holt das Token ab wie /api/auth/device/token, nach dem Poll-Intervall.
func (e pairEnv) poll(t *testing.T, deviceCode string) (store.DeviceApproval, error) {
	t.Helper()
	e.clock.t = e.clock.t.Add(time.Minute)
	a, _, err := e.st.Device().Poll(deviceCode)
	return a, err
}

type testClock struct{ t time.Time }

func (e pairEnv) page(t *testing.T, c *http.Client) string {
	t.Helper()
	resp, text := e.get(t, c, "/join/pair")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pair page: %d %s", resp.StatusCode, text)
	}
	return text
}

func TestJoinAcceptEndsOnThePairingPageWithTheCommandButNeverTheInvitationCode(t *testing.T) {
	e := newPairEnv(t)
	pair := e.accept(t)
	if pair == "" {
		t.Fatal("no pairing code on the page")
	}
	resp, text := e.get(t, e.anna, "/join/pair")
	if !strings.Contains(text, "--pair "+pair) || !strings.Contains(text, "| sh -s --") {
		t.Fatalf("no command: %s", text)
	}
	if strings.Contains(text, e.code) {
		t.Fatal("the invitation code is on the pairing page")
	}
	h := resp.Header
	if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "strict-origin" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("headers %v", h)
	}
	if loc := resp.Request.URL.String(); strings.Contains(loc, pair) {
		t.Fatal("the code is in the URL")
	}
	// Die Einladung ist verbraucht, die Sitzung gehört anna.
	if _, err := e.st.PreviewInvitation(e.code, true); err == nil {
		t.Fatal("invitation still valid")
	}
}

// Reihenfolge 1: Seite zuerst offen, das Gerät meldet sich danach.
func TestJoinPairBrowserFirstThenInstaller(t *testing.T) {
	e := newPairEnv(t)
	pair := e.accept(t)
	if text := e.page(t, e.anna); strings.Contains(text, "wants to connect") || strings.Contains(text, `value="approve"`) {
		t.Fatalf("approval offered with no device: %s", text)
	}
	// Ohne Gerät gibt es nichts freizugeben.
	if resp := e.decide(t, e.anna, "approve", "anna"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("early approve: %d", resp.StatusCode)
	}
	claim := e.claim(t, strings.ToLower(pair), "anna-laptop")
	text := e.page(t, e.anna)
	for _, want := range []string{"anna-laptop", "wants to connect", pair, `value="approve"`, `value="deny"`, "203.0.113.5"} {
		if !strings.Contains(text, want) {
			t.Errorf("approval page lacks %q: %s", want, text)
		}
	}
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatalf("before approval: %v", err)
	}
	resp := e.decide(t, e.anna, "approve", "anna")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("approve: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	appr, err := e.poll(t, claim.DeviceCode)
	if err != nil || appr.AccountID != "person:2" || appr.Machine != "anna-laptop" {
		t.Fatalf("poll: %+v %v", appr, err)
	}
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDeviceUnknown) {
		t.Fatalf("second poll: %v", err)
	}
	if text := e.page(t, e.anna); !strings.Contains(text, "Connected") {
		t.Fatalf("final page: %s", text)
	}
}

// Reihenfolge 2: das Gerät hat sich gemeldet, bevor die Seite je neu geladen wurde.
func TestJoinPairInstallerFirstThenBrowser(t *testing.T) {
	e := newPairEnv(t)
	pair := e.accept(t)
	claim := e.claim(t, pair, "box")
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatal(err)
	}
	// Erst jetzt öffnet die Seite wieder: sie zeigt sofort die Freigabe.
	if text := e.page(t, e.anna); !strings.Contains(text, "box") || !strings.Contains(text, `value="approve"`) {
		t.Fatalf("page: %s", text)
	}
	resp := e.decide(t, e.anna, "approve", "anna")
	resp.Body.Close()
	if appr, err := e.poll(t, claim.DeviceCode); err != nil || appr.AccountID != "person:2" {
		t.Fatalf("%+v %v", appr, err)
	}
}

func TestJoinPairDenyGivesNoToken(t *testing.T) {
	e := newPairEnv(t)
	claim := e.claim(t, e.accept(t), "box")
	resp := e.decide(t, e.anna, "deny", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deny: %d", resp.StatusCode)
	}
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDeviceDenied) {
		t.Fatalf("poll: %v", err)
	}
	if text := e.page(t, e.anna); !strings.Contains(text, "Denied") {
		t.Fatalf("page: %s", text)
	}
}

func TestJoinPairApprovalNeedsInteractiveSessionCSRFOriginAndConfirmation(t *testing.T) {
	e := newPairEnv(t)
	claim := e.claim(t, e.accept(t), "box")
	target := e.srv + "/join/pair/decide"
	csrf := e.csrf(t, e.anna)
	// Anonym: Login-Seite.
	if resp := sameOriginPostForm(t, anonClient(), target, url.Values{"decision": {"approve"}}); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	// Eingefügtes Token (nicht interaktiv): 403, auch für anna selbst.
	pasted := login(t, e.hs, e.annaTok)
	if resp := sameOriginPostForm(t, pasted, target, url.Values{"csrf_token": {renderedCSRFToken(t, pasted, e.srv+"/ui/orgs")}, "decision": {"approve"}, "confirm_account": {"anna"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pasted token: %d", resp.StatusCode)
	}
	// CSRF fehlt/falsch, fremder Origin.
	for _, token := range []string{"", "nope"} {
		if resp := sameOriginPostForm(t, e.anna, target, url.Values{"csrf_token": {token}, "decision": {"approve"}, "confirm_account": {"anna"}}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("csrf %q: %d", token, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", target, strings.NewReader(url.Values{"csrf_token": {csrf}, "decision": {"approve"}, "confirm_account": {"anna"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := e.anna.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
	// Ohne Kontobestätigung oder mit einem anderen Namen: 400.
	for _, confirm := range []string{"", "ben"} {
		resp := e.decide(t, e.anna, "approve", confirm)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("confirm %q: %d", confirm, resp.StatusCode)
		}
	}
	// Nichts davon hat entschieden.
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatalf("a refused request decided the flow: %v", err)
	}
}

func TestJoinPairAnotherAccountNeitherSeesNorDecidesAnnasSession(t *testing.T) {
	e := newPairEnv(t)
	pair := e.accept(t)
	claim := e.claim(t, pair, "annas-laptop")
	e.st.AddPerson("ben")
	ben := loginInteractive(t, e.hs, e.st, "ben")
	if text := e.page(t, ben); strings.Contains(text, pair) || strings.Contains(text, "annas-laptop") {
		t.Fatalf("ben sees annas session: %s", text)
	}
	resp := e.decide(t, ben, "approve", "ben")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ben approve: %d", resp.StatusCode)
	}
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatalf("ben decided: %v", err)
	}
}

func TestJoinPairConnectThisMachineForASignedInAccount(t *testing.T) {
	srv, st, _, _, _ := joinWeb(t)
	st.AddPerson("anna")
	anna := loginInteractive(t, srv, st, "anna")
	e := pairEnv{hs: srv, srv: srv.URL, st: st, anna: anna, clock: &testClock{t: time.Now()}}
	st.Device().SetClock(func() time.Time { return e.clock.t })
	text := e.page(t, anna)
	if !strings.Contains(text, "Connect this machine") || strings.Contains(text, "Setup was interrupted") {
		t.Fatalf("start page: %s", text)
	}
	target := srv.URL + "/join/pair"
	csrf := e.csrf(t, anna)
	// Ohne Bestätigung des Kontos keine Sitzung.
	resp := sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || st.Join().Open() != 0 {
		t.Fatalf("unconfirmed: %d", resp.StatusCode)
	}
	resp = sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {csrf}, "confirm_account": {"anna"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || st.Join().Open() != 1 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	first := pairRE.FindString(e.page(t, anna))
	// Ein neuer Code ersetzt den alten, ohne neue Bestätigung.
	resp = sameOriginPostForm(t, anna, target, url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	second := pairRE.FindString(e.page(t, anna))
	if resp.StatusCode != http.StatusSeeOther || first == second {
		t.Fatalf("renew: %d %s %s", resp.StatusCode, first, second)
	}
	if _, err := st.Join().Claim("1.1.1.1", first, "m", "1.1.1.1"); err != store.ErrJoinInvalid {
		t.Fatalf("old code: %v", err)
	}
}

func TestJoinPairPageAfterLossOfTheSessionSaysSetupWasInterrupted(t *testing.T) {
	e := newPairEnv(t)
	e.accept(t)
	e.clock.t = e.clock.t.Add(store.JoinSessionTTL + time.Minute) // oder ein Neustart: die Sitzung ist weg
	_, text := e.get(t, e.anna, "/join/pair?w=1")
	if !strings.Contains(text, "Setup was interrupted") || !strings.Contains(text, "Start again") {
		t.Fatalf("page: %s", text)
	}
	if _, text := e.get(t, e.anna, "/join/pair"); strings.Contains(text, "interrupted") {
		t.Fatalf("a first visit looks interrupted: %s", text)
	}
}

func TestJoinPairClaimedPageWarnsAboutASecondDevice(t *testing.T) {
	e := newPairEnv(t)
	pair := e.accept(t)
	e.claim(t, pair, "mine")
	if _, err := e.st.Join().Claim("198.51.100.7", pair, "thief", "198.51.100.7"); err == nil {
		t.Fatal("second claim worked")
	}
	text := e.page(t, e.anna)
	if !strings.Contains(text, "mine") || strings.Contains(text, "thief") || !strings.Contains(text, "Another device tried to use this code") {
		t.Fatalf("page: %s", text)
	}
}

func TestJoinPairApproveRefusesAMachineNameOfAnotherAccount(t *testing.T) {
	e := newPairEnv(t)
	e.st.AddPerson("ben")
	if _, _, err := e.st.CreateDeviceToken("person:3", "shared-name"); err != nil {
		t.Fatal(err)
	}
	claim := e.claim(t, e.accept(t), "shared-name")
	resp := e.decide(t, e.anna, "approve", "anna")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	if _, err := e.poll(t, claim.DeviceCode); !errors.Is(err, store.ErrDevicePending) {
		t.Fatalf("flow was decided: %v", err)
	}
}

func TestJoinSignInThroughTheJoinPageLandsOnThePairingPage(t *testing.T) {
	srv, st, _, org, _ := joinWeb(t)
	code := projectInvite(t, st, org, store.RoleMember)
	_, page := (pairEnv{srv: srv.URL}).get(t, anonClient(), "/join/"+code)
	if !strings.Contains(page, `name="join" value="1"`) {
		t.Fatalf("the join form lacks the marker: %s", page)
	}
	resp := sameOriginPostForm(t, anonClient(), srv.URL+"/ui/login/code", url.Values{"code": {code}, "name": {"philipp"}, "join": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("sign-in: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	acct, _ := st.AccountByName("philipp")
	if v := st.Join().View(acct.ID); v.State != store.JoinWaiting {
		t.Fatalf("session %q", v.State)
	}
	// Ohne Marker bleibt alles wie bisher.
	other := projectInvite(t, st, org, store.RoleMember)
	resp = sameOriginPostForm(t, anonClient(), srv.URL+"/ui/login/code", url.Values{"code": {other}, "name": {"paula"}})
	resp.Body.Close()
	if resp.Header.Get("Location") != "/ui/requests" {
		t.Fatalf("without marker: %s", resp.Header.Get("Location"))
	}
}

func TestJoinPairWritesNoLogLineWithTheCodes(t *testing.T) {
	e := newPairEnv(t)
	var logs bytes.Buffer
	prevSlog, prevOut := slog.Default(), log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	log.SetOutput(&logs)
	t.Cleanup(func() { slog.SetDefault(prevSlog); log.SetOutput(prevOut) })
	pair := e.accept(t)
	e.claim(t, pair, "box")
	e.page(t, e.anna)
	if strings.Contains(logs.String(), pair) || strings.Contains(logs.String(), e.code) {
		t.Fatalf("a code reached a log: %s", logs.String())
	}
}

func TestJoinOIDCSignInThroughTheJoinPageLandsOnThePairingPage(t *testing.T) {
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
	page, _ := anonClient().Get(env.web.URL + "/join/" + code)
	if text := body(t, page); !strings.Contains(text, `name="join" value="1"`) {
		t.Fatalf("oidc join form lacks the marker: %s", text)
	}
	env.idp.subject, env.idp.username, env.idp.email = "sub-philipp", "philipp", "philipp@example.test"
	b := newBrowser(t)
	resp := env.callback(t, b, env.startFlowWith(t, b, url.Values{"code": {code}, "join": {"1"}}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/join/pair" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	acct, err := env.store.AccountByName("philipp")
	if err != nil {
		t.Fatal(err)
	}
	if v := env.store.Join().View(acct.ID); v.State != store.JoinWaiting {
		t.Fatalf("session %q", v.State)
	}
}
