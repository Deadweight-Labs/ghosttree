package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func deviceFixture(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st))
	t.Cleanup(srv.Close)
	return srv, st
}

func postJSON(t *testing.T, url, forwardedFor string, payload any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestDeviceLoginEndToEnd(t *testing.T) {
	srv, st := deviceFixture(t)
	clock := &struct{ t time.Time }{time.Now()}
	st.Device().SetClock(func() time.Time { return clock.t })

	// Ohne Token erreichbar.
	code, start := postJSON(t, srv.URL+"/api/auth/device", "", map[string]string{"machine": "laptop"})
	if code != 200 {
		t.Fatalf("start: %d %v", code, start)
	}
	deviceCode, userCode := start["device_code"].(string), start["user_code"].(string)
	if !strings.HasSuffix(start["verification_uri"].(string), "/ui/device") || start["interval"].(float64) != 5 || start["expires_in"].(float64) != 600 {
		t.Fatalf("start body: %v", start)
	}

	// Zu früh: slow_down.
	code, body := postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
	if code != 400 || body["error"] != "slow_down" {
		t.Fatalf("early: %d %v", code, body)
	}
	clock.t = clock.t.Add(time.Minute)
	code, body = postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
	if code != 400 || body["error"] != "authorization_pending" {
		t.Fatalf("pending: %d %v", code, body)
	}

	// Falscher Code: kein Token, Konto zählt den Fehlversuch.
	if err := st.Device().Decide("BBBB-BBBB", "person:1", true); err == nil {
		t.Fatal("wrong code accepted")
	}
	if err := st.Device().Decide(userCode, "person:1", true); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(time.Minute)
	code, body = postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
	if code != 200 {
		t.Fatalf("token: %d %v", code, body)
	}
	token := body["access_token"].(string)
	if body["machine"] != "laptop" {
		t.Fatalf("machine: %v", body)
	}

	// Das Token gilt, ist an die Maschine gebunden und die Konto-Sicht stimmt.
	req, _ := http.NewRequest("GET", srv.URL+"/api/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var who map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&who)
	resp.Body.Close()
	if resp.StatusCode != 200 || who["machine"] != "laptop" || who["token_kind"] != "device" || who["label"] != "alice" {
		t.Fatalf("whoami: %d %v", resp.StatusCode, who)
	}

	// Einmalig.
	clock.t = clock.t.Add(time.Minute)
	code, body = postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": deviceCode})
	if code != 400 || body["error"] != "expired_token" {
		t.Fatalf("replay: %d %v", code, body)
	}
}

func TestDeviceLoginExpiryAndDenial(t *testing.T) {
	srv, st := deviceFixture(t)
	clock := &struct{ t time.Time }{time.Now()}
	st.Device().SetClock(func() time.Time { return clock.t })
	_, start := postJSON(t, srv.URL+"/api/auth/device", "", map[string]string{"machine": "laptop"})
	clock.t = clock.t.Add(store.DeviceFlowTTL + time.Second)
	_, body := postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": start["device_code"].(string)})
	if body["error"] != "expired_token" {
		t.Fatalf("expired: %v", body)
	}
	_, start = postJSON(t, srv.URL+"/api/auth/device", "", map[string]string{"machine": "laptop"})
	if err := st.Device().Decide(start["user_code"].(string), "person:1", false); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(time.Minute)
	_, body = postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": start["device_code"].(string)})
	if body["error"] != "access_denied" {
		t.Fatalf("denied: %v", body)
	}
}

func TestDeviceStartIsBoundedAndInputIsLimited(t *testing.T) {
	srv, _ := deviceFixture(t)
	// Ein Absender hält höchstens fünf Abläufe offen; andere sind nicht gesperrt.
	for i := 0; i < 5; i++ {
		if code, _ := postJSON(t, srv.URL+"/api/auth/device", "10.0.0.1", map[string]string{"machine": "m"}); code != 200 {
			t.Fatalf("start %d: %d", i, code)
		}
	}
	if code, _ := postJSON(t, srv.URL+"/api/auth/device", "10.0.0.1", map[string]string{"machine": "m"}); code != 429 {
		t.Fatalf("flooder status = %d, want 429", code)
	}
	for i := 0; i < 20; i++ {
		if code, _ := postJSON(t, srv.URL+"/api/auth/device", fmt.Sprintf("10.0.1.%d", i), map[string]string{"machine": "m"}); code != 200 {
			t.Fatalf("other client %d blocked: %d", i, code)
		}
	}
	// Eine Fälschung des Headers durch einen Nicht-Loopback-Absender zählt nicht;
	// hier zählt er, weil httptest über Loopback kommt. Body- und Namensgrenzen:
	big := map[string]string{"machine": strings.Repeat("a", 5000)}
	if code, _ := postJSON(t, srv.URL+"/api/auth/device", "10.0.2.1", big); code != 413 && code != 400 {
		t.Fatalf("oversized body status = %d", code)
	}
	for _, bad := range []string{"", "  ", strings.Repeat("a", 129), "evil\nname", "x\x00y"} {
		if code, _ := postJSON(t, srv.URL+"/api/auth/device", "10.0.2.2", map[string]string{"machine": bad}); code != 400 {
			t.Fatalf("machine %q status = %d", bad, code)
		}
	}
	// Der Token-Endpunkt begrenzt seinen Körper ebenso.
	if code, _ := postJSON(t, srv.URL+"/api/auth/device/token", "", map[string]string{"device_code": strings.Repeat("z", 100000)}); code != 413 {
		t.Fatalf("oversized poll status = %d", code)
	}
}

func TestOtherAPIRoutesStillNeedAToken(t *testing.T) {
	srv, _ := deviceFixture(t)
	resp, _ := http.Get(srv.URL + "/api/whoami")
	if resp.StatusCode != 401 {
		t.Fatalf("whoami without token = %d", resp.StatusCode)
	}
	// Nur POST auf die beiden Pfade ist offen.
	resp, _ = http.Get(srv.URL + "/api/auth/device")
	if resp.StatusCode == 200 {
		t.Fatal("GET on device start answered 200")
	}
}
