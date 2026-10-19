package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// decideJSON ist die Freigabe, wie join.js sie per fetch schickt.
func (e pairEnv) decideJSON(t *testing.T, c *http.Client, form url.Values) (*http.Response, string) {
	t.Helper()
	_, page := e.get(t, c, "/join/pair")
	form.Set("csrf_token", csrfOn(t, page))
	if m := nonceRE.FindStringSubmatch(page); m != nil {
		form.Set("nonce", m[1])
	}
	req, err := http.NewRequest(http.MethodPost, e.srv+"/join/pair/decide", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", e.srv)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func (e pairEnv) claimedLoopback(t *testing.T) *http.Client {
	t.Helper()
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	e.claimLoop(t, pairRE.FindString(e.pairPage(t, b)), "annas-laptop")
	return b
}

// Die Freigabe per fetch antwortet mit dem Loopback-Ziel statt mit einer
// Weiterleitung; die Seite danach zeigt den Code und navigiert nirgendwohin.
func TestJoinApproveByScriptReturnsTheLoopbackAndTheCodeStaysOnThePage(t *testing.T) {
	e := newPairEnv(t)
	b := e.claimedLoopback(t)
	page := e.pairPage(t, b)
	if !strings.Contains(page, "data-join-popup") {
		t.Fatalf("the approval form is not marked for the second window: %s", page)
	}
	resp, body := e.decideJSON(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("approve: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	var out struct{ Navigate string }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	page = e.pairPage(t, b)
	shown := regexp.MustCompile(`id="join-paste-code"[^>]*>([A-Z0-9-]+)<`).FindStringSubmatch(page)
	if shown == nil || !regexp.MustCompile(`^http://127\.0\.0\.1:40123/callback\?code=`+strings.ReplaceAll(shown[1], "-", "")+`&state=state-12345678$`).MatchString(out.Navigate) {
		t.Fatalf("navigate %q does not carry the code on the page %q", out.Navigate, shown)
	}
	if strings.Contains(page, `http-equiv="refresh" content="0`) {
		t.Fatalf("the page redirects: %s", page)
	}
}

// Eine Ablehnung per fetch braucht kein Fenster: die Antwort nennt kein Ziel.
func TestJoinDenyByScriptNamesNoTarget(t *testing.T) {
	e := newPairEnv(t)
	b := e.claimedLoopback(t)
	resp, body := e.decideJSON(t, b, url.Values{"decision": {"deny"}})
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "127.0.0.1") || !strings.Contains(e.pairPage(t, b), "Request denied") {
		t.Fatalf("deny: %d %s", resp.StatusCode, body)
	}
}

// Fehler kommen weiter als Seite mit Statuscode: join.js schickt dann denselben Post als Formular.
func TestJoinApproveByScriptWithoutConfirmationFailsAsBefore(t *testing.T) {
	e := newPairEnv(t)
	b := e.claimedLoopback(t)
	resp, body := e.decideJSON(t, b, url.Values{"decision": {"approve"}})
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(body, "navigate") {
		t.Fatalf("approve without confirmation: %d %s", resp.StatusCode, body)
	}
}

// Ohne Skript bleibt der Formular-Post: Ablehnung geht per 303 auf den Loopback.
func TestJoinDenyWithoutScriptStillRedirectsToTheLoopback(t *testing.T) {
	e := newPairEnv(t)
	b := e.claimedLoopback(t)
	resp := e.decide(t, b, url.Values{"decision": {"deny"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "http://127.0.0.1:40123/callback?error=access_denied") {
		t.Fatalf("deny: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// Das Skript darf den Tab mit dem Code nie selbst wegschicken; der Loopback läuft nur im zweiten Fenster.
func TestJoinScriptNeverNavigatesTheCodeTab(t *testing.T) {
	src, err := os.ReadFile("static/join.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	if strings.Contains(js, "location.href = go") || strings.Contains(js, "sessionStorage") {
		t.Fatal("join.js navigates the code tab to the loopback")
	}
	for _, want := range []string{`window.open("about:blank"`, "popup.location.href", "popup.close()", "event.submitter"} {
		if !strings.Contains(js, want) {
			t.Errorf("join.js lacks %q", want)
		}
	}
	// window.open muss vor dem ersten await stehen, sonst gilt die Nutzergeste nicht mehr.
	if open, wait := strings.Index(js, "window.open("), strings.Index(js, "await fetch"); open < 0 || wait < 0 || open > wait {
		t.Fatal("window.open must run before the first await")
	}
}

// Nach dem Tausch zeigt die Seite des Konto-Tabs "verbunden", ohne den Code; der
// Zustandsaufruf, den join.js abfragt, verrät den Code nie.
func TestJoinStateFlipsToConnectedAndThePageDropsTheCode(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	claim := loopClaim(pairRE.FindString(e.pairPage(t, b)), "annas-laptop", "127.0.0.1")
	claim.Challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	if _, err := e.st.Join().Claim(claim); err != nil {
		t.Fatal(err)
	}
	e.decideJSON(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}})
	code := regexp.MustCompile(`id="join-paste-code"[^>]*>([A-Z0-9-]+)<`).FindStringSubmatch(e.pairPage(t, b))
	if code == nil {
		t.Fatal("no code on the approved page")
	}
	if _, st := e.get(t, b, "/join/pair/state"); strings.Contains(st, strings.ReplaceAll(code[1], "-", "")) || !strings.Contains(st, `"approved"`) {
		t.Fatalf("state: %s", st)
	}
	grant, err := e.st.Join().Exchange("127.0.0.1", code[1], verifier)
	if err != nil {
		t.Fatal(err)
	}
	e.st.Join().Delivered(grant)
	_, st := e.get(t, b, "/join/pair/state")
	page := e.pairPage(t, b)
	if !strings.Contains(st, `"connected"`) || strings.Contains(page, "join-paste-code") || !strings.Contains(page, "annas-laptop is connected") || !strings.Contains(page, "Continue in your terminal") {
		t.Fatalf("state %s, page %s", st, page)
	}
}
