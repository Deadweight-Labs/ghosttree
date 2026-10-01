package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openCodesStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "codes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func expireCodes(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE account_codes SET expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapCodeOnlyOnEmptyInstanceAndStoredHashed(t *testing.T) {
	s := openCodesStore(t)
	code, ok, err := s.EnsureBootstrapCode()
	if err != nil || !ok || code == "" {
		t.Fatalf("code=%q ok=%v err=%v", code, ok, err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT code_hash FROM account_codes WHERE kind='bootstrap'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == code || stored != hashToken(code) {
		t.Fatalf("plaintext stored: %q", stored)
	}
	// Ein Neustart ersetzt den unbenutzten Code.
	second, _, _ := s.EnsureBootstrapCode()
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "s", Code: code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("replaced code still works: %v", err)
	}
	a, outcome, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "s", Name: "robin", Email: "r@x", Code: second})
	if err != nil || outcome != LoginBootstrapped || !a.Admin || a.Name != "robin" || a.Email != "r@x" {
		t.Fatalf("a=%+v outcome=%s err=%v", a, outcome, err)
	}
	// Einmalig, und mit Konto gibt es keinen neuen Code mehr.
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "other", Code: second}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("reuse: %v", err)
	}
	if _, ok, _ := s.EnsureBootstrapCode(); ok {
		t.Fatal("bootstrap code issued on a non-empty instance")
	}
	// Die Identität meldet danach ohne Code an.
	again, outcome, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "s"})
	if err != nil || outcome != LoginExisting || again.ID != a.ID {
		t.Fatalf("again=%+v outcome=%s err=%v", again, outcome, err)
	}
}

func TestBootstrapCodeIsDeadOnceAnAccountExists(t *testing.T) {
	s := openCodesStore(t)
	code, _, _ := s.EnsureBootstrapCode()
	if _, err := s.AddPerson("early"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "s", Code: code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.BootstrapLocal(code, "x"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("local err=%v", err)
	}
}

func TestClaimCodeBindsExistingAccountOnceAndExpires(t *testing.T) {
	s := openCodesStore(t)
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	code, ttl, err := s.CreateAccountCode(CodeClaim, "robin")
	if err != nil || ttl != ClaimCodeTTL {
		t.Fatalf("err=%v ttl=%v", err, ttl)
	}
	a, outcome, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "robin-sub", Email: "r@x", Code: code})
	if err != nil || outcome != LoginClaimed || a.ID != "person:1" || a.Email != "r@x" {
		t.Fatalf("a=%+v outcome=%s err=%v", a, outcome, err)
	}
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "thief", Code: code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second use: %v", err)
	}
	// Ein Konto mit Identität bekommt keinen Claim-Code mehr.
	if _, _, err := s.CreateAccountCode(CodeClaim, "robin"); !errors.Is(err, ErrAccountHasIdentity) {
		t.Fatalf("err=%v", err)
	}
	// Und ein schon ausgestellter Code kann ein verbundenes Konto nicht umhängen.
	if _, err := s.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	c1, _, _ := s.CreateAccountCode(CodeClaim, "alice")
	c2, _, _ := s.CreateAccountCode(CodeClaim, "alice")
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "a1", Code: c1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "a2", Code: c2}); !errors.Is(err, ErrAccountHasIdentity) {
		t.Fatalf("rebind: %v", err)
	}
	// Der gescheiterte Versuch verbraucht den Code nicht.
	if kind := s.CodeKindFor(c2); kind != CodeClaim {
		t.Fatalf("failed attempt consumed the code, kind=%q", kind)
	}

	if _, err := s.AddPerson("carol"); err != nil {
		t.Fatal(err)
	}
	old, _, _ := s.CreateAccountCode(CodeClaim, "carol")
	expireCodes(t, s)
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "c", Code: old}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired: %v", err)
	}
}

func TestUnknownIdentityWithoutCodeIsRejected(t *testing.T) {
	s := openCodesStore(t)
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "stranger"}); !errors.Is(err, ErrNoAccountForIdentity) {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "stranger", Code: "nonsense"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("err=%v", err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM persons`).Scan(&n)
	if n != 1 {
		t.Fatalf("an account was created: %d", n)
	}
}

func TestLoginLinkIsSingleUseExpiresAndNeedsActiveAccount(t *testing.T) {
	s := openCodesStore(t)
	if _, err := s.AddPerson("robin"); err != nil {
		t.Fatal(err)
	}
	code, _, err := s.CreateAccountCode(CodeLogin, "robin")
	if err != nil {
		t.Fatal(err)
	}
	if a, err := s.RedeemLoginLink(code); err != nil || a.Name != "robin" {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if _, err := s.RedeemLoginLink(code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("reuse: %v", err)
	}
	// Ein Claim-Code ist kein Login-Link und umgekehrt.
	claim, _, _ := s.CreateAccountCode(CodeClaim, "robin")
	if _, err := s.RedeemLoginLink(claim); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("claim as link: %v", err)
	}
	link, _, _ := s.CreateAccountCode(CodeLogin, "robin")
	if _, _, err := s.LoginIdentity(IdentityLogin{Issuer: "i", Subject: "s", Code: link}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link as claim: %v", err)
	}
	expireCodes(t, s)
	if _, err := s.RedeemLoginLink(link); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired: %v", err)
	}
	s.db.Exec(`UPDATE persons SET state='disabled'`)
	if _, _, err := s.CreateAccountCode(CodeLogin, "robin"); err == nil {
		t.Fatal("code issued for a disabled account")
	}
}
