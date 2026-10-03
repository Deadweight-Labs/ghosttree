package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Paarungsseite führt in Schritten, zeigt in jedem Zustand "New code" und
// schaltet sich über /join/pair/state weiter.
func TestJoinGuidedPairPageStatesAndNewCodeEverywhere(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)

	resp, text := e.get(t, b, "/join/pair")
	for _, want := range []string{"Open a terminal on your computer", "Copy this command and run it", "Confirm here", "Waiting for your computer", "Send this to your computer", `data-join-state="waiting"`, `data-copy="#join-cmd"`, "mailto:?subject=", "| sh -s -- --pair " + pair, "New code"} {
		if !strings.Contains(text, want) {
			t.Errorf("waiting page lacks %q", want)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "connect-src 'self'") || strings.Contains(csp, "unsafe") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %q %q", csp, resp.Header.Get("Cache-Control"))
	}
	if strings.Contains(text, "<script>") || strings.Contains(text, " style=") || strings.Contains(text, "person:") {
		t.Error("inline script or style, or an internal id")
	}
	// Ein Neuladen erzeugt keinen neuen Code.
	if again := e.pairPage(t, b); !strings.Contains(again, pair) || e.st.Join().Sessions() != 1 {
		t.Fatalf("reload changed the code: %d", e.st.Join().Sessions())
	}

	// Code-Weg: großes Feld, sprechende Bestätigung ohne Kennung.
	claim := e.claimCode(t, pair, "annas-laptop")
	text = e.pairPage(t, b)
	for _, want := range []string{"annas-laptop wants to connect", "Code from your terminal", `class="clay join-code"`, "This is me, anna", "Connect annas-laptop", "New code"} {
		if !strings.Contains(text, want) {
			t.Errorf("claimed page lacks %q", want)
		}
	}
	if strings.Contains(text, "person:") || strings.Contains(text, "Yes, I am") {
		t.Error("claimed page shows an internal id or the old confirmation")
	}
	state := func() string {
		_, s := e.get(t, b, "/join/pair/state")
		return s
	}
	if s := state(); !strings.Contains(s, `"state":"claimed"`) {
		t.Fatalf("state %s", s)
	}
	e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "confirm_code": {claim.Confirm}}).Body.Close()
	text = e.pairPage(t, b)
	if !strings.Contains(text, "Connecting annas-laptop") || !strings.Contains(text, "New code") {
		t.Fatalf("approved page: %s", text)
	}
	if err := e.poll(t, claim.DeviceCode); err != nil {
		t.Fatal(err)
	}
	e.st.Join().DeliveredDevice(claim.DeviceCode)
	text = e.pairPage(t, b)
	for _, want := range []string{"annas-laptop is connected", `href="/ui/overview"`, "Go to overview", "New code"} {
		if !strings.Contains(text, want) {
			t.Errorf("connected page lacks %q", want)
		}
	}
	if s := state(); !strings.Contains(s, `"state":"connected"`) {
		t.Fatalf("state %s", s)
	}
}

func TestJoinStateEndpointNeedsAnInteractiveSessionAndIsNeverCached(t *testing.T) {
	e := newPairEnv(t)
	resp, _ := e.get(t, browser(t), "/join/pair/state")
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	b := browser(t)
	e.signInAs(t, b, "anna")
	resp, text := e.get(t, b, "/join/pair/state")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(text, `"state":"none"`) {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, text)
	}
}

// Abgebrochene Anfrage: die Seite sagt es und bietet einen neuen Code an.
func TestJoinLapsedRequestSaysSoAndNeverBlocksTheNextCode(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	pair := e.pairOf(t, b, e.code)
	e.accept(t, b, e.code)
	e.claimLoop(t, pair, "box")
	e.clock.t = e.clock.t.Add(store.JoinClaimTTL + 1)
	text := e.pairPage(t, b)
	if !strings.Contains(text, "That request timed out") || !strings.Contains(text, "New code") || strings.Contains(text, `value="approve"`) {
		t.Fatalf("page: %s", text)
	}
	resp := sameOriginPostForm(t, b, e.srv+"/join/pair", url.Values{"csrf_token": {csrfOn(t, text)}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("new code: %d", resp.StatusCode)
	}
	if next := e.pairPage(t, b); !strings.Contains(next, `data-join-state="waiting"`) {
		t.Fatalf("no fresh code: %s", next)
	}
}
