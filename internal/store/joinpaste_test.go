package store

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Der Code zum Einfügen (REQ-434): dieselbe Berechtigung wie der Loopback-Code,
// auf einem zweiten Weg zum Installer.

func pkceFor(seed string) (verifier, challenge string) {
	verifier = strings.Repeat(seed, 43)[:43]
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func pasteReq(pair, machine, addr, challenge string) JoinClaimRequest {
	return JoinClaimRequest{Addr: addr, Pair: pair, Machine: machine, Challenge: challenge}
}

// approvedPaste lässt ein Gerät auf dem Paste-Weg claimen und das Konto freigeben.
func approvedPaste(t *testing.T, j *JoinSessions, account, seed string) (verifier string, v JoinView) {
	t.Helper()
	o := openPair(j, account)
	verifier, challenge := pkceFor(seed)
	req := pasteReq(o.Pair, "far-box", "1.1.1.1", challenge)
	if !req.ValidLoopback() {
		t.Fatal("a claim with only a challenge is not accepted")
	}
	out, err := j.Claim(req)
	if err != nil || out.Mode != JoinModePaste {
		t.Fatalf("claim: %+v %v", out, err)
	}
	dec, err := j.Decide(account, true, j.View(account).Nonce, "", nil)
	if err != nil || dec.Redirect != "" {
		t.Fatalf("decide: %+v %v", dec, err)
	}
	return verifier, j.View(account)
}

func TestJoinPasteShowsAGroupedCodeAndExchangesItOnceWithTheVerifier(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	verifier, v := approvedPaste(t, j, "person:2", "a")
	if v.State != JoinApproved || v.Redirect != "" || !strings.Contains(v.Code, "-") || len(NormalizeAuthCode(v.Code)) != authCodeLen {
		t.Fatalf("view %+v", v)
	}
	// Eingegeben wird, was die Seite zeigt: gruppiert, auch klein und mit Leerzeichen.
	g, err := j.Exchange("9.9.9.9", " "+strings.ToLower(v.Code)+" ", verifier)
	if err != nil || g.Account != "person:2" || g.Machine != "far-box" {
		t.Fatalf("exchange: %+v %v", g, err)
	}
	if j.View("person:2").Code != "" {
		t.Fatal("the code is still shown after the exchange")
	}
	if _, err := j.Exchange("9.9.9.9", v.Code, verifier); err != ErrJoinInvalid {
		t.Fatalf("second use: %v", err)
	}
}

func TestJoinLoopbackShowsTheSameCodeAndTheFirstExchangeWins(t *testing.T) {
	for _, pasteFirst := range []bool{true, false} {
		st, _ := pairFixture(t)
		j := st.Join()
		o := openPair(j, "person:2")
		verifier, challenge := pkceFor("b")
		req := loopReq(o.Pair, "laptop", "1.1.1.1")
		req.Challenge = challenge
		if _, err := j.Claim(req); err != nil {
			t.Fatal(err)
		}
		dec, err := j.Decide("person:2", true, j.View("person:2").Nonce, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		v := j.View("person:2")
		viaCallback := callbackCodeFrom(dec.Redirect)
		if v.Redirect != dec.Redirect || NormalizeAuthCode(v.Code) != viaCallback {
			t.Fatalf("the page code and the callback code differ: %q vs %q", v.Code, viaCallback)
		}
		first, second := viaCallback, v.Code
		if pasteFirst {
			first, second = v.Code, viaCallback
		}
		if _, err := j.Exchange("9.9.9.9", first, verifier); err != nil {
			t.Fatalf("first (pasteFirst=%v): %v", pasteFirst, err)
		}
		if _, err := j.Exchange("9.9.9.9", second, verifier); err != ErrJoinInvalid {
			t.Fatalf("second (pasteFirst=%v) was accepted: %v", pasteFirst, err)
		}
	}
}

func TestJoinWrongPastedCodeDoesNotBurnTheRightOne(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	verifier, v := approvedPaste(t, j, "person:2", "c")
	for _, bad := range []string{"BCDF-GHJK-MNPQ-RTVW-X346", "garbage", "", strings.Repeat("W", 20)} {
		if bad == NormalizeAuthCode(v.Code) {
			continue
		}
		if _, err := j.Exchange("9.9.9.9", bad, verifier); err != ErrJoinInvalid {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, err := j.Exchange("9.9.9.9", v.Code, verifier); err != nil {
		t.Fatalf("the right code no longer works: %v", err)
	}
}

func TestJoinPastedCodeOfAnotherFlowGivesNothingToThisTerminal(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	_, a := approvedPaste(t, j, "person:2", "d")
	verifierB, b := approvedPaste(t, j, "person:3", "e")
	// B's terminal pastes A's code: A belongs to another verifier.
	if _, err := j.Exchange("9.9.9.9", a.Code, verifierB); err != ErrJoinInvalid {
		t.Fatalf("A's code worked with B's verifier: %v", err)
	}
	// B's own code still works for B.
	g, err := j.Exchange("9.9.9.9", b.Code, verifierB)
	if err != nil || g.Account != "person:3" {
		t.Fatalf("B: %+v %v", g, err)
	}
}

func TestJoinPastedCodeExpiresAfterMinutes(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	verifier, v := approvedPaste(t, j, "person:2", "f")
	if joinAuthTTL > 5*time.Minute {
		t.Fatalf("the code lives %v", joinAuthTTL)
	}
	clock.t = clock.t.Add(joinAuthTTL + time.Second)
	if j.View("person:2").Code != "" {
		t.Fatal("an expired code is still shown")
	}
	if _, err := j.Exchange("9.9.9.9", v.Code, verifier); err != ErrJoinInvalid {
		t.Fatalf("expired code: %v", err)
	}
}

func TestJoinPastedCodeGuessingIsRateLimited(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	verifier, v := approvedPaste(t, j, "person:2", "g")
	for i := 0; i < maxJoinFailures; i++ {
		if _, err := j.Exchange("7.7.7.7", strings.Repeat("W", 20), verifier); err != ErrJoinInvalid {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if _, err := j.Exchange("7.7.7.7", v.Code, verifier); err != ErrJoinLocked {
		t.Fatalf("the guesser may still try: %v", err)
	}
}

func TestJoinAuthCodeIsOnlyHeldAsAHashLookup(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	_, v := approvedPaste(t, j, "person:2", "h")
	j.mu.Lock()
	defer j.mu.Unlock()
	for key := range j.byAuth {
		if strings.Contains(key, NormalizeAuthCode(v.Code)) {
			t.Fatal("the lookup is keyed by the code itself")
		}
	}
}

func TestJoinClaimWithPartialLoopbackFieldsStaysInvalid(t *testing.T) {
	_, challenge := pkceFor("i")
	for _, r := range []JoinClaimRequest{
		{Challenge: challenge, State: "state-12345678"}, // state without port
		{Challenge: challenge, Port: 40123},             // port without state
		{Challenge: challenge, Host: "127.0.0.1"},       // host only
		{State: "state-12345678", Port: 40123},          // no challenge
		{Challenge: "short"},                            // malformed challenge
	} {
		if r.ValidLoopback() {
			t.Errorf("%+v accepted", r)
		}
	}
}
