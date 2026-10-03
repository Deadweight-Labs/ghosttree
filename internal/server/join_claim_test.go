package server

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

var testVerifier = strings.Repeat("v", 43)

func testChallenge() string {
	sum := sha256.Sum256([]byte(testVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func claimBody(pair, machine string) map[string]any {
	return map[string]any{"pair": pair, "machine": machine}
}

func loopbackBody(pair, machine string) map[string]any {
	b := claimBody(pair, machine)
	b["code_challenge"], b["code_challenge_method"], b["loopback_port"], b["state"] = testChallenge(), "S256", 40123, "state-12345678"
	return b
}

func claimWith(t *testing.T, srv *httptest.Server, forwardedFor string, body map[string]any) (int, map[string]any) {
	t.Helper()
	return postJSON(t, srv.URL+"/api/join/claim", forwardedFor, body)
}

func joinClockFixture(t *testing.T) (*httptest.Server, *store.Store, *time.Time) {
	t.Helper()
	srv, st := deviceFixture(t)
	now := time.Now()
	clock := &now
	st.Device().SetClock(func() time.Time { return *clock })
	st.Join().SetClock(func() time.Time { return *clock })
	return srv, st, clock
}

func boundSession(t *testing.T, st *store.Store, account string) (pair, nonce string) {
	t.Helper()
	pair, err := st.Join().Create(account)
	if err != nil {
		t.Fatal(err)
	}
	return pair, ""
}

func nonceOf(st *store.Store, account string) string { return st.Join().View(account).Nonce }

func pollToken(t *testing.T, srv *httptest.Server, clock *time.Time, deviceCode string) (int, map[string]any) {
	t.Helper()
	*clock = clock.Add(time.Minute)
	return postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
}

func callbackCode(t *testing.T, redirect string) string {
	t.Helper()
	i := strings.Index(redirect, "code=")
	if i < 0 {
		t.Fatalf("redirect %q", redirect)
	}
	return strings.SplitN(redirect[i+5:], "&", 2)[0]
}

func TestJoinLoopbackFlowIssuesOneTokenForTheBoundAccount(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	code, out := claimWith(t, srv, "", loopbackBody(pair, "anna-laptop"))
	if code != 200 || out["mode"] != "loopback" || out["token_endpoint"] != "/api/join/token" || !strings.HasSuffix(out["verification_uri"].(string), "/join/pair") {
		t.Fatalf("claim: %d %v", code, out)
	}
	for _, k := range []string{"device_code", "confirm_code", "user_code"} {
		if _, ok := out[k]; ok {
			t.Fatalf("loopback claim hands out %q", k)
		}
	}
	dec, err := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ac := callbackCode(t, dec.Redirect)
	// Falscher Verifier: kein Token, und der Code ist verbraucht.
	if code, body := postJSON(t, srv.URL+"/api/join/token", "", map[string]string{"code": ac, "code_verifier": strings.Repeat("x", 43)}); code != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", code, body)
	}
	if code, _ := postJSON(t, srv.URL+"/api/join/token", "", map[string]string{"code": ac, "code_verifier": testVerifier}); code != 400 {
		t.Fatalf("code reusable after a failed try: %d", code)
	}
}

func TestJoinLoopbackExchangeSuccessAndReuse(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	claimWith(t, srv, "", loopbackBody(pair, "anna-laptop"))
	dec, _ := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), "", nil)
	ac := callbackCode(t, dec.Redirect)
	code, body := postJSON(t, srv.URL+"/api/join/token", "", map[string]string{"code": ac, "code_verifier": testVerifier})
	tok, _ := body["access_token"].(string)
	if code != 200 || tok == "" || body["machine"] != "anna-laptop" {
		t.Fatalf("exchange: %d %v", code, body)
	}
	if p, ok := st.AuthenticatePrincipal(tok); !ok || p.ID != "person:2" {
		t.Fatalf("token belongs to %+v", p)
	}
	if v := st.Join().View("person:2"); v.State != store.JoinConnected {
		t.Fatalf("state %q", v.State)
	}
	if code, _ := postJSON(t, srv.URL+"/api/join/token", "", map[string]string{"code": ac, "code_verifier": testVerifier}); code != 400 {
		t.Fatalf("reuse: %d", code)
	}
}

