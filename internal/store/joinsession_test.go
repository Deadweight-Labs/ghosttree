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

type pairFor struct{ Pair string }

// openPair legt die Sitzung eines Kontos an, wie es die Annahme der Einladung tut.
func openPair(j *JoinSessions, account string) pairFor {
	pair, err := j.Create(account)
	if err != nil {
		panic(err)
	}
	return pairFor{Pair: pair}
}

func codeReq(pair, machine, addr string) JoinClaimRequest {
	return JoinClaimRequest{Addr: addr, Pair: pair, Machine: machine}
}

func TestJoinSessionsExpire(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	clock.t = clock.t.Add(JoinSessionTTL + time.Second)
	if _, err := j.Claim(codeReq(o.Pair, "m", "1.1.1.1")); err != ErrJoinInvalid {
		t.Fatalf("expired: %v", err)
	}
	if j.Sessions() != 0 {
		t.Fatal("expired session stays")
	}
}

// Code-Weg: Bestätigungscode eintippen, dann liefert der Geräte-Ablauf das Token.
func TestJoinCodeModeApproveWithTheTerminalCode(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	claim, err := j.Claim(codeReq(strings.ToLower(o.Pair), "laptop", "1.1.1.1"))
	if err != nil || claim.Mode != JoinModeCode || len(claim.DeviceCode) != 64 || len(claim.Confirm) != 4 {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDevicePending) {
		t.Fatalf("poll before approval: %v", err)
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

// Die Sitzung wartet auf ihr Gerät; erst nach dem Claim gibt es etwas freizugeben.
func TestJoinWaitsForTheInstaller(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	if v := j.View("person:2"); v.State != JoinWaiting || v.Pair != o.Pair {
		t.Fatalf("new session %+v", v)
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

func exchangeFlow(t *testing.T) (*JoinSessions, JoinView, string) {
	t.Helper()
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	if _, err := j.Claim(loopReq(o.Pair, "laptop", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
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
	o := openPair(j, "person:2")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
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
	o := openPair(j, "person:2")
	thief := loopReq(o.Pair, "laptop", "6.6.6.6") // Gerätename wie beim Opfer
	thief.Challenge = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := j.Claim(thief); err != nil {
		t.Fatal(err)
	}
	// Das Opfer meldet sich danach: zweiter Claim, die Sitzung ist verworfen.
	if _, err := j.Claim(loopReq(o.Pair, "laptop", "1.1.1.1")); err != ErrJoinInvalid {
		t.Fatalf("victim claim: %v", err)
	}
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
	o := openPair(j, "person:2")
	thief := loopReq(o.Pair, "laptop", "6.6.6.6")
	thief.Challenge = base64.RawURLEncoding.EncodeToString(sha256Of("thief-secret-verifier-0000000000000000000000"))
	j.Claim(thief)
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
	o := openPair(j, "person:2")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "6.6.6.6"))
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
	o := openPair(j, "person:2")
	first, _ := j.Claim(codeReq(o.Pair, "mine", "1.1.1.1"))
	if _, err := j.Claim(codeReq(o.Pair, "thief", "2.2.2.2")); err != ErrJoinInvalid {
		t.Fatalf("second claim: %v", err)
	}
	if _, _, err := pollLater(st, clock, first.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("first device flow survived: %v", err)
	}
	// Die Seite des Kontos warnt und bietet einen neuen Code an.
	if v := j.View("person:2"); v.State != JoinCompromised {
		t.Fatalf("state %q", v.State)
	}
}

func TestJoinDeny(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
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
	o := openPair(j, "person:2")
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
	expired := openPair(j, "person:5")
	consumed := openPair(j, "person:6")
	denied := openPair(j, "person:4")
	j.Claim(codeReq(consumed.Pair, "m", "1.1.1.1"))
	j.Claim(codeReq(denied.Pair, "m", "1.1.1.2"))
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
	o := openPair(st.Join(), "person:2")
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
	o := openPair(j, "person:2")
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

func TestJoinLoopbackClaimsAreLimitedPerNetwork(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	claim := func(addr string) error {
		o := openPair(j, fmt.Sprintf("person:%s-%d", addr, j.Sessions()))
		_, err := j.Claim(loopReq(o.Pair, "m", addr))
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

// N6: Ablehnung und Kompromittierung sagen dem wartenden Installer Bescheid.
func TestJoinDenyLoopbackRedirectsWithAccessDenied(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	dec, err := j.Decide("person:2", false, j.View("person:2").Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dec.Redirect, "http://127.0.0.1:40123/callback?") || !strings.Contains(dec.Redirect, "error=access_denied") ||
		!strings.Contains(dec.Redirect, "state=state-12345678") || strings.Contains(dec.Redirect, "code=") {
		t.Fatalf("deny redirect %q", dec.Redirect)
	}
	// Code-Weg: kein Ziel.
	o2 := openPair(j, "person:3")
	j.Claim(codeReq(o2.Pair, "m", "1.1.1.1"))
	if dec, _ := j.Decide("person:3", false, j.View("person:3").Nonce, "", nil); dec.Redirect != "" {
		t.Fatalf("code mode redirect %q", dec.Redirect)
	}
}

func TestJoinCompromisedLoopbackOffersTheCallbackTarget(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	if _, err := j.Claim(loopReq(o.Pair, "thief", "2.2.2.2")); err != ErrJoinInvalid {
		t.Fatal(err)
	}
	v := j.View("person:2")
	if v.State != JoinCompromised || !strings.HasPrefix(v.Callback, "http://127.0.0.1:40123/callback?") || !strings.Contains(v.Callback, "error=access_denied") {
		t.Fatalf("view %+v", v)
	}
	// Ohne Callback-Ziel (Code-Weg) gibt es keins.
	o2 := openPair(j, "person:3")
	j.Claim(codeReq(o2.Pair, "m", "1.1.1.1"))
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
	o := openPair(j, "person:2")
	req.Pair = o.Pair
	j.Claim(req)
	dec, _ := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
	if _, err := j.Exchange("9.9.9.9", authCode(dec.Redirect), bad); err != ErrJoinInvalid {
		t.Fatalf("verifier with '+' accepted: %v", err)
	}
}

// Nit: die Datenbankprüfung läuft ohne Sperre, und danach zählt der aktuelle Zustand.
func TestJoinDecideChecksTheMachineOutsideTheLockAndRechecksState(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
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

func TestJoinClaimLapsesAfterTheWaitAndFreesTheSession(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	claim, err := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("state %s", v.State)
	}
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	v := j.View("person:2")
	if v.State != JoinExpired {
		t.Fatalf("a request nobody approved must expire, got %s", v.State)
	}
	if _, err := j.Decide("person:2", true, "x", "", nil); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("decide on a lapsed request: %v", err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("device flow of a lapsed request lives: %v", err)
	}
	if _, err := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1")); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("a lapsed code must not be claimable: %v", err)
	}
	// Ein neuer Code ersetzt die abgelaufene Sitzung.
	if _, err := j.Create("person:2"); err != nil {
		t.Fatal(err)
	}
	if v := j.View("person:2"); v.State != JoinWaiting {
		t.Fatalf("after a new code: %s", v.State)
	}
}

func TestJoinSameInstallerMayClaimAgainWithItsResumeToken(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	first, err := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	if err != nil || first.Resume == "" {
		t.Fatalf("claim: %+v %v", first, err)
	}
	clock.t = clock.t.Add(30 * time.Second)
	again := codeReq(o.Pair, "laptop", "1.1.1.1")
	again.Resume = first.Resume
	second, err := j.Claim(again)
	if err != nil {
		t.Fatalf("rerun of the same installer burned the code: %v", err)
	}
	if second.DeviceCode == first.DeviceCode || second.Resume == first.Resume || second.Resume == "" {
		t.Fatal("the rerun must get its own device flow and a fresh resume token")
	}
	if _, _, err := pollLater(st, clock, first.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("the first device flow survived the rerun: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("state %s", v.State)
	}
	// Das alte Token gilt nicht mehr: es wurde mit dem ersten Claim ersetzt.
	stale := codeReq(o.Pair, "laptop", "1.1.1.1")
	stale.Resume = first.Resume
	if _, err := j.Claim(stale); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("a spent resume token worked: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinCompromised {
		t.Fatalf("state %s", v.State)
	}
}

// Name und Netz beweisen nichts: ein Fremder kennt den Gerätenamen von der
// Freigabeseite und teilt hinter NAT, CGNAT oder Proxy die Absenderadresse.
func TestJoinSameNameAndNetworkWithoutTheResumeTokenIsASecondClaim(t *testing.T) {
	for name, mod := range map[string]func(*JoinClaimRequest){
		"no token":      func(*JoinClaimRequest) {},
		"wrong token":   func(r *JoinClaimRequest) { r.Resume = strings.Repeat("0", 32) },
		"name in caps":  func(r *JoinClaimRequest) { r.Machine = "LAPTOP" },
		"same network":  func(r *JoinClaimRequest) { r.Addr = "1.1.1.1" },
		"other network": func(r *JoinClaimRequest) { r.Addr = "6.6.6.6" },
	} {
		t.Run(name, func(t *testing.T) {
			st, clock := pairFixture(t)
			j := st.Join()
			o := openPair(j, "person:2")
			first, _ := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
			clock.t = clock.t.Add(time.Second)
			req := codeReq(o.Pair, "laptop", "1.1.1.1")
			mod(&req)
			if _, err := j.Claim(req); !errors.Is(err, ErrJoinInvalid) {
				t.Fatalf("second claim: %v", err)
			}
			if v := j.View("person:2"); v.State != JoinCompromised {
				t.Fatalf("state %s", v.State)
			}
			if _, _, err := pollLater(st, clock, first.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
				t.Fatalf("the victim's device flow survived: %v", err)
			}
		})
	}
}

func TestJoinResumeCannotSwitchTheMode(t *testing.T) {
	for name, tc := range map[string]struct {
		first, second func(string) JoinClaimRequest
	}{
		"loopback to code": {func(p string) JoinClaimRequest { return loopReq(p, "m", "1.1.1.1") }, func(p string) JoinClaimRequest { return codeReq(p, "m", "1.1.1.1") }},
		"code to loopback": {func(p string) JoinClaimRequest { return codeReq(p, "m", "1.1.1.1") }, func(p string) JoinClaimRequest { return loopReq(p, "m", "1.1.1.1") }},
	} {
		t.Run(name, func(t *testing.T) {
			st, _ := pairFixture(t)
			j := st.Join()
			o := openPair(j, "person:2")
			first, err := j.Claim(tc.first(o.Pair))
			if err != nil {
				t.Fatal(err)
			}
			req := tc.second(o.Pair)
			req.Resume = first.Resume
			if _, err := j.Claim(req); !errors.Is(err, ErrJoinInvalid) {
				t.Fatalf("mode switch with a valid token: %v", err)
			}
			if v := j.View("person:2"); v.State != JoinCompromised {
				t.Fatalf("state %s", v.State)
			}
		})
	}
}

func TestJoinResumeKeepsTheCountOfWrongConfirmations(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	claim, _ := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	for i := 0; i < maxJoinConfirmFails-1; i++ {
		if _, err := j.Decide("person:2", true, j.View("person:2").Nonce, "ZZZZ", nil); !errors.Is(err, ErrJoinConfirm) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	req := codeReq(o.Pair, "laptop", "1.1.1.1")
	req.Resume = claim.Resume
	if _, err := j.Claim(req); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Decide("person:2", true, j.View("person:2").Nonce, "ZZZZ", nil); !errors.Is(err, ErrJoinConfirm) {
		t.Fatal(err)
	}
	if v := j.View("person:2"); v.State != JoinCompromised {
		t.Fatalf("a rerun reset the wrong-code count: %s", v.State)
	}
}

func TestJoinResumeNeedsAPendingRequest(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	first, _ := j.Claim(loopReq(o.Pair, "m", "1.1.1.1"))
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	req := loopReq(o.Pair, "m", "1.1.1.1")
	req.Resume = first.Resume
	if _, err := j.Claim(req); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("a lapsed request resumed: %v", err)
	}
}

// Ein Claim auf eine abgelaufene, nie freigegebene Anfrage ist ein Timeout und
// kein "anderes Gerät".
func TestJoinClaimAfterTheWaitSaysTimeoutNotAnotherDevice(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	if _, err := j.Claim(codeReq(o.Pair, "laptop", "2.2.2.2")); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("claim after the wait: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinExpired {
		t.Fatalf("state %s, want expired", v.State)
	}
}

// Das Login-Fenster endet 30 Minuten nach dem Anlegen; ein Claim verlängert es nicht.
// expires_in nennt aber nur, wie lange die Anfrage auf die Freigabe wartet.
func TestJoinClaimExpiresInIsTheShorterOfWaitAndLoginWindow(t *testing.T) {
	for name, req := range map[string]func(string) JoinClaimRequest{
		"loopback": func(p string) JoinClaimRequest { return loopReq(p, "m", "1.1.1.1") },
		"code":     func(p string) JoinClaimRequest { return codeReq(p, "m", "1.1.1.1") },
	} {
		t.Run(name, func(t *testing.T) {
			st, clock := pairFixture(t)
			j := st.Join()
			o := openPair(j, "person:2")
			start := clock.t
			clock.t = start.Add(time.Minute)
			claim, err := j.Claim(req(o.Pair))
			if err != nil {
				t.Fatal(err)
			}
			if claim.ExpiresIn != JoinClaimTTL {
				t.Fatalf("expires_in %v want %v", claim.ExpiresIn, JoinClaimTTL)
			}
			clock.t = start.Add(JoinMaxLifetime + time.Second)
			if v := j.View("person:2"); v.State != JoinNone {
				t.Fatalf("session outlived the window: %q", v.State)
			}
		})
	}
}

// Eine wiederaufgenommene Anfrage nahe am Ende des Login-Fensters meldet den
// Rest des Fensters.
func TestJoinClaimExpiresInNeverExceedsTheLoginWindow(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	start := clock.t
	req := loopReq(o.Pair, "m", "1.1.1.1")
	claim, err := j.Claim(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Duration{4, 8, 12, 16, 20, 24, 28} {
		clock.t = start.Add(at * time.Minute)
		req.Resume = claim.Resume
		if claim, err = j.Claim(req); err != nil {
			t.Fatalf("resume at %dm: %v", at, err)
		}
	}
	if claim.ExpiresIn != 2*time.Minute {
		t.Fatalf("expires_in %v want 2m", claim.ExpiresIn)
	}
}

// Läuft eine Loopback-Anfrage unfreigegeben ab, führt die Seite den Browser
// mit access_denied zurück zum Installer.
func TestJoinExpiredLoopbackOffersTheCallbackTarget(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	if _, err := j.Claim(loopReq(o.Pair, "m", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	v := j.View("person:2")
	if v.State != JoinExpired || !strings.HasPrefix(v.Callback, "http://127.0.0.1:40123/callback?") || !strings.Contains(v.Callback, "error=access_denied") {
		t.Fatalf("view %+v", v)
	}
	o2 := openPair(j, "person:3")
	if _, err := j.Claim(codeReq(o2.Pair, "m", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	if v := j.View("person:3"); v.State != JoinExpired || v.Callback != "" {
		t.Fatalf("code view %+v", v)
	}
}

// Abgelaufene Loopback-Anfragen zählen nicht mehr gegen die Grenze je Netz.
func TestJoinLoopbackBusyIgnoresLapsedClaims(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	for i := 0; i < maxDevicePerClient; i++ {
		o := openPair(j, fmt.Sprintf("person:%d", i+10))
		if _, err := j.Claim(loopReq(o.Pair, "m", "5.5.5.5")); err != nil {
			t.Fatal(err)
		}
	}
	o := openPair(j, "person:99")
	if _, err := j.Claim(loopReq(o.Pair, "m", "5.5.5.5")); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("full: %v", err)
	}
	clock.t = clock.t.Add(JoinClaimTTL + time.Second)
	if _, err := j.Claim(loopReq(o.Pair, "m", "5.5.5.5")); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
}

// Schlägt das Erzeugen des Wiederaufnahme-Tokens fehl, bleibt die frühere
// Anfrage unverändert und der Installer kann es erneut versuchen.
func TestJoinResumeFailureKeepsTheEarlierRequest(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	first, err := j.Claim(codeReq(o.Pair, "m", "1.1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	real := joinRandomHex
	joinRandomHex = func(int) (string, error) { return "", errors.New("no entropy") }
	req := codeReq(o.Pair, "m", "1.1.1.1")
	req.Resume = first.Resume
	_, err = j.Claim(req)
	joinRandomHex = real
	if err == nil {
		t.Fatal("claim succeeded without randomness")
	}
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("state %s, want claimed", v.State)
	}
	if _, err := j.Claim(req); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// Scheitert das neue Gerät nach einem Resume, endet die Anfrage ehrlich als
// abgelaufen und nicht als Fremdzugriff.
func TestJoinResumeThatFailsToStartLapsesInsteadOfCompromising(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	first, err := j.Claim(codeReq(o.Pair, "laptop", "1.1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(30 * time.Second)
	j.startFlow = func(string, string, string, time.Duration) (DeviceStart, error) {
		return DeviceStart{}, ErrDeviceBusy
	}
	again := codeReq(o.Pair, "laptop", "1.1.1.1")
	again.Resume = first.Resume
	if _, err := j.Claim(again); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("claim: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinExpired {
		t.Fatalf("state %s, want expired", v.State)
	}
}

func TestJoinClaimResolvesTheMachineNameAfterTheCodeIsChecked(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	calls := 0
	resolve := func(account, machine string, auto bool) (string, error) {
		calls++
		if account != "person:2" || !auto {
			t.Errorf("resolve(%q, %q, %v)", account, machine, auto)
		}
		return machine + "-x", nil
	}
	// A wrong code never reaches the resolver: nothing about machine names leaks.
	bad := loopReq("ZZZZ-ZZZZ", "box", "1.1.1.1")
	bad.Auto, bad.Resolve = true, resolve
	if _, err := j.Claim(bad); err != ErrJoinInvalid || calls != 0 {
		t.Fatalf("wrong code: err=%v calls=%d", err, calls)
	}
	req := loopReq(o.Pair, "box", "1.1.1.1")
	req.Auto, req.Resolve = true, resolve
	out, err := j.Claim(req)
	if err != nil || out.Machine != "box-x" || calls != 1 {
		t.Fatalf("claim: %+v %v calls=%d", out, err, calls)
	}
	if v := j.View("person:2"); v.Machine != "box-x" {
		t.Fatalf("the page shows %q", v.Machine)
	}
}

func TestJoinClaimKeepsTheCodeWhenTheNameIsTaken(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	req := loopReq(o.Pair, "box", "1.1.1.1")
	req.Resolve = func(string, string, bool) (string, error) { return "", ErrMachineTaken }
	if _, err := j.Claim(req); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("err = %v", err)
	}
	if v := j.View("person:2"); v.State != JoinWaiting {
		t.Fatalf("state after a refused name = %q, want waiting", v.State)
	}
	if _, err := j.Claim(loopReq(o.Pair, "other", "1.1.1.1")); err != nil {
		t.Fatalf("the same code with another name: %v", err)
	}
}

func TestJoinClaimBurnsTheCodeAfterThreeTakenNames(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	taken := func(string, string, bool) (string, error) { return "", ErrMachineTaken }
	for i := 1; i <= 3; i++ {
		req := loopReq(o.Pair, "guess", "1.1.1.1")
		req.Resolve = taken
		if _, err := j.Claim(req); !errors.Is(err, ErrMachineTaken) {
			t.Fatalf("conflict %d: err = %v", i, err)
		}
		want := JoinWaiting
		if i == 3 {
			want = JoinCompromised
		}
		if v := j.View("person:2"); v.State != want {
			t.Fatalf("after conflict %d the state is %q, want %q", i, v.State, want)
		}
	}
	if _, err := j.Claim(loopReq(o.Pair, "free-name", "1.1.1.1")); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("a burned code still claims: %v", err)
	}
}

func TestJoinCancelledOnlyAffectsAJustConnectedSessionOfThatMachine(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	o := openPair(j, "person:2")
	j.Cancelled("person:2", "box")
	if v := j.View("person:2"); v.State != JoinWaiting {
		t.Fatalf("a waiting session changed to %q", v.State)
	}
	if _, err := j.Claim(loopReq(o.Pair, "box", "1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	dec, err := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := pkce()
	grant, err := j.Exchange("1.1.1.1", callbackCodeFrom(dec.Redirect), verifier)
	if err != nil {
		t.Fatal(err)
	}
	j.Delivered(grant)
	j.Cancelled("person:2", "another-box")
	if v := j.View("person:2"); v.State != JoinConnected {
		t.Fatalf("another machine's revocation changed the state to %q", v.State)
	}
	j.Cancelled("person:2", "BOX")
	if v := j.View("person:2"); v.State != JoinDenied {
		t.Fatalf("state after the installer revoked its token = %q, want denied", v.State)
	}
}

func callbackCodeFrom(redirect string) string {
	i := strings.Index(redirect, "code=")
	return strings.SplitN(redirect[i+5:], "&", 2)[0]
}
