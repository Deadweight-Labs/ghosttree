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
