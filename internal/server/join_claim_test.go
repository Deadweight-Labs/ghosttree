package server

import (
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

func claimJSON(t *testing.T, srv *httptest.Server, forwardedFor, pair, machine string) (int, map[string]any) {
	t.Helper()
	return postJSON(t, srv.URL+"/api/join/claim", forwardedFor, map[string]string{"pair": pair, "machine": machine})
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

func pollToken(t *testing.T, srv *httptest.Server, clock *time.Time, deviceCode string) (int, map[string]any) {
	t.Helper()
	*clock = clock.Add(time.Minute)
	return postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
}

func TestJoinClaimNeedsNoTokenAndYieldsOneTokenForTheBoundAccount(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	pair, err := st.Join().Create("person:2")
	if err != nil {
		t.Fatal(err)
	}
	code, out := claimJSON(t, srv, "", pair, "anna-laptop")
	if code != 200 {
		t.Fatalf("claim: %d %v", code, out)
	}
	dc, _ := out["device_code"].(string)
	if len(dc) != 64 || out["interval"].(float64) != 5 || out["expires_in"].(float64) != 600 ||
		out["token_endpoint"] != "/api/auth/device/token" || !strings.HasSuffix(out["verification_uri"].(string), "/join/pair") {
		t.Fatalf("claim body %v", out)
	}
	for k := range out {
		if k == "user_code" || strings.Contains(k, "pair") {
			t.Fatalf("claim body leaks %q", k)
		}
	}
	// Vor der Freigabe: ausstehend.
	if code, body := pollToken(t, srv, clock, dc); code != 400 || body["error"] != "authorization_pending" {
		t.Fatalf("pending: %d %v", code, body)
	}
	if err := st.Join().Decide("person:2", true); err != nil {
		t.Fatal(err)
	}
	code, body := pollToken(t, srv, clock, dc)
	tok, _ := body["access_token"].(string)
	if code != 200 || tok == "" || body["machine"] != "anna-laptop" {
		t.Fatalf("token: %d %v", code, body)
	}
	if p, ok := st.AuthenticatePrincipal(tok); !ok || p.ID != "person:2" {
		t.Fatalf("token belongs to %+v", p)
	}
	// Genau einmal.
	if code, body := pollToken(t, srv, clock, dc); code != 400 || body["error"] != "expired_token" {
		t.Fatalf("second poll: %d %v", code, body)
	}
}

func TestJoinClaimDenyEndsInAccessDenied(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := st.Join().Create("person:2")
	_, out := claimJSON(t, srv, "", pair, "m")
	st.Join().Decide("person:2", false)
	if code, body := pollToken(t, srv, clock, out["device_code"].(string)); code != 400 || body["error"] != "access_denied" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestJoinClaimRefusesASecondDeviceAndNeverHandsOutTwoTokens(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := st.Join().Create("person:2")
	_, first := claimJSON(t, srv, "10.0.0.1", pair, "mine")
	if code, body := claimJSON(t, srv, "10.0.0.2", pair, "thief"); code != 400 || body["error"] != "invalid_pair" {
		t.Fatalf("second claim: %d %v", code, body)
	}
	st.Join().Decide("person:2", true)
	if code, body := pollToken(t, srv, clock, first["device_code"].(string)); code != 200 || body["machine"] != "mine" {
		t.Fatalf("%d %v", code, body)
	}
}

// Pitfall #2447: was ein Fremder sieht, hängt nicht davon ab, ob der Code gültig war.
func TestJoinClaimAnswersAreIdenticalForEveryUselessCode(t *testing.T) {
	srv, st, clock := joinClockFixture(t)
	st.AddPerson("anna")
	st.AddPerson("ben")
	expired, _ := st.Join().Create("person:2")
	consumed, _ := st.Join().Create("person:3")
	claimJSON(t, srv, "10.9.9.9", consumed, "m")
	*clock = clock.Add(store.JoinSessionTTL + time.Minute)
	cases := map[string]string{"unknown": "BCDF-GHJK", "expired": expired, "consumed": consumed, "lowercase unknown": "bcdf-ghjk",
		"too short": "BCDF", "garbage": "!!!!-????", "empty": "", "invisible": "BCDF\u200b-GHJK"}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	var ref, refName string
	for i, name := range names {
		raw := fmt.Sprintf(`{"pair":%q,"machine":"m"}`, cases[name])
		req, _ := http.NewRequest("POST", srv.URL+"/api/join/claim", strings.NewReader(raw))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.1.0.%d", i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Header.Del("Date")
		resp.Header.Del("X-Request-Id")
		snap := fmt.Sprintf("%d %v %s", resp.StatusCode, resp.Header, b)
		if resp.StatusCode != 400 {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
		if ref == "" {
			ref, refName = snap, name
		} else if snap != ref {
			t.Errorf("%s differs from %s:\n%s\n%s", name, refName, ref, snap)
		}
		for _, secret := range []string{cases["expired"], cases["consumed"]} {
			if secret != "" && strings.Contains(snap, secret) {
				t.Errorf("%s echoes a code", name)
			}
		}
	}
}

func TestJoinClaimRateLimitIsPerAddressAndIdenticalForRightAndWrongCodes(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := st.Join().Create("person:2")
	for i := 0; i < 8; i++ {
		if code, _ := claimJSON(t, srv, "10.2.0.1", "BCDF-GHJK", "m"); code != 400 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	codeRight, bodyRight := claimJSON(t, srv, "10.2.0.1", pair, "m")
	codeWrong, bodyWrong := claimJSON(t, srv, "10.2.0.1", "BCDF-GHJK", "m")
	if codeRight != 429 || codeWrong != 429 || fmt.Sprint(bodyRight) != fmt.Sprint(bodyWrong) {
		t.Fatalf("right %d %v / wrong %d %v", codeRight, bodyRight, codeWrong, bodyWrong)
	}
	if code, _ := claimJSON(t, srv, "10.2.0.2", pair, "m"); code != 200 {
		t.Fatalf("another address: %d", code)
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
	pair, _ := st.Join().Create("person:2")
	c1, b1 := claimJSON(t, srv, "10.3.0.1", pair, "m")
	c2, b2 := claimJSON(t, srv, "10.3.0.1", "BCDF-GHJK", "m")
	if c1 != 429 || c2 != 429 || fmt.Sprint(b1) != fmt.Sprint(b2) {
		t.Fatalf("%d %v / %d %v", c1, b1, c2, b2)
	}
}

func TestJoinClaimValidatesTheBodyWithoutTouchingTheCode(t *testing.T) {
	srv, st, _ := joinClockFixture(t)
	st.AddPerson("anna")
	pair, _ := st.Join().Create("person:2")
	for _, machine := range []string{"", "bad\nname", strings.Repeat("x", 129)} {
		if code, body := claimJSON(t, srv, "10.4.0.1", pair, machine); code != 400 || body["error"] != "invalid_request" {
			t.Fatalf("machine %q: %d %v", machine, code, body)
		}
	}
	big := map[string]string{"pair": pair, "machine": strings.Repeat("a", 100000)}
	if code, _ := postJSON(t, srv.URL+"/api/join/claim", "10.4.0.1", big); code != 413 && code != 400 {
		t.Fatalf("big body: %d", code)
	}
	// Die Sitzung ist weiter unbeansprucht.
	if code, _ := claimJSON(t, srv, "10.4.0.2", pair, "ok"); code != 200 {
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
	pair, _ := st.Join().Create("person:2")
	// Neuer Prozess auf derselben Datenbank: die Sitzungen liegen im Speicher.
	again, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	srv := httptest.NewServer(New(again))
	defer srv.Close()
	if code, body := claimJSON(t, srv, "", pair, "m"); code != 400 || body["error"] != "invalid_pair" {
		t.Fatalf("%d %v", code, body)
	}
}
