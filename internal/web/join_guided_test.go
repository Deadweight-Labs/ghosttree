package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Paarungsseite führt in Schritten, zeigt in jedem Zustand "New code" und
// schaltet sich über /join/pair/state weiter.
func TestJoinGuidedPairPageStatesAndNewCodeEverywhere(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	pair := pairRE.FindString(e.pairPage(t, b))

	resp, text := e.get(t, b, "/join/pair")
	for _, want := range []string{"Open a terminal on your computer", "Copy this command and run it", "Confirm here", "Waiting for your computer", "Send this to your computer", `data-join-state="waiting"`, `data-copy="#join-cmd"`, "mailto:?subject=", "| sh -s -- --pair " + pair, "New code"} {
		if !strings.Contains(text, want) && !strings.Contains(plainText(text), want) {
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
	e.accept(t, b, e.code)
	pair := pairRE.FindString(e.pairPage(t, b))
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

// Der Befehl bricht nur an Leerzeichen um: "--pair" und der Code bleiben je in
// einem unteilbaren Stück, allein die Adresse darf überall brechen. Kopiert wird
// weiter derselbe Befehl.
func TestJoinCommandWrapsOnlyAtSpaces(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	_, page := e.get(t, b, "/join/pair")
	pair := pairRE.FindString(page)
	for _, tok := range []string{"curl", "-fsSL", "|", "sh", "-s", "--", "--pair", pair} {
		if !strings.Contains(page, `<span class="join-tok">`+tok+`</span>`) {
			t.Errorf("command piece %q is not one unbreakable piece", tok)
		}
	}
	if !strings.Contains(page, `<span class="join-url">http://`) {
		t.Error("the address is not marked as the breakable piece")
	}
	if !strings.Contains(page, `<span class="join-tok">|</span><wbr>`) {
		t.Error("no break hint after the pipe")
	}
	want := "curl -fsSL " + e.srv + "/install.sh | sh -s -- --pair " + pair
	m := regexp.MustCompile(`(?s)<code id="join-cmd">(.*?)</code>`).FindStringSubmatch(page)
	if m == nil || plainText(m[1]) != want {
		t.Fatalf("copied text differs from the command: %q", plainText(m[1]))
	}
	css, err := os.ReadFile("static/shell.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".join-tok { white-space: nowrap; }") || strings.Contains(string(css), "overflow-wrap: break-word; white-space: pre-wrap") {
		t.Error("css lets the command break anywhere")
	}
}

// ctx join lehnt http außerhalb von Loopback ab; die Seite zeigt dann keinen
// Befehl, der ins Leere läuft, sondern sagt, dass der Server https braucht.
func TestJoinPairPageOverPlainHTTPSaysTheServerNeedsHTTPS(t *testing.T) {
	e := newPairEnv(t)
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	// Dieselbe Oberfläche, aber ihre öffentliche Adresse ist ein Name über http.
	var h http.Handler
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(hs.Close)
	h = New(e.st, WithPublicURL("http://gt.example"))
	e.hs, e.srv = hs, hs.URL
	b = browser(t)
	e.signInAs(t, b, "anna")
	_, text := e.get(t, b, "/join/pair")
	if !strings.Contains(text, "needs https") || strings.Contains(text, "ctx join") || strings.Contains(text, "install.sh") || strings.Contains(text, "--pair") || strings.Contains(text, "mailto:") {
		t.Fatalf("page: %s", text)
	}
	if pairRE.FindString(text) != "" {
		t.Fatalf("a code is shown for a server the installer refuses: %s", text)
	}
}

// Fehlerseiten der Paarung führen zurück zur Paarungsseite.
func TestJoinPairErrorPagesLinkBackToThePairingPage(t *testing.T) {
	e := newPairEnv(t)
	e.st.AddPerson("ben")
	if _, _, err := e.st.CreateDeviceToken("person:3", "shared-name"); err != nil {
		t.Fatal(err)
	}
	b := browser(t)
	e.signInAs(t, b, "anna")
	e.accept(t, b, e.code)
	pair := pairRE.FindString(e.pairPage(t, b))
	claim := e.claimCode(t, pair, "box")
	wrong := e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}, "confirm_code": {"WWWW"}})
	if got := body(t, wrong); wrong.StatusCode != http.StatusBadRequest || !strings.Contains(got, `href="/join/pair"`) {
		t.Fatalf("wrong code page has no way back: %d %s", wrong.StatusCode, got)
	}
	_ = claim
	// Name vergeben.
	e.clock.t = e.clock.t.Add(time.Second)
	if _, err := e.st.Join().Create("person:2"); err != nil {
		t.Fatal(err)
	}
	pair = pairRE.FindString(e.pairPage(t, b))
	e.claimLoop(t, pair, "shared-name")
	taken := e.decide(t, b, url.Values{"decision": {"approve"}, "confirm_account": {"anna"}})
	if got := body(t, taken); taken.StatusCode != http.StatusConflict || !strings.Contains(got, `href="/join/pair"`) || !strings.Contains(got, "Machine name taken") {
		t.Fatalf("name taken page: %d %s", taken.StatusCode, got)
	}
	// Die Fehlerseite der Einladung selbst hat keinen Rückweg auf /join/pair.
	resp, text := e.get(t, b, "/join/"+strings.Repeat("ab", 32))
	if resp.StatusCode != http.StatusNotFound || strings.Contains(text, `href="/join/pair"`) {
		t.Fatalf("not-found page: %d", resp.StatusCode)
	}
}
