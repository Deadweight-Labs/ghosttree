package store

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type joinClock struct{ t time.Time }

func pairFixture(t *testing.T) (*Store, *joinClock) {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := &joinClock{t: time.Now()}
	st.Device().SetClock(func() time.Time { return clock.t })
	st.Join().SetClock(func() time.Time { return clock.t })
	return st, clock
}

// pollLater wartet länger als jedes Poll-Intervall, damit kein slow_down dazwischenkommt.
func pollLater(st *Store, clock *joinClock, deviceCode string) (DeviceApproval, time.Duration, error) {
	clock.t = clock.t.Add(time.Minute)
	return st.Device().Poll(deviceCode)
}

const testInvite = "invite-code-1"

// pkce liefert Verifier und Challenge (S256).
func pkce() (verifier, challenge string) {
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func loopReq(pair, machine, addr string) JoinClaimRequest {
	_, ch := pkce()
	return JoinClaimRequest{Addr: addr, Pair: pair, Machine: machine, Challenge: ch, State: "state-12345678", Port: 40123}
}

func codeReq(pair, machine, addr string) JoinClaimRequest {
	return JoinClaimRequest{Addr: addr, Pair: pair, Machine: machine}
}

func TestJoinOpenCreatesOneSessionPerBrowserAndInvitation(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	a, err := j.Open(testInvite, "")
	if err != nil || a.ID == "" || len(a.Pair) != 9 || a.Pair[4] != '-' || NormalizeUserCode(a.Pair) == "" {
		t.Fatalf("open: %+v %v", a, err)
	}
	// Dasselbe Cookie: dieselbe Sitzung, kein neues Cookie.
	again, _ := j.Open(testInvite, a.ID)
	if again.Pair != a.Pair || again.ID != "" || j.Sessions() != 1 {
		t.Fatalf("reload: %+v sessions=%d", again, j.Sessions())
	}
	// Anderer Browser oder Cookie einer anderen Einladung: eigene Sitzung.
	other, _ := j.Open(testInvite, "")
	third, _ := j.Open("invite-code-2", a.ID)
	if other.Pair == a.Pair || third.Pair == a.Pair || third.ID == "" || j.Sessions() != 3 {
		t.Fatalf("others: %+v %+v", other, third)
	}
}

func TestJoinOpenCapsSessionsPerInvitationAndKeepsOtherInvitations(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	first, _ := j.Open("other", "")
	for i := 0; i < maxJoinPerInvite+5; i++ {
		if _, err := j.Open(testInvite, ""); err != nil {
			t.Fatal(err)
		}
	}
	if n := j.Sessions(); n != maxJoinPerInvite+1 {
		t.Fatalf("%d sessions", n)
	}
	if _, err := j.Claim(codeReq(first.Pair, "m", "1.1.1.1")); err != nil {
		t.Fatalf("another invitation lost its session: %v", err)
	}
}

func TestJoinSessionsExpire(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	clock.t = clock.t.Add(JoinSessionTTL + time.Second)
	if _, err := j.Claim(codeReq(o.Pair, "m", "1.1.1.1")); err != ErrJoinInvalid {
		t.Fatalf("expired: %v", err)
	}
	if j.Sessions() != 0 {
		t.Fatal("expired session stays")
	}
}

// Installation zuerst: Gerät meldet sich vor dem Login, Bindung kommt später.
func TestJoinCodeModeInstallerFirstThenLoginThenApprove(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	claim, err := j.Claim(codeReq(strings.ToLower(o.Pair), "laptop", "1.1.1.1"))
	if err != nil || claim.Mode != JoinModeCode || len(claim.DeviceCode) != 64 || len(claim.Confirm) != 4 {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDevicePending) {
		t.Fatalf("poll before login: %v", err)
	}
	if err := j.Bind(testInvite, o.ID, "person:2"); err != nil {
		t.Fatal(err)
	}
	v := j.View("person:2")
	if v.State != JoinClaimed || v.Machine != "laptop" || v.Mode != JoinModeCode || v.Nonce == "" {
		t.Fatalf("view %+v", v)
	}
	// Falscher Bestätigungscode, dann der richtige.
	if _, err := j.Decide("person:2", true, v.Nonce, "ZZZZ", nil); !errors.Is(err, ErrJoinConfirm) {
		t.Fatalf("wrong confirm: %v", err)
	}
	if _, err := j.Decide("person:2", true, v.Nonce, strings.ToLower(claim.Confirm), nil); err != nil {
		t.Fatal(err)
	}
	appr, _, err := pollLater(st, clock, claim.DeviceCode)
	if err != nil || appr.AccountID != "person:2" || appr.Machine != "laptop" {
		t.Fatalf("poll: %+v %v", appr, err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("second poll: %v", err)
	}
	// Erst nach ausgestelltem Token gilt die Sitzung als verbunden.
	if v := j.View("person:2"); v.State == JoinConnected {
		t.Fatal("connected before the token was issued")
	}
	j.DeliveredDevice(claim.DeviceCode)
	if v := j.View("person:2"); v.State != JoinConnected {
		t.Fatalf("after delivery %q", v.State)
	}
}

// Login zuerst: Konto gebunden, Seite wartet, dann meldet sich das Gerät.
func TestJoinLoginFirstThenInstaller(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	j.Bind(testInvite, o.ID, "person:2")
	if v := j.View("person:2"); v.State != JoinWaiting || v.Pair != o.Pair {
		t.Fatalf("after bind %+v", v)
	}
	if _, err := j.Decide("person:2", true, "x", "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("approve without device: %v", err)
	}
	if _, err := j.Claim(loopReq(o.Pair, "box", "2.2.2.2")); err != nil {
		t.Fatal(err)
	}
	if v := j.View("person:2"); v.State != JoinClaimed || v.Mode != JoinModeLoopback {
		t.Fatalf("view %+v", v)
	}
}

func TestJoinBindNeedsTheSameInvitationAndBrowser(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	// Falsche Einladung oder Cookie: das Konto bekommt eine neue, eigene Sitzung.
	j.Bind("invite-code-2", o.ID, "person:3")
	if v := j.View("person:3"); v.State != JoinWaiting || v.Pair == o.Pair {
		t.Fatalf("foreign bind %+v", v)
	}
	if _, err := j.Claim(codeReq(o.Pair, "m", "1.1.1.1")); err != nil {
		t.Fatalf("the browser's session was taken: %v", err)
	}
	// Ein Konto, das schon eine Sitzung hat, behält nur die neue.
	j.Bind(testInvite, o.ID, "person:3")
	if j.Sessions() != 1 {
		t.Fatalf("sessions %d", j.Sessions())
	}
}

func exchangeFlow(t *testing.T) (*JoinSessions, JoinView, string) {
	t.Helper()
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	if _, err := j.Claim(loopReq(o.Pair, "laptop", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	j.Bind(testInvite, o.ID, "person:2")
	v := j.View("person:2")
	dec, err := j.Decide("person:2", true, v.Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return j, v, dec.Redirect
}

func TestJoinLoopbackApproveRedirectsToTheClaimedPortOnly(t *testing.T) {
	_, _, target := exchangeFlow(t)
	if !strings.HasPrefix(target, "http://127.0.0.1:40123/callback?") || !strings.Contains(target, "state=state-12345678") ||
		!strings.Contains(target, "code=") {
		t.Fatalf("redirect %q", target)
	}
}

func authCode(target string) string {
	i := strings.Index(target, "code=")
	return strings.SplitN(target[i+5:], "&", 2)[0]
}

func TestJoinExchangeNeedsTheVerifierAndWorksOnce(t *testing.T) {
	j, _, target := exchangeFlow(t)
	code := authCode(target)
	verifier, _ := pkce()
	if _, err := j.Exchange("9.9.9.9", code, strings.Repeat("w", 43)); err != ErrJoinInvalid {
		t.Fatalf("wrong verifier: %v", err)
	}
	// Ein Fehlversuch verbraucht den Code.
	if _, err := j.Exchange("9.9.9.9", code, verifier); err != ErrJoinInvalid {
		t.Fatalf("code survived a failed try: %v", err)
	}
}

func TestJoinExchangeSuccessAndReuse(t *testing.T) {
	j, _, target := exchangeFlow(t)
	code := authCode(target)
	verifier, _ := pkce()
	g, err := j.Exchange("9.9.9.9", code, verifier)
	if err != nil || g.Account != "person:2" || g.Machine != "laptop" {
		t.Fatalf("exchange: %+v %v", g, err)
	}
	if v := j.View("person:2"); v.State == JoinConnected {
		t.Fatal("connected before delivery")
	}
	j.Delivered(g)
	if v := j.View("person:2"); v.State != JoinConnected {
		t.Fatalf("state %q", v.State)
	}
	if _, err := j.Exchange("9.9.9.9", code, verifier); err != ErrJoinInvalid {
		t.Fatalf("reuse: %v", err)
	}
}

func TestJoinExchangeExpires(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	j.Bind(testInvite, o.ID, "person:2")
	dec, _ := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
	clock.t = clock.t.Add(joinAuthTTL + time.Second)
	verifier, _ := pkce()
	if _, err := j.Exchange("9.9.9.9", authCode(dec.Redirect), verifier); err != ErrJoinInvalid {
		t.Fatalf("expired: %v", err)
	}
}

// Dieb-Szenario: wer den Paarungscode abfängt und zuerst claimt, bekommt weder den
// Autorisierungscode (er geht auf den Loopback des Browsers) noch ein Token.
func TestJoinThiefClaimsFirstGetsNothing(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	thief := loopReq(o.Pair, "laptop", "6.6.6.6") // Gerätename wie beim Opfer
	thief.Challenge = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := j.Claim(thief); err != nil {
		t.Fatal(err)
	}
	// Das Opfer meldet sich danach: zweiter Claim, die Sitzung ist verworfen.
	if _, err := j.Claim(loopReq(o.Pair, "laptop", "1.1.1.1")); err != ErrJoinInvalid {
		t.Fatalf("victim claim: %v", err)
	}
	j.Bind(testInvite, o.ID, "person:2")
	v := j.View("person:2")
	if v.State != JoinCompromised {
		t.Fatalf("state %q", v.State)
	}
	if _, err := j.Decide("person:2", true, v.Nonce, "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("approve of a compromised session: %v", err)
	}
}

// Der Dieb claimt zuerst und das Opfer gibt trotzdem frei (es sieht nur "laptop"):
// der Code geht an den Loopback des Opfers; der Dieb kennt ihn nicht, seinen
// Verifier gibt es auch nicht, und mit dem Verifier des Opfers (den nur dessen
// CLI hat) lässt sich der Code nicht tauschen, wenn der Dieb eine andere
// Challenge nannte.
func TestJoinStolenPairCodeWithApprovedVictimStillGivesTheThiefNothing(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	thief := loopReq(o.Pair, "laptop", "6.6.6.6")
	thief.Challenge = base64.RawURLEncoding.EncodeToString(sha256Of("thief-secret-verifier-0000000000000000000000"))
	j.Claim(thief)
	j.Bind(testInvite, o.ID, "person:2")
	dec, err := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Der Dieb sieht die URL nie; selbst mit einem geratenen Code scheitert er.
	if _, err := j.Exchange("6.6.6.6", strings.Repeat("a", 64), "thief-secret-verifier-0000000000000000000000"); err != ErrJoinInvalid {
		t.Fatal("guessed code worked")
	}
	// Das Opfer-Gerät (anderer Verifier) kann den Code nicht einlösen.
	verifier, _ := pkce()
	if _, err := j.Exchange("1.1.1.1", authCode(dec.Redirect), verifier); err != ErrJoinInvalid {
		t.Fatalf("exchange with a foreign verifier: %v", err)
	}
}

func sha256Of(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }

func TestJoinCodeModeThiefCannotApproveWithoutTheTerminalCode(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "6.6.6.6"))
	j.Bind(testInvite, o.ID, "person:2")
	nonce := j.View("person:2").Nonce
	for i := 0; i < maxJoinConfirmFails; i++ {
		if _, err := j.Decide("person:2", true, nonce, "", nil); !errors.Is(err, ErrJoinConfirm) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if v := j.View("person:2"); v.State != JoinCompromised {
		t.Fatalf("state %q", v.State)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("the device flow of a compromised session lives: %v", err)
	}
}

func TestJoinSecondClaimDropsTheSessionAndItsDeviceFlow(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	first, _ := j.Claim(codeReq(o.Pair, "mine", "1.1.1.1"))
	if _, err := j.Claim(codeReq(o.Pair, "thief", "2.2.2.2")); err != ErrJoinInvalid {
		t.Fatalf("second claim: %v", err)
	}
	if _, _, err := pollLater(st, clock, first.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("first device flow survived: %v", err)
	}
	// Die Einladungsseite im selben Browser bekommt jetzt einen neuen Code.
	again, _ := j.Open(testInvite, o.ID)
	if again.ID == "" || again.Pair == o.Pair {
		t.Fatalf("reload after compromise: %+v", again)
	}
}

func TestJoinDeny(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	j.Bind(testInvite, o.ID, "person:2")
	if _, err := j.Decide("person:2", false, j.View("person:2").Nonce, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceDenied) {
		t.Fatalf("poll: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinDenied {
		t.Fatalf("state %q", v.State)
	}
	if _, err := j.Claim(codeReq(o.Pair, "x", "1.1.1.1")); err != ErrJoinInvalid {
		t.Fatalf("claim after deny: %v", err)
	}
}

func TestJoinApproveIsBoundToTheShownRequest(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	j.Claim(loopReq(pair, "laptop", "1.1.1.1"))
	if _, err := j.Decide("person:2", true, "stale-nonce", "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("stale nonce: %v", err)
	}
	if _, err := j.Decide("person:2", true, "", "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("no nonce: %v", err)
	}
	nonce := j.View("person:2").Nonce
	boom := errors.New("name taken")
	if _, err := j.Decide("person:2", true, nonce, "", func(m string) error {
		if m != "laptop" {
			t.Errorf("check saw %q", m)
		}
		return boom
	}); err != boom {
		t.Fatalf("check error: %v", err)
	}
	if _, err := j.Decide("person:2", true, nonce, "", nil); err != nil {
		t.Fatalf("a failed check consumed the request: %v", err)
	}
}

func TestJoinDecideBelongsToTheBoundAccount(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	j.Claim(loopReq(pair, "laptop", "1.1.1.1"))
	nonce := j.View("person:2").Nonce
	if _, err := j.Decide("person:9", true, nonce, "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("other account: %v", err)
	}
	if v := j.View("person:9"); v.State != JoinNone || v.Pair != "" {
		t.Fatalf("other account sees %+v", v)
	}
}

func TestJoinJoinFlowsAreInvisibleToTheUserCodeFlow(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	for _, code := range []string{o.Pair, claim.Confirm, claim.Confirm + claim.Confirm} {
		if _, err := st.Device().Lookup(code, "person:2"); err == nil {
			t.Fatalf("/ui/device finds a join flow by %q", code)
		}
		if err := st.Device().Decide(code, "person:2", true); err == nil {
			t.Fatalf("/ui/device decided a join flow by %q", code)
		}
	}
	if st.Device().JoinStatus(DeviceHash(claim.DeviceCode)) != "pending" {
		t.Fatal("the join flow was touched")
	}
}

func TestJoinUnknownExpiredAndConsumedAreTheSameError(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	expired, _ := j.Open("a", "")
	consumed, _ := j.Open("b", "")
	denied, _ := j.Open("c", "")
	j.Claim(codeReq(consumed.Pair, "m", "1.1.1.1"))
	j.Claim(codeReq(denied.Pair, "m", "1.1.1.2"))
	j.Bind("c", denied.ID, "person:4")
	j.Decide("person:4", false, j.View("person:4").Nonce, "", nil)
	cases := map[string]string{"unknown": "BCDF-GHJK", "claimed": consumed.Pair, "denied": denied.Pair, "garbage": "not a code", "empty": ""}
	for name, code := range cases {
		if _, err := j.Claim(codeReq(code, "m", "c-"+name)); err != ErrJoinInvalid {
			t.Errorf("%s: %v (want exactly ErrJoinInvalid)", name, err)
		}
	}
	clock.t = clock.t.Add(JoinSessionTTL + time.Second)
	if _, err := j.Claim(codeReq(expired.Pair, "m", "c-exp")); err != ErrJoinInvalid {
		t.Errorf("expired: %v", err)
	}
}

func TestJoinRestartLosesSessionsNeutrally(t *testing.T) {
	st, _ := pairFixture(t)
	o, _ := st.Join().Open(testInvite, "")
	fresh := NewJoinSessions(st.Device())
	if _, err := fresh.Claim(codeReq(o.Pair, "m", "a")); err != ErrJoinInvalid {
		t.Fatalf("claim after restart: %v", err)
	}
	if v := fresh.View("person:2"); v.State != JoinNone {
		t.Fatalf("view %q", v.State)
	}
}

func TestJoinNewCodeReplacesTheOldAndDropsItsDevice(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	first, _ := j.Create("person:2")
	claim, _ := j.Claim(codeReq(first, "m", "1.1.1.1"))
	second, _ := j.Create("person:2")
	if first == second {
		t.Fatal("same code twice")
	}
	if _, err := j.Claim(codeReq(first, "m", "2.2.2.2")); err != ErrJoinInvalid {
		t.Fatalf("old code: %v", err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("old device flow survived: %v", err)
	}
}

func TestJoinWrongGuessesLockTheNetworkIncludingIPv6Prefixes(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	for i := 0; i < maxJoinFailures; i++ {
		if _, err := j.Claim(codeReq("BCDF-GHJK", "m", "9.9.9.9")); err != ErrJoinInvalid {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	for _, code := range []string{pair, "BCDF-GHJK"} {
		if _, err := j.Claim(codeReq(code, "m", "9.9.9.9")); !errors.Is(err, ErrJoinLocked) {
			t.Fatalf("locked address, code %q: %v", code, err)
		}
	}
	// IPv6: Adressen eines /64 teilen sich die Sperre.
	for i := 0; i < maxJoinFailures; i++ {
		j.Claim(codeReq("BCDF-GHJK", "m", "2001:db8:1:2::"+string(rune('1'+i))))
	}
	if _, err := j.Claim(codeReq(pair, "m", "2001:db8:1:2::ff")); !errors.Is(err, ErrJoinLocked) {
		t.Fatalf("same /64: %v", err)
	}
	// Ein anderes /64 im selben /48 hat die höhere zweite Stufe: erst viele Fehlversuche sperren es.
	if _, err := j.Claim(codeReq("BCDF-GHJK", "m", "2001:db8:1:3::1")); err != ErrJoinInvalid {
		t.Fatalf("other /64: %v", err)
	}
	for i := 0; i < maxJoinFailures*joinWiderFactor; i++ {
		j.Claim(codeReq("BCDF-GHJK", "m", fmt.Sprintf("2001:db8:1:%x::1", i+16)))
	}
	if _, err := j.Claim(codeReq(pair, "m", "2001:db8:1:ffff::1")); !errors.Is(err, ErrJoinLocked) {
		t.Fatalf("same /48: %v", err)
	}
	// Eine fremde Adresse und die Sitzung sind unberührt.
	if _, err := j.Claim(codeReq(pair, "m", "8.8.8.8")); err != nil {
		t.Fatalf("other address: %v", err)
	}
	clock.t = clock.t.Add(joinFailureWindow + time.Second)
	if _, err := j.Claim(codeReq("BCDF-GHJK", "m", "9.9.9.9")); err != ErrJoinInvalid {
		t.Fatalf("after the window: %v", err)
	}
}

func TestJoinBusyAddressAnswersTheSameForValidAndInvalidCodes(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	for i := 0; i < maxDevicePerClient; i++ {
		if _, err := st.Device().Start("7.7.7.7", "other", "7.7.7.7"); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := j.Open(testInvite, "")
	_, errValid := j.Claim(codeReq(o.Pair, "m", "7.7.7.7"))
	_, errInvalid := j.Claim(codeReq("BCDF-GHJK", "m", "7.7.7.7"))
	if !errors.Is(errValid, ErrDeviceBusy) || errValid != errInvalid {
		t.Fatalf("valid=%v invalid=%v", errValid, errInvalid)
	}
	if _, err := j.Claim(codeReq(o.Pair, "m", "6.6.6.6")); err != nil {
		t.Fatal(err)
	}
}

func TestJoinLoopbackFieldsAreValidated(t *testing.T) {
	good := loopReq("x", "m", "a")
	if !good.ValidLoopback() {
		t.Fatal("good request invalid")
	}
	for name, mod := range map[string]func(*JoinClaimRequest){
		"port low": func(r *JoinClaimRequest) { r.Port = 80 }, "port high": func(r *JoinClaimRequest) { r.Port = 70000 },
		"no port": func(r *JoinClaimRequest) { r.Port = 0 }, "short challenge": func(r *JoinClaimRequest) { r.Challenge = "abc" },
		"bad challenge": func(r *JoinClaimRequest) { r.Challenge = strings.Repeat("+", 43) },
		"short state":   func(r *JoinClaimRequest) { r.State = "s" }, "state with &": func(r *JoinClaimRequest) { r.State = "state&evil=1abc" },
		"foreign host": func(r *JoinClaimRequest) { r.Host = "evil.example" }, "no state": func(r *JoinClaimRequest) { r.State = "" },
	} {
		r := good
		mod(&r)
		if r.ValidLoopback() {
			t.Errorf("%s accepted", name)
		}
	}
	v6 := good
	v6.Host = "[::1]"
	if !v6.ValidLoopback() {
		t.Error("ipv6 loopback refused")
	}
}

func TestJoinSessionsAreCapped(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	for i := 0; i < maxJoinSessions+10; i++ {
		if _, err := j.Create("person:" + strings.Repeat("x", i%7) + string(rune('A'+i%26)) + strings.Repeat("y", i/26)); err != nil {
			t.Fatal(err)
		}
	}
	if n := j.Sessions(); n > maxJoinSessions {
		t.Fatalf("%d sessions", n)
	}
}

// N1: wer den Einladungslink hat, verdrängt keine Sitzung mit Gerät.
func TestJoinOpenNeverEvictsSessionsWithADevice(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	var first JoinOpen
	for i := 0; i < maxJoinPerInvite; i++ {
		o, err := j.Open(testInvite, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = o
		}
		if _, err := j.Claim(loopReq(o.Pair, "m", fmt.Sprintf("10.0.%d.1", i))); err != nil {
			t.Fatal(err)
		}
	}
	extra, err := j.Open(testInvite, "")
	if err != nil || extra.Pair != "" || extra.ID != "" {
		t.Fatalf("overflow got a session: %+v %v", extra, err)
	}
	if n := j.Sessions(); n != maxJoinPerInvite {
		t.Fatalf("%d sessions", n)
	}
	// Die erste Sitzung ist noch beansprucht und bindbar.
	if err := j.Bind(testInvite, first.ID, "person:2"); err != nil {
		t.Fatal(err)
	}
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("state %q", v.State)
	}
	// Eine Sitzung ohne Gerät weicht weiterhin.
	st2, _ := pairFixture(t)
	j2 := st2.Join()
	for i := 0; i < maxJoinPerInvite; i++ {
		j2.Open(testInvite, "")
	}
	if o, _ := j2.Open(testInvite, ""); o.Pair == "" {
		t.Fatal("waiting sessions should still make room")
	}
}

func TestJoinLoopbackClaimsAreLimitedPerNetwork(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	claim := func(addr string) error {
		o, err := j.Open(fmt.Sprintf("inv-%s-%d", addr, j.Sessions()), "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = j.Claim(loopReq(o.Pair, "m", addr))
		return err
	}
	for i := 0; i < maxDevicePerClient; i++ {
		if err := claim("5.5.5.5"); err != nil {
			t.Fatal(err)
		}
	}
	if err := claim("5.5.5.5"); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("v4: %v", err)
	}
	if err := claim("6.6.6.6"); err != nil {
		t.Fatalf("other address: %v", err)
	}
	// IPv6: ein /64 teilt sich die Grenze, ein /48 hat das Vierfache.
	for i := 0; i < maxDevicePerClient; i++ {
		if err := claim(fmt.Sprintf("2001:db8:1:1:%x::1", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := claim("2001:db8:1:1:ffff::1"); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("/64: %v", err)
	}
	for sub := 2; sub <= 4; sub++ {
		for i := 0; i < maxDevicePerClient; i++ {
			if err := claim(fmt.Sprintf("2001:db8:1:%d::%x", sub, i+1)); err != nil {
				t.Fatalf("sub %d: %v", sub, err)
			}
		}
	}
	if err := claim("2001:db8:1:9::1"); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("/48: %v", err)
	}
}

// N4: ein beanspruchtes Gerät verlängert die Sitzung nicht über das Login-Fenster.
func TestJoinClaimDoesNotExtendBeyondTheLoginWindow(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	clock.t = clock.t.Add(JoinSessionTTL - time.Minute)
	if _, err := j.Claim(loopReq(o.Pair, "m", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	j.Bind(testInvite, o.ID, "person:2")
	clock.t = clock.t.Add(2 * time.Minute) // über TTL, Claim hält die Sitzung noch
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("claimed session should survive its TTL: %q", v.State)
	}
	clock.t = clock.t.Add(JoinMaxLifetime) // weit über das Fenster
	if v := j.View("person:2"); v.State != JoinNone {
		t.Fatalf("session outlived the window: %q", v.State)
	}
}

// N6: Ablehnung und Kompromittierung sagen dem wartenden Installer Bescheid.
func TestJoinDenyLoopbackRedirectsWithAccessDenied(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	j.Bind(testInvite, o.ID, "person:2")
	dec, err := j.Decide("person:2", false, j.View("person:2").Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dec.Redirect, "http://127.0.0.1:40123/callback?") || !strings.Contains(dec.Redirect, "error=access_denied") ||
		!strings.Contains(dec.Redirect, "state=state-12345678") || strings.Contains(dec.Redirect, "code=") {
		t.Fatalf("deny redirect %q", dec.Redirect)
	}
	// Code-Weg: kein Ziel.
	o2, _ := j.Open(testInvite, "")
	j.Claim(codeReq(o2.Pair, "m", "1.1.1.1"))
	j.Bind(testInvite, o2.ID, "person:3")
	if dec, _ := j.Decide("person:3", false, j.View("person:3").Nonce, "", nil); dec.Redirect != "" {
		t.Fatalf("code mode redirect %q", dec.Redirect)
	}
}

func TestJoinCompromisedLoopbackOffersTheCallbackTarget(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	j.Bind(testInvite, o.ID, "person:2")
	if _, err := j.Claim(loopReq(o.Pair, "thief", "2.2.2.2")); err != ErrJoinInvalid {
		t.Fatal(err)
	}
	v := j.View("person:2")
	if v.State != JoinCompromised || !strings.HasPrefix(v.Callback, "http://127.0.0.1:40123/callback?") || !strings.Contains(v.Callback, "error=access_denied") {
		t.Fatalf("view %+v", v)
	}
	// Ohne Callback-Ziel (Code-Weg) gibt es keins.
	o2, _ := j.Open(testInvite, "")
	j.Claim(codeReq(o2.Pair, "m", "1.1.1.1"))
	j.Bind(testInvite, o2.ID, "person:3")
	j.Claim(codeReq(o2.Pair, "thief", "2.2.2.2"))
	if v := j.View("person:3"); v.State != JoinCompromised || v.Callback != "" {
		t.Fatalf("view %+v", v)
	}
}

// Nit: code_verifier nur aus den Zeichen nach RFC 7636.
func TestJoinExchangeRejectsVerifiersOutsideRFC7636(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	bad := strings.Repeat("v", 42) + "+"
	sum := sha256.Sum256([]byte(bad))
	req := loopReq("", "m", "1.1.1.1")
	req.Challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	o, _ := j.Open(testInvite, "")
	req.Pair = o.Pair
	j.Claim(req)
	j.Bind(testInvite, o.ID, "person:2")
	dec, _ := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
	if _, err := j.Exchange("9.9.9.9", authCode(dec.Redirect), bad); err != ErrJoinInvalid {
		t.Fatalf("verifier with '+' accepted: %v", err)
	}
}

// Nit: die Datenbankprüfung läuft ohne Sperre, und danach zählt der aktuelle Zustand.
func TestJoinDecideChecksTheMachineOutsideTheLockAndRechecksState(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o, _ := j.Open(testInvite, "")
	j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	j.Bind(testInvite, o.ID, "person:2")
	nonce := j.View("person:2").Nonce
	_, err := j.Decide("person:2", true, nonce, "", func(string) error {
		if j.mu.TryLock() {
			j.mu.Unlock()
		} else {
			t.Error("check ran under the lock")
		}
		// Während der Prüfung meldet sich ein Dieb: Sitzung wird kompromittiert.
		j.Claim(codeReq(o.Pair, "thief", "2.2.2.2"))
		return nil
	})
	if err != ErrJoinNotReady {
		t.Fatalf("approve after state change: %v", err)
	}
}