func TestJoinLoopbackThiefWhoClaimedFirstGetsNoTokenEvenIfTheVictimApproves(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	// Der Dieb claimt zuerst, mit seiner eigenen Challenge und seinem Gerätenamen.
	thief := loopbackBody(pair, "anna-laptop")
	thief["code_challenge"] = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if code, _ := claimWith(t, srv, "6.6.6.6", thief); code != 200 {
		t.Fatalf("thief claim: %d", code)
	}
	// Das Opfer gibt frei; der Code geht an den Loopback des Browsers.
	dec, _ := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), "", nil)
	if !strings.HasPrefix(dec.Redirect, "http://127.0.0.1:40123/callback?") {
		t.Fatalf("redirect %q", dec.Redirect)
	}
	// Der Dieb kennt den Code nicht. Das Opfer-Gerät kann ihn nicht lösen, weil es eine andere Challenge hat.
	if code, _ := postJSON(t, srv.URL+"/api/join/token", "6.6.6.6", map[string]string{"code": strings.Repeat("ab", 32), "code_verifier": testVerifier}); code != 400 {
		t.Fatalf("guessed code: %d", code)
	}
	if code, _ := postJSON(t, srv.URL+"/api/join/token", "", map[string]string{"code": callbackCode(t, dec.Redirect), "code_verifier": testVerifier}); code != 400 {
		t.Fatalf("a foreign verifier worked: %d", code)
	}
}

func TestJoinCodeFallbackNeedsTheConfirmationBeforeTheDeviceFlowDeliversAToken(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	code, out := claimWith(t, srv, "", claimBody(pair, "anna-laptop"))
	if code != 200 || out["mode"] != "code" || out["token_endpoint"] != "/api/auth/device/token" {
		t.Fatalf("claim: %d %v", code, out)
	}
	dc, confirm := out["device_code"].(string), out["confirm_code"].(string)
	if len(dc) != 64 || len(confirm) != 4 || out["interval"].(float64) != 5 {
		t.Fatalf("claim body %v", out)
	}
	if _, ok := out["user_code"]; ok {
		t.Fatal("user_code leaks")
	}
	if code, body := pollToken(t, srv, clock, dc); code != 400 || body["error"] != "authorization_pending" {
		t.Fatalf("pending: %d %v", code, body)
	}
	if _, err := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), "WXWX", nil); err == nil {
		t.Fatal("approved with a wrong confirmation")
	}
	if code, body := pollToken(t, srv, clock, dc); code != 400 || body["error"] != "authorization_pending" {
		t.Fatalf("still pending: %d %v", code, body)
	}
	if _, err := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), confirm, nil); err != nil {
		t.Fatal(err)
	}
	code, body := pollToken(t, srv, clock, dc)
	tok, _ := body["access_token"].(string)
	if code != 200 || tok == "" {
		t.Fatalf("token: %d %v", code, body)
	}
	if p, ok := st.AuthenticatePrincipal(tok); !ok || p.ID != "person:2" {
		t.Fatalf("token belongs to %+v", p)
	}
	if v := st.Join().View("person:2"); v.State != store.JoinConnected {
		t.Fatalf("state %q", v.State)
	}
	if code, body := pollToken(t, srv, clock, dc); code != 400 || body["error"] != "expired_token" {
		t.Fatalf("second poll: %d %v", code, body)
	}
}

