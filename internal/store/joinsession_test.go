package store

import (
	"errors"
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

func TestJoinPairCodeLooksLikeAUserCode(t *testing.T) {
	st, _ := pairFixture(t)
	pair, err := st.Join().Create("person:2")
	if err != nil {
		t.Fatal(err)
	}
	if len(pair) != 9 || pair[4] != '-' || NormalizeUserCode(pair) == "" {
		t.Fatalf("pair %q", pair)
	}
}

// Installation zuerst: das Gerät meldet sich, die Seite sieht es später.
func TestJoinClaimBeforeTheBrowserLooksAndApproveDeliversOneToken(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	if v := j.View("person:2"); v.State != JoinWaiting {
		t.Fatalf("state %q", v.State)
	}
	claim, err := j.Claim("1.1.1.1", strings.ToLower(pair), "laptop", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDevicePending) {
		t.Fatalf("poll before approval: %v", err)
	}
	v := j.View("person:2")
	if v.State != JoinClaimed || v.Machine != "laptop" || v.Remote != "1.1.1.1" || v.Pair != pair {
		t.Fatalf("view %+v", v)
	}
	if err := j.Decide("person:2", true); err != nil {
		t.Fatal(err)
	}
	if j.View("person:2").State != JoinApproved {
		t.Fatal("not approved")
	}
	appr, _, err := pollLater(st, clock, claim.DeviceCode)
	if err != nil || appr.AccountID != "person:2" || appr.Machine != "laptop" {
		t.Fatalf("poll after approval: %+v %v", appr, err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("second poll: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinConnected {
		t.Fatalf("after delivery %q", v.State)
	}
}

// pollLater wartet länger als jedes Poll-Intervall, damit kein slow_down dazwischenkommt.
func pollLater(st *Store, clock *joinClock, deviceCode string) (DeviceApproval, time.Duration, error) {
	clock.t = clock.t.Add(time.Minute)
	return st.Device().Poll(deviceCode)
}

func TestJoinApproveNeedsAClaimFirst(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	j.Create("person:2")
	if err := j.Decide("person:2", true); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("approve without device: %v", err)
	}
	if err := j.Decide("person:9", true); !errors.Is(err, ErrJoinNotReady) {
		t.Fatalf("approve without session: %v", err)
	}
}

func TestJoinDeny(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	claim, _ := j.Claim("a", pair, "laptop", "a")
	if err := j.Decide("person:2", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceDenied) {
		t.Fatalf("poll: %v", err)
	}
	if v := j.View("person:2"); v.State != JoinDenied {
		t.Fatalf("state %q", v.State)
	}
	// Danach lässt sich der Code nicht mehr beanspruchen.
	if _, err := j.Claim("a", pair, "laptop", "a"); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("claim after deny: %v", err)
	}
}

func TestJoinSecondClaimIsRefusedAndShownAsWarning(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	if _, err := j.Claim("a", pair, "laptop", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Claim("b", pair, "evil", "b"); !errors.Is(err, ErrJoinInvalid) {
		t.Fatalf("second claim: %v", err)
	}
	v := j.View("person:2")
	if v.Machine != "laptop" || v.Conflicts != 1 {
		t.Fatalf("view %+v", v)
	}
}

func TestJoinUnknownExpiredAndConsumedAreTheSameError(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	expired, _ := j.Create("person:2")
	consumed, _ := j.Create("person:3")
	if _, err := j.Claim("a", consumed, "m", "a"); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(JoinSessionTTL + time.Second)
	for name, code := range map[string]string{"unknown": "BCDF-GHJK", "expired": expired, "consumed": consumed, "garbage": "not a code", "empty": ""} {
		if _, err := j.Claim("c-"+name, code, "m", "c"); err != ErrJoinInvalid {
			t.Errorf("%s: %v (want exactly ErrJoinInvalid)", name, err)
		}
	}
}

func TestJoinClaimAfterSessionExpiryExtendsToTheDeviceFlow(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	clock.t = clock.t.Add(JoinSessionTTL - time.Second)
	claim, err := j.Claim("a", pair, "m", "a")
	if err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(5 * time.Minute) // über die Frist des Codes, unter der des Geräte-Ablaufs
	if v := j.View("person:2"); v.State != JoinClaimed {
		t.Fatalf("state %q", v.State)
	}
	if err := j.Decide("person:2", true); err != nil {
		t.Fatal(err)
	}
	_ = claim
	clock.t = clock.t.Add(DeviceFlowTTL + JoinSessionTTL)
	if v := j.View("person:2"); v.State != JoinNone {
		t.Fatalf("after expiry %q", v.State)
	}
}

func TestJoinRestartLosesSessionsNeutrally(t *testing.T) {
	st, _ := pairFixture(t)
	pair, _ := st.Join().Create("person:2")
	fresh := NewJoinSessions(st.Device())
	if _, err := fresh.Claim("a", pair, "m", "a"); err != ErrJoinInvalid {
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
	claim, _ := j.Claim("a", first, "m", "a")
	second, _ := j.Create("person:2")
	if first == second {
		t.Fatal("same code twice")
	}
	if _, err := j.Claim("b", first, "m", "b"); err != ErrJoinInvalid {
		t.Fatalf("old code: %v", err)
	}
	if _, _, err := pollLater(st, clock, claim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("old device flow survived: %v", err)
	}
}

func TestJoinWrongGuessesLockTheAddress(t *testing.T) {
	st, clock := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	for i := 0; i < maxJoinFailures; i++ {
		if _, err := j.Claim("9.9.9.9", "BCDF-GHJK", "m", "9.9.9.9"); err != ErrJoinInvalid {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	// Auch der richtige Code hilft dieser Adresse jetzt nicht; die Sperre hängt nur an der Adresse.
	if _, err := j.Claim("9.9.9.9", pair, "m", "9.9.9.9"); !errors.Is(err, ErrJoinLocked) {
		t.Fatalf("locked address with right code: %v", err)
	}
	if _, err := j.Claim("9.9.9.9", "BCDF-GHJK", "m", "9.9.9.9"); !errors.Is(err, ErrJoinLocked) {
		t.Fatalf("locked address with wrong code: %v", err)
	}
	// Eine andere Adresse ist nicht betroffen, die Sitzung auch nicht.
	if _, err := j.Claim("8.8.8.8", pair, "m", "8.8.8.8"); err != nil {
		t.Fatalf("other address: %v", err)
	}
	clock.t = clock.t.Add(joinFailureWindow + time.Second)
	if _, err := j.Claim("9.9.9.9", "BCDF-GHJK", "m", "9.9.9.9"); err != ErrJoinInvalid {
		t.Fatalf("after the window: %v", err)
	}
}

func TestJoinBusyAddressAnswersTheSameForValidAndInvalidCodes(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	for i := 0; i < maxDevicePerClient; i++ {
		if _, _, err := startOpen(st, "7.7.7.7"); err != nil {
			t.Fatal(err)
		}
	}
	pair, _ := j.Create("person:2")
	_, errValid := j.Claim("7.7.7.7", pair, "m", "7.7.7.7")
	_, errInvalid := j.Claim("7.7.7.7", "BCDF-GHJK", "m", "7.7.7.7")
	if !errors.Is(errValid, ErrDeviceBusy) || errValid != errInvalid {
		t.Fatalf("valid=%v invalid=%v", errValid, errInvalid)
	}
	// Die Sitzung ist durch den Fehlschlag nicht verbraucht.
	if _, err := j.Claim("6.6.6.6", pair, "m", "6.6.6.6"); err != nil {
		t.Fatal(err)
	}
}

func startOpen(st *Store, client string) (DeviceStart, int, error) {
	s, err := st.Device().Start(client, "other", client)
	return s, 0, err
}

func TestJoinSessionAttemptsAreCapped(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	pair, _ := j.Create("person:2")
	if _, err := j.Claim("a", pair, "m", "a"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxJoinSessionTries+2; i++ {
		j.Claim("evil-"+string(rune('a'+i)), pair, "m", "x")
	}
	v := j.View("person:2")
	if v.Machine != "m" || v.Conflicts == 0 {
		t.Fatalf("view %+v", v)
	}
}

func TestJoinSessionsAreCapped(t *testing.T) {
	st, _ := pairFixture(t)
	j := st.Join()
	for i := 0; i < maxJoinSessions+10; i++ {
		if _, err := j.Create("person:" + string(rune('A'+i%26)) + strings.Repeat("x", i/26)); err != nil {
			t.Fatal(err)
		}
	}
	if n := j.Open(); n > maxJoinSessions {
		t.Fatalf("%d sessions", n)
	}
}
