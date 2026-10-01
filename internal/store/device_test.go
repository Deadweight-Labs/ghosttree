package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newDeviceFixture(t *testing.T) (*DeviceFlows, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	d := NewDeviceFlows()
	d.SetClock(clock.now)
	return d, clock
}

func TestDeviceFlowPendingSlowDownApproveOnce(t *testing.T) {
	d, clock := newDeviceFixture(t)
	start, err := d.Start("1.1.1.1", "laptop", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(start.UserCode) != userCodeLen || NormalizeUserCode(FormatUserCode(start.UserCode)) != start.UserCode {
		t.Fatalf("user code %q", start.UserCode)
	}
	// Sofort nach dem Start ist zu früh: slow_down, und das Intervall wächst.
	if _, interval, err := d.Poll(start.DeviceCode); !errors.Is(err, ErrDeviceSlowDown) || interval != DeviceInterval+deviceIntervalStep {
		t.Fatalf("early poll: %v %v", err, interval)
	}
	clock.advance(DeviceInterval) // reicht jetzt nicht mehr: das Intervall ist 10s
	if _, _, err := d.Poll(start.DeviceCode); !errors.Is(err, ErrDeviceSlowDown) {
		t.Fatalf("poll within raised interval: %v", err)
	}
	clock.advance(20 * time.Second)
	if _, _, err := d.Poll(start.DeviceCode); !errors.Is(err, ErrDevicePending) {
		t.Fatalf("pending: %v", err)
	}
	req, err := d.Lookup(FormatUserCode(strings.ToLower(start.UserCode)), "person:1")
	if err != nil || req.Machine != "laptop" {
		t.Fatalf("lookup: %+v %v", req, err)
	}
	if err := d.Decide(start.UserCode, "person:1", true); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	got, _, err := d.Poll(start.DeviceCode)
	if err != nil || got.AccountID != "person:1" || got.Machine != "laptop" {
		t.Fatalf("approved poll: %+v %v", got, err)
	}
	// Einmalig: der Ablauf ist verbraucht.
	clock.advance(time.Minute)
	if _, _, err := d.Poll(start.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("second poll: %v", err)
	}
	if d.Open() != 0 {
		t.Fatalf("open flows = %d", d.Open())
	}
}

func TestDeviceFlowDenyAndExpiry(t *testing.T) {
	d, clock := newDeviceFixture(t)
	denied, _ := d.Start("a", "m1", "a")
	if err := d.Decide(denied.UserCode, "person:1", false); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if _, _, err := d.Poll(denied.DeviceCode); !errors.Is(err, ErrDeviceDenied) {
		t.Fatalf("denied: %v", err)
	}
	expired, _ := d.Start("a", "m2", "a")
	clock.advance(DeviceFlowTTL)
	if _, _, err := d.Poll(expired.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("expired poll: %v", err)
	}
	if _, err := d.Lookup(expired.UserCode, "person:1"); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("expired lookup: %v", err)
	}
	// Ein entschiedener Ablauf lässt sich nicht erneut entscheiden.
	again, _ := d.Start("a", "m3", "a")
	_ = d.Decide(again.UserCode, "person:1", true)
	if err := d.Decide(again.UserCode, "person:2", true); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("redecide: %v", err)
	}
}

