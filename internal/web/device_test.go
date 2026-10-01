package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func startDevice(t *testing.T, st *store.Store, machine string) store.DeviceStart {
	t.Helper()
	s, err := st.Device().Start("203.0.113.9", machine, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDevicePageNeedsLoginAndPostNeedsCSRF(t *testing.T) {
	srv, st, token := testWeb(t)
	start := startDevice(t, st, "laptop")
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(srv.URL + "/ui/device")
	if err != nil || resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("anonymous GET: %v %v", resp, err)
	}
	resp = sameOriginPostForm(t, anon, srv.URL+"/ui/device/decide", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous POST: %d", resp.StatusCode)
	}

	client := login(t, srv, token)
	for name, form := range map[string]url.Values{
		"missing":  {"user_code": {start.UserCode}, "decision": {"approve"}},
		"wrong":    {"user_code": {start.UserCode}, "decision": {"approve"}, "csrf_token": {"nope"}},
		"lookup":   {"user_code": {start.UserCode}},
		"lookup-x": {"user_code": {start.UserCode}, "csrf_token": {"nope"}},
	} {
		target := srv.URL + "/ui/device/decide"
		if strings.HasPrefix(name, "lookup") {
			target = srv.URL + "/ui/device"
		}
		resp := sameOriginPostForm(t, client, target, form)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status %d", name, resp.StatusCode)
		}
	}
	// Richtiges Token, aber fremder Origin.
	csrf := renderedCSRFToken(t, client, srv.URL+"/ui/device")
	req, _ := http.NewRequest("POST", srv.URL+"/ui/device/decide", strings.NewReader(url.Values{
		"user_code": {start.UserCode}, "decision": {"approve"}, "csrf_token": {csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %v %v", resp, err)
	}
	// Nichts davon hat den Ablauf entschieden.
	if _, err := st.Device().Lookup(start.UserCode, "person:1"); err != nil {
		t.Fatalf("flow was decided without a valid request: %v", err)
	}
}

func TestDeviceApprovalInBrowserIssuesBoundToken(t *testing.T) {
	srv, st, _ := testWeb(t)
	clock := &struct{ t time.Time }{time.Now()}
	st.Device().SetClock(func() time.Time { return clock.t })
	start := startDevice(t, st, "laptop")
	client := loginInteractive(t, srv, st, "alice")

	// Die Eingabeseite füllt den Code vor, ohne zu bestätigen.
	resp, err := client.Get(srv.URL + "/ui/device?user_code=" + url.QueryEscape(store.FormatUserCode(start.UserCode)))
	if err != nil {
		t.Fatal(err)
	}
	if page := body(t, resp); !strings.Contains(page, store.FormatUserCode(start.UserCode)) {
		t.Fatalf("code not prefilled: %s", page)
	}
	csrf := renderedCSRFToken(t, client, srv.URL+"/ui/device")
	resp = sameOriginPostForm(t, client, srv.URL+"/ui/device", url.Values{"user_code": {strings.ToLower(start.UserCode)}, "csrf_token": {csrf}})
	page := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(page, "laptop") || !strings.Contains(page, "203.0.113.9") {
		t.Fatalf("check page: %d %s", resp.StatusCode, page)
	}
	resp = sameOriginPostForm(t, client, srv.URL+"/ui/device/decide", url.Values{"user_code": {start.UserCode}, "decision": {"approve"}, "csrf_token": {csrf}})
	if resp.StatusCode != 200 || !strings.Contains(body(t, resp), "Device approved") {
		t.Fatalf("decide: %d", resp.StatusCode)
	}
	clock.t = clock.t.Add(time.Minute)
	approval, _, err := st.Device().Poll(start.DeviceCode)
	if err != nil || approval.AccountID != "person:1" || approval.Machine != "laptop" {
		t.Fatalf("poll: %+v %v", approval, err)
	}
}

func TestDeviceWrongCodesLockTheSession(t *testing.T) {
	srv, st, _ := testWeb(t)
	start := startDevice(t, st, "laptop")
	client := loginInteractive(t, srv, st, "alice")
	csrf := renderedCSRFToken(t, client, srv.URL+"/ui/device")
	for i := 0; i < 5; i++ {
		resp := sameOriginPostForm(t, client, srv.URL+"/ui/device", url.Values{"user_code": {"BBBB-BBBB"}, "csrf_token": {csrf}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("wrong code %d: %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/device", url.Values{"user_code": {start.UserCode}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after lockout: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeviceFormBodyIsLimited(t *testing.T) {
	srv, st, _ := testWeb(t)
	client := loginInteractive(t, srv, st, "alice")
	csrf := renderedCSRFToken(t, client, srv.URL+"/ui/device")
	resp := sameOriginPostForm(t, client, srv.URL+"/ui/device", url.Values{"user_code": {strings.Repeat("A", 100000)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("oversized form: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTokenPageListsOwnTokensAndRevocationIsImmediate(t *testing.T) {
	srv, st, aliceToken := testWeb(t)
	if _, err := st.DB().Exec(`UPDATE persons SET is_admin=1 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	bobLegacy, _ := st.AddPerson("bob")
	bob, _ := st.AccountByName("bob")
	deviceToken, info, err := st.CreateDeviceToken(bob.ID, "bob-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateToken("bob", store.TokenSpec{Label: "ci", Machine: "runner", ExpiresIn: time.Hour}); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(server.New(st))
	t.Cleanup(api.Close)
	whoami := func(tok string) int {
		req, _ := http.NewRequest("GET", api.URL+"/api/whoami", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	bobClient := login(t, srv, bobLegacy)
	resp, _ := bobClient.Get(srv.URL + "/ui/account/tokens")
	page := body(t, resp)
	for _, want := range []string{"legacy", "device", "manual", "bob-laptop", "runner", "never"} {
		if !strings.Contains(page, want) {
			t.Errorf("token page lacks %q", want)
		}
	}
	if strings.Contains(page, "alice") {
		t.Error("member sees another account")
	}

	// Widerruf wirkt sofort.
	if whoami(deviceToken) != 200 {
		t.Fatal("device token should work before revocation")
	}
	csrf := renderedCSRFToken(t, bobClient, srv.URL+"/ui/account/tokens")
	resp = sameOriginPostForm(t, bobClient, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {strconv.FormatInt(info.ID, 10)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if whoami(deviceToken) != 401 {
		t.Fatal("revoked device token still works")
	}
	// Ohne CSRF-Token kein Widerruf.
	resp = sameOriginPostForm(t, bobClient, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {"1"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoke without csrf: %d", resp.StatusCode)
	}

	// Alice (Admin, erste Person) sieht alle, Bob sieht Alices Token nicht.
	aliceClient := loginInteractive(t, srv, st, "alice")
	resp, _ = aliceClient.Get(srv.URL + "/ui/account/tokens")
	adminPage := body(t, resp)
	if !strings.Contains(adminPage, "bob-laptop") || !strings.Contains(adminPage, "alice") || !strings.Contains(adminPage, "revoked") {
		t.Fatalf("admin page incomplete: %s", adminPage)
	}
	// Bob darf Alices Token nicht widerrufen (404, nichts passiert).
	aliceTokens, _ := st.ListTokens("alice")
	csrf = renderedCSRFToken(t, bobClient, srv.URL+"/ui/account/tokens")
	resp = sameOriginPostForm(t, bobClient, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {strconv.FormatInt(aliceTokens[0].ID, 10)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusNotFound || whoami(aliceToken) != 200 {
		t.Fatalf("cross-account revoke: %d", resp.StatusCode)
	}
	// Der Admin darf ein fremdes Token widerrufen.
	bobTokens, _ := st.ListTokens("bob")
	csrf = renderedCSRFToken(t, aliceClient, srv.URL+"/ui/account/tokens")
	var manualID int64
	for _, tk := range bobTokens {
		if tk.Label == "ci" {
			manualID = tk.ID
		}
	}
	resp = sameOriginPostForm(t, aliceClient, srv.URL+"/ui/account/tokens/revoke", url.Values{"token_id": {strconv.FormatInt(manualID, 10)}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin revoke: %d", resp.StatusCode)
	}
	if tk, _ := st.TokenByID(manualID); tk.RevokedAt == "" {
		t.Fatal("admin revoke did not take effect")
	}
}

func TestRevokingTheTokenOfABrowserSessionEndsTheSession(t *testing.T) {
	srv, st, token := testWeb(t)
	client := login(t, srv, token)
	tokens, _ := st.ListTokens("alice")
	if err := st.RevokeToken(tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ := noFollow.Get(srv.URL + "/ui/account/tokens")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ui/login" {
		t.Fatalf("revoked session: %d", resp.StatusCode)
	}
}
