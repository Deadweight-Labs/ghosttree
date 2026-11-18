package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A token that authenticates gets a last use and starts the tracking clock;
// before that the column was never written and every token read "never used".
func TestAuthenticatingATokenRecordsItsLastUse(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "use.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	if st.TokenUseTrackingSince() != "" {
		t.Fatal("tracking claims to run before any use")
	}
	token, info, err := st.CreateToken("anna", TokenSpec{Label: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastUsedAt != "" {
		t.Fatalf("fresh token already used: %q", info.LastUsedAt)
	}
	principal, ok := st.AuthenticatePrincipal(token)
	if !ok {
		t.Fatal("token does not authenticate")
	}
	st.TouchToken(principal.TokenID)
	// In runtime mode the write is queued behind the request.
	var got TokenInfo
	for i := 0; i < 200; i++ {
		if got, err = st.TokenByID(info.ID); err != nil || got.LastUsedAt != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || got.LastUsedAt == "" {
		t.Fatalf("last use after authenticating = %q, %v", got.LastUsedAt, err)
	}
	if st.TokenUseTrackingSince() == "" {
		t.Fatal("tracking start was not recorded")
	}
	// A failed login records nothing for the other token.
	_, other, _ := st.CreateToken("anna", TokenSpec{Label: "other"})
	if g, _ := st.TokenByID(other.ID); g.LastUsedAt != "" {
		t.Fatalf("unused token has a last use: %q", g.LastUsedAt)
	}
}

// REQ-685 AC-4: the last use of a token follows the beat of the collector. A device that sends every 30 s reads
// "used a minute ago" at most; the interval of the write is not longer than that, and a call after the interval
// writes again (a call inside it does not).
func TestLastUseOfATokenFollowsTheCollectorBeat(t *testing.T) {
	if tokenTouchEvery > time.Minute {
		t.Fatalf("a token is written every %s: a collector that sends every 30 s would read as idle for that long", tokenTouchEvery)
	}
	st, err := Open(filepath.Join(t.TempDir(), "beat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddPerson("anna"); err != nil {
		t.Fatal(err)
	}
	token, info, err := st.CreateToken("anna", TokenSpec{Label: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := st.AuthenticatePrincipal(token)
	waitUse := func(different string) string {
		t.Helper()
		var got TokenInfo
		for i := 0; i < 300; i++ {
			if got, err = st.TokenByID(info.ID); err == nil && got.LastUsedAt != "" && got.LastUsedAt != different {
				return got.LastUsedAt
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("last use stayed %q (%v)", got.LastUsedAt, err)
		return ""
	}
	st.TouchToken(principal.TokenID)
	first := waitUse("")
	// Inside the interval nothing is written.
	st.TouchToken(principal.TokenID)
	time.Sleep(1100 * time.Millisecond)
	if got, _ := st.TokenByID(info.ID); got.LastUsedAt != first {
		t.Fatalf("a call inside the interval wrote %q over %q", got.LastUsedAt, first)
	}
	// After it the next call writes, so the page follows the device.
	if st.bookkeeper != nil {
		st.bookkeeper.mu.Lock()
		st.bookkeeper.tokenTouched[principal.TokenID] = time.Now().Add(-tokenTouchEvery - time.Second)
		st.bookkeeper.mu.Unlock()
	}
	st.TouchToken(principal.TokenID)
	if second := waitUse(first); second <= first {
		t.Fatalf("last use after the interval = %q, not after %q", second, first)
	}
}