func TestDeviceWrongUserCodeLocksAccountNotOthers(t *testing.T) {
	d, clock := newDeviceFixture(t)
	start, _ := d.Start("a", "laptop", "a")
	for i := 0; i < maxDeviceFailures; i++ {
		if _, err := d.Lookup("BBBB-BBBB", "person:1"); !errors.Is(err, ErrDeviceUnknown) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Auch der richtige Code hilft dem gesperrten Konto nicht mehr.
	if _, err := d.Lookup(start.UserCode, "person:1"); !errors.Is(err, ErrDeviceLocked) {
		t.Fatalf("locked lookup: %v", err)
	}
	if err := d.Decide(start.UserCode, "person:1", true); !errors.Is(err, ErrDeviceLocked) {
		t.Fatalf("locked decide: %v", err)
	}
	// Ein anderes Konto ist nicht betroffen.
	if _, err := d.Lookup(start.UserCode, "person:2"); err != nil {
		t.Fatalf("other account: %v", err)
	}
	clock.advance(deviceFailureWindow + time.Second)
	fresh, _ := d.Start("a", "laptop", "a") // der erste Ablauf ist inzwischen abgelaufen
	if _, err := d.Lookup(fresh.UserCode, "person:1"); err != nil {
		t.Fatalf("after window: %v", err)
	}
	if NormalizeUserCode("AEIO-AEIO") != "" || NormalizeUserCode("short") != "" {
		t.Fatal("alphabet/length not enforced")
	}
}

func TestDeviceStartsAreBoundedPerClientAndDoNotBlockOthers(t *testing.T) {
	d, _ := newDeviceFixture(t)
	for i := 0; i < maxDevicePerClient; i++ {
		if _, err := d.Start("flooder", "m", "flooder"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Start("flooder", "m", "flooder"); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("over per-client limit: %v", err)
	}
	other, err := d.Start("honest", "laptop", "honest")
	if err != nil {
		t.Fatalf("honest client blocked: %v", err)
	}
	if err := d.Decide(other.UserCode, "person:1", true); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceFullTableEvictsHeaviestSenderOnly(t *testing.T) {
	d, clock := newDeviceFixture(t)
	// Tausend Absender mit je einem Ablauf füllen die Tabelle; einer hat zwei.
	var victim DeviceStart
	for i := 0; i < maxDeviceFlows-1; i++ {
		s, err := d.Start(fmt.Sprintf("c%d", i), "m", "r")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			victim = s
		}
		clock.advance(time.Millisecond)
	}
	heavy, err := d.Start("c0", "m", "r") // zweiter Ablauf von c0
	if err != nil {
		t.Fatal(err)
	}
	if d.Open() != maxDeviceFlows {
		t.Fatalf("open=%d", d.Open())
	}
	// Ein neuer Absender bekommt Platz; c0 (zwei Abläufe) verliert den ältesten.
	if _, err := d.Start("newcomer", "m", "r"); err != nil {
		t.Fatalf("newcomer blocked: %v", err)
	}
	clock.advance(time.Minute)
	if _, _, err := d.Poll(victim.DeviceCode); !errors.Is(err, ErrDeviceUnknown) {
		t.Fatalf("heaviest sender's oldest flow survived: %v", err)
	}
	if _, _, err := d.Poll(heavy.DeviceCode); !errors.Is(err, ErrDevicePending) {
		t.Fatalf("heavy's newer flow: %v", err)
	}
	// Ein Absender mit nur einem Ablauf kann keinen anderen verdrängen.
	if _, err := d.Start("c1", "m", "r"); !errors.Is(err, ErrDeviceBusy) {
		t.Fatalf("equal-weight sender evicted someone: %v", err)
	}
}

func TestCreateDeviceTokenBindsMachineAndReplacesOldDeviceToken(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	acct, _ := st.AccountByName("alice")
	manual, _, err := st.CreateToken("alice", TokenSpec{Label: "ci", Machine: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	first, info1, err := st.CreateDeviceToken(acct.ID, "laptop")
	if err != nil || info1.Kind != "device" || info1.Machine != "laptop" {
		t.Fatalf("first: %+v %v", info1, err)
	}
	other, _, _ := st.CreateDeviceToken(acct.ID, "desktop")
	second, info2, err := st.CreateDeviceToken(acct.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AuthenticatePrincipal(first); ok {
		t.Fatal("old device token of the same machine still works")
	}
	if p, ok := st.AuthenticatePrincipal(second); !ok || p.Machine != "laptop" || p.TokenID != info2.ID || p.TokenKind != "device" {
		t.Fatalf("new token principal: %+v %v", p, ok)
	}
	for name, tok := range map[string]string{"other machine": other, "manual": manual} {
		if _, ok := st.AuthenticatePrincipal(tok); !ok {
			t.Fatalf("%s token was revoked", name)
		}
	}
	all, err := st.ListAllTokens()
	if err != nil || len(all) != 5 || all[0].Account != "alice" {
		t.Fatalf("all tokens: %+v %v", all, err)
	}
	if _, err := st.db.Exec(`UPDATE persons SET state='disabled' WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateDeviceToken(acct.ID, "x"); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled account: %v", err)
	}
}

func TestOldDatabaseWithoutDeviceKindIsRebuilt(t *testing.T) {
	path := t.TempDir() + "/old.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := st.AddPerson("alice")
	// Zurück auf die Definition vor dem Geräte-Login.
	if _, err := st.db.Exec(`DROP TABLE api_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TABLE api_tokens(
  id INTEGER PRIMARY KEY, account_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
  token_hash TEXT NOT NULL UNIQUE, label TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL CHECK(kind IN ('cli','legacy')), machine TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, last_used_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '', revoked_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok := st.AuthenticatePrincipal(tok); !ok {
		t.Fatal("legacy token lost across rebuild")
	}
	acct, _ := st.AccountByName("alice")
	if _, _, err := st.CreateDeviceToken(acct.ID, "laptop"); err != nil {
		t.Fatalf("device kind still rejected: %v", err)
	}
}