func TestJoinCodeFallbackSecondClaimDiscardsTheSession(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	_, first := claimWith(t, srv, "10.0.0.1", claimBody(pair, "mine"))
	if code, body := claimWith(t, srv, "10.0.0.2", claimBody(pair, "thief")); code != 400 || body["error"] != "invalid_pair" {
		t.Fatalf("second claim: %d %v", code, body)
	}
	if code, body := pollToken(t, srv, clock, first["device_code"].(string)); code != 400 || body["error"] != "expired_token" {
		t.Fatalf("first device flow lives: %d %v", code, body)
	}
}

func TestJoinCodeFlowIsInvisibleToTheDeviceApprovalPage(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	_, out := claimWith(t, srv, "", claimBody(pair, "box"))
	for _, c := range []string{pair, out["confirm_code"].(string), out["confirm_code"].(string) + out["confirm_code"].(string)} {
		if err := st.Device().Decide(c, "person:2", true); err == nil {
			t.Fatalf("/ui/device decided a join flow with %q", c)
		}
	}
}

func TestJoinClaimRejectsBrokenLoopbackFields(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	for name, mod := range map[string]func(map[string]any){
		"port 80":      func(b map[string]any) { b["loopback_port"] = 80 },
		"no state":     func(b map[string]any) { delete(b, "state") },
		"bad method":   func(b map[string]any) { b["code_challenge_method"] = "plain" },
		"no method":    func(b map[string]any) { delete(b, "code_challenge_method") },
		"empty method": func(b map[string]any) { b["code_challenge_method"] = "" },
		"foreign host": func(b map[string]any) { b["loopback_host"] = "evil.example" },
		"short chall":  func(b map[string]any) { b["code_challenge"] = "abc" },
	} {
		b := loopbackBody(pair, "m")
		mod(b)
		if code, body := claimWith(t, srv, "10.5.0.1", b); code != 400 || body["error"] != "invalid_request" {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	if code, _ := claimWith(t, srv, "10.5.0.2", loopbackBody(pair, "m")); code != 200 {
		t.Fatalf("claim after bad bodies: %d", code)
	}
}

// Pitfall #2447: was ein Fremder sieht, hängt nicht davon ab, ob der Code gültig war.
func TestJoinClaimAnswersAreIdenticalForEveryUselessCode(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	for _, n := range []string{"anna", "ben", "cora", "dan"} {
		st.AddPerson(n)
	}
	expired, _ := st.Join().Create("person:2")
	claimed, _ := st.Join().Create("person:3")
	denied, _ := st.Join().Create("person:4")
	claimWith(t, srv, "10.9.9.9", claimBody(claimed, "m"))
	claimWith(t, srv, "10.9.9.8", claimBody(denied, "m"))
	st.Join().Decide("person:4", false, nonceOf(st, "person:4"), "", nil)
	live, _ := st.Join().Create("person:5")
	_ = live
	cases := map[string]string{"unknown": "BCDF-GHJK", "live claimed": claimed, "denied": denied, "lowercase unknown": "bcdf-ghjk",
		"too short": "BCDF", "garbage": "!!!!-????", "empty": "", "invisible": "BCDF​-GHJK"}
	run := func(cases map[string]string) map[string]string {
		out := map[string]string{}
		names := make([]string, 0, len(cases))
		for n := range cases {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, name := range names {
			raw := fmt.Sprintf(`{"pair":%q,"machine":"m"}`, cases[name])
			req, _ := http.NewRequest("POST", srv.URL+"/api/join/claim", strings.NewReader(raw))
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.1.%d.%d", len(cases), i))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Header.Del("Date")
			resp.Header.Del("X-Request-Id")
			out[name] = fmt.Sprintf("%d %v %s", resp.StatusCode, resp.Header, b)
			if resp.StatusCode != 400 {
				t.Fatalf("%s: %d", name, resp.StatusCode)
			}
		}
		return out
	}
	snaps := run(cases)
	*clock = clock.Add(store.JoinSessionTTL + time.Minute)
	snaps2 := run(map[string]string{"expired": expired})
	ref := snaps["unknown"]
	for name, snap := range snaps {
		if snap != ref {
			t.Errorf("%s differs from unknown:\n%s\n%s", name, ref, snap)
		}
	}
	if snaps2["expired"] != ref {
		t.Errorf("expired differs:\n%s\n%s", ref, snaps2["expired"])
	}
	for name, snap := range snaps {
		for _, secret := range []string{expired, claimed, denied} {
			if strings.Contains(snap, secret) {
				t.Errorf("%s echoes a code", name)
			}
		}
	}
}

func TestJoinExchangeAnswersAreIdenticalForEveryUselessGrant(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	claimWith(t, srv, "", loopbackBody(pair, "m"))
	dec, _ := st.Join().Decide("person:2", true, nonceOf(st, "person:2"), "", nil)
	live := callbackCode(t, dec.Redirect)
	var ref string
	for i, c := range []struct{ code, verifier string }{
		{strings.Repeat("ab", 32), testVerifier}, {live, strings.Repeat("x", 43)}, {"", ""}, {"zz", "short"},
	} {
		raw := fmt.Sprintf(`{"code":%q,"code_verifier":%q}`, c.code, c.verifier)
		req, _ := http.NewRequest("POST", srv.URL+"/api/join/token", strings.NewReader(raw))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.7.0.%d", i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Header.Del("Date")
		resp.Header.Del("X-Request-Id")
		snap := fmt.Sprintf("%d %v %s", resp.StatusCode, resp.Header, b)
		if ref == "" {
			ref = snap
		} else if snap != ref {
			t.Errorf("case %d differs:\n%s\n%s", i, ref, snap)
		}
	}
}

func TestJoinClaimRateLimitIsPerNetworkAndIdenticalForRightAndWrongCodes(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	for i := 0; i < 8; i++ {
		if code, _ := claimWith(t, srv, "10.2.0.1", claimBody("BCDF-GHJK", "m")); code != 400 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	codeRight, bodyRight := claimWith(t, srv, "10.2.0.1", claimBody(pair, "m"))
	codeWrong, bodyWrong := claimWith(t, srv, "10.2.0.1", claimBody("BCDF-GHJK", "m"))
	if codeRight != 429 || codeWrong != 429 || fmt.Sprint(bodyRight) != fmt.Sprint(bodyWrong) {
		t.Fatalf("right %d %v / wrong %d %v", codeRight, bodyRight, codeWrong, bodyWrong)
	}
	if code, _ := claimWith(t, srv, "10.2.0.2", claimBody(pair, "m")); code != 200 {
		t.Fatalf("another address: %d", code)
	}
}

func TestJoinClaimIPv6AddressesOfOneSlash64ShareTheLimit(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	for i := 0; i < 8; i++ {
		claimWith(t, srv, fmt.Sprintf("2001:db8:5:6::%d", i+1), claimBody("BCDF-GHJK", "m"))
	}
	if code, _ := claimWith(t, srv, "2001:db8:5:6::ffff", claimBody(pair, "m")); code != 429 {
		t.Fatalf("same /64: %d", code)
	}
}

func TestJoinClaimBusyAddressLooksTheSameForRightAndWrongCodes(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	for i := 0; i < 5; i++ {
		if code, _ := postJSON(t, srv.URL+"/api/auth/device", "10.3.0.1", map[string]string{"machine": "x"}); code != 200 {
			t.Fatal(code)
		}
	}
	pair, _ := boundSession(t, st, "person:2")
	c1, b1 := claimWith(t, srv, "10.3.0.1", claimBody(pair, "m"))
	c2, b2 := claimWith(t, srv, "10.3.0.1", claimBody("BCDF-GHJK", "m"))
	if c1 != 429 || c2 != 429 || fmt.Sprint(b1) != fmt.Sprint(b2) {
		t.Fatalf("%d %v / %d %v", c1, b1, c2, b2)
	}
}

func TestJoinClaimValidatesTheBodyWithoutTouchingTheCode(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	for _, machine := range []string{"", "bad\nname", strings.Repeat("x", 129)} {
		if code, body := claimWith(t, srv, "10.4.0.1", claimBody(pair, machine)); code != 400 || body["error"] != "invalid_request" {
			t.Fatalf("machine %q: %d %v", machine, code, body)
		}
	}
	if code, _ := claimWith(t, srv, "10.4.0.1", claimBody(pair, strings.Repeat("a", 100000))); code != 413 && code != 400 {
		t.Fatalf("big body: %d", code)
	}
	if code, _ := claimWith(t, srv, "10.4.0.2", claimBody(pair, "ok")); code != 200 {
		t.Fatalf("claim after bad bodies: %d", code)
	}
	resp, _ := http.Get(srv.URL + "/api/join/claim")
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
}

func TestJoinClaimAfterRestartIsTheNeutralAnswer(t *testing.T) {
	_, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	again, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	srv := httptest.NewServer(New(again))
	defer srv.Close()
	if code, body := claimWith(t, srv, "", claimBody(pair, "m")); code != 400 || body["error"] != "invalid_pair" {
		t.Fatalf("%d %v", code, body)
	}
}

// N3: der Maschinenname im Join-Claim ist ein Kennzeichen, kein Text.
func TestJoinClaimMachineNameIsRestrictedToHostnameCharacters(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	for _, name := range []string{"Approve this now", "a/b", "caf\u00e9", "x:y", strings.Repeat("x", 65), "a b"} {
		if code, body := claimWith(t, srv, "10.8.0.1", claimBody(pair, name)); code != 400 || body["error"] != "invalid_request" {
			t.Errorf("%q: %d %v", name, code, body)
		}
	}
	if code, body := claimWith(t, srv, "10.8.0.2", loopbackBody(pair, strings.Repeat("a", 63)+".")); code != 200 {
		t.Fatalf("hostname-like name refused: %d %v", code, body)
	}
}

// Die Claim-Antwort trägt das Wiederaufnahme-Token; nur damit ersetzt derselbe
// Installer seine eigene Anfrage, gleicher Name und gleiches Netz genügen nicht.
func TestJoinClaimReturnsAResumeTokenThatAloneAllowsAReclaim(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := boundSession(t, st, "person:2")
	code, first := claimWith(t, srv, "10.0.0.1", claimBody(pair, "mine"))
	resume, _ := first["resume"].(string)
	if code != 200 || len(resume) != 32 {
		t.Fatalf("claim: %d %v", code, first)
	}
	// Gleiches Netz, gleicher Name, aber ohne Token: zweiter Claim.
	probe := claimBody(pair, "mine")
	if code, body := claimWith(t, srv, "10.0.0.1", probe); code != 400 || body["error"] != "invalid_pair" {
		t.Fatalf("reclaim without the token: %d %v", code, body)
	}
	if v := st.Join().View("person:2"); v.State != store.JoinCompromised {
		t.Fatalf("state %q", v.State)
	}
	// Mit Token (frische Sitzung): erlaubt, und es gibt ein neues.
	pair, _ = boundSession(t, st, "person:2")
	_, first = claimWith(t, srv, "10.0.0.1", claimBody(pair, "mine"))
	again := claimBody(pair, "mine")
	again["resume"] = first["resume"]
	code, second := claimWith(t, srv, "10.0.0.1", again)
	if code != 200 || second["resume"] == "" || second["resume"] == first["resume"] {
		t.Fatalf("reclaim with the token: %d %v", code, second)
	}
	// Zu langes Token ist ein kaputter Körper.
	long := claimBody(pair, "mine")
	long["resume"] = strings.Repeat("a", 65)
	if code, body := claimWith(t, srv, "10.0.0.1", long); code != 400 || body["error"] != "invalid_request" {
		t.Fatalf("long token: %d %v", code, body)
	}
}
