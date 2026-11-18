package store

import (
	"path/filepath"
	"testing"
	"time"
)

func idleAgo(d time.Duration) string { return time.Now().UTC().Add(-d).Format(time.RFC3339) }

// Ein Geräte-Token läuft ab, wenn es 90 Tage nicht benutzt wurde, und jede Nutzung
// verlängert es. Bestehende Tokens werden beim Einführen nicht sofort entwertet.
func TestDeviceTokenExpiresAfterIdleAndSlidesWithUse(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "idle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acct, err := st.AddAccount("mara", "", false)
	if err != nil {
		t.Fatal(err)
	}
	token, info, err := st.CreateDeviceToken(acct.ID, "mara-laptop")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := st.AuthenticatePrincipal(token)
	if !ok || !st.PrincipalValid(p) {
		t.Fatal("a fresh device token must work")
	}

	// Introduced into an instance with old tokens: the start of the idle clock is "now",
	// so a token made 200 days ago and never used still works.
	if _, err := st.db.Exec(`UPDATE api_tokens SET created_at=? WHERE id=?`, idleAgo(200*24*time.Hour), info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AuthenticatePrincipal(token); !ok {
		t.Fatal("an old token must not be cut off at once when the rule starts")
	}

	// Once the rule has been there for longer than the idle time, an unused token is dead.
	if _, err := st.db.Exec(`UPDATE org_state SET value=? WHERE key=?`, idleAgo(120*24*time.Hour), tokenIdleFromKey); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AuthenticatePrincipal(token); ok {
		t.Fatal("a device token unused for 90 days still works")
	}
	if st.PrincipalValid(p) {
		t.Fatal("a session of an idle device token is still valid")
	}

	// Use slides the end.
	if _, err := st.db.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=?`, idleAgo(24*time.Hour), info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AuthenticatePrincipal(token); !ok || !st.PrincipalValid(p) {
		t.Fatal("a device token used yesterday must work")
	}

	// A token with an explicit date is not touched by the idle rule.
	cli, cliInfo, err := st.CreateToken(acct.Name, TokenSpec{Label: "ci", ExpiresIn: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE api_tokens SET created_at=? WHERE id=?`, idleAgo(300*24*time.Hour), cliInfo.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AuthenticatePrincipal(cli); !ok {
		t.Fatal("a manual token with a date must work")
	}
}
