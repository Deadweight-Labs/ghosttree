package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// legacyAccountsDB baut eine Datenbank, wie das Binary vor den Konten sie
// hinterlassen hat: persons ohne die neuen Spalten, keine api_tokens.
func legacyAccountsDB(t *testing.T, tokens map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE persons(
		id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
		token_hash TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"robin", "alice", "bob"} {
		if _, err := db.Exec(`INSERT INTO persons(name, token_hash, created_at) VALUES(?,?,?)`,
			name, hashToken(tokens[name]), "2026-08-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLegacyTokensMigrateToTheirOwnAccounts(t *testing.T) {
	tokens := map[string]string{"robin": "tok-robin", "alice": "tok-alice", "bob": "tok-bob"}
	path := legacyAccountsDB(t, tokens)

	// Zweimal öffnen: die Migration muss wiederholbar sein und darf nichts doppeln.
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		for name, token := range tokens {
			p, ok := s.AuthenticatePrincipal(token)
			if !ok || p.Label != name || p.TokenKind != "legacy" {
				t.Fatalf("open %d: %s token -> %+v ok=%v", i, name, p, ok)
			}
		}
		var n int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM api_tokens`).Scan(&n); err != nil || n != 3 {
			t.Fatalf("open %d: api_tokens rows = %d err=%v", i, n, err)
		}
		a, err := s.AccountByName("robin")
		if err != nil || !a.Admin || a.State != "active" || a.ID != "person:1" {
			t.Fatalf("robin = %+v err=%v", a, err)
		}
		if b, _ := s.AccountByName("bob"); b.Admin {
			t.Fatalf("only the first account becomes admin: %+v", b)
		}
		s.Close()
	}
}

func TestLegacyMigrationKeepsPersonsTokenHashForRollback(t *testing.T) {
	path := legacyAccountsDB(t, map[string]string{"robin": "r", "alice": "a", "bob": "b"})
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var hash string
	if err := s.DB().QueryRow(`SELECT token_hash FROM persons WHERE name='alice'`).Scan(&hash); err != nil || hash != hashToken("a") {
		t.Fatalf("persons.token_hash changed: %q err=%v", hash, err)
	}
}

func TestMigrationDoesNotReviveRevokedTokenOrRegrantAdmin(t *testing.T) {
	path := legacyAccountsDB(t, map[string]string{"robin": "r", "alice": "a", "bob": "b"})
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.DB().QueryRow(`SELECT id FROM api_tokens WHERE token_hash=?`, hashToken("a")).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE persons SET is_admin=0 WHERE name='robin'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.AuthenticatePrincipal("a"); ok {
		t.Fatal("revoked legacy token authenticates again after reopen")
	}
	if a, _ := s.AccountByName("robin"); a.Admin {
		t.Fatal("admin flag was granted again after reopen")
	}
}

func TestMigrationPicksUpPersonAddedByOlderBinary(t *testing.T) {
	path := legacyAccountsDB(t, map[string]string{"robin": "r", "alice": "a", "bob": "b"})
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Ein zurückgerolltes Binary schreibt nur persons.
	if _, err := s.DB().Exec(`INSERT INTO persons(name, token_hash, created_at) VALUES('carol',?,?)`, hashToken("c"), now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, ok := s.AuthenticatePrincipal("c"); !ok || p.Label != "carol" {
		t.Fatalf("carol = %+v ok=%v", p, ok)
	}
}

func TestAddPersonCreatesAccountWithLegacyToken(t *testing.T) {
	s := openTest(t)
	token, err := s.AddPerson("alice")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.AuthenticatePrincipal(token)
	if !ok || p.ID != "person:1" || p.Label != "alice" || p.TokenKind != "legacy" || p.TokenID == 0 {
		t.Fatalf("principal = %+v ok=%v", p, ok)
	}
	var hash string
	if err := s.DB().QueryRow(`SELECT token_hash FROM api_tokens`).Scan(&hash); err != nil || hash == token {
		t.Fatalf("token must be stored hashed: %q err=%v", hash, err)
	}
}

func TestCreateCheckAndRevokeToken(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddAccount("alice", "alice@example.com", false); err != nil {
		t.Fatal(err)
	}
	token, info, err := s.CreateToken("alice", TokenSpec{Label: "laptop", Machine: "laptop-a"})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.DB().QueryRow(`SELECT token_hash FROM api_tokens WHERE id=?`, info.ID).Scan(&stored); err != nil || stored == token || stored != hashToken(token) {
		t.Fatalf("stored = %q err=%v", stored, err)
	}
	p, ok := s.AuthenticatePrincipal(token)
	if !ok || p.Label != "alice" || p.TokenID != info.ID || p.Machine != "laptop-a" || p.TokenKind != "cli" {
		t.Fatalf("principal = %+v ok=%v", p, ok)
	}
	// Jedes Token gehört genau einem Konto: zweites Konto, eigenes Token.
	if _, err := s.AddAccount("bob", "", false); err != nil {
		t.Fatal(err)
	}
	bobToken, _, err := s.CreateToken("bob", TokenSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.AuthenticatePrincipal(bobToken); p.Label != "bob" {
		t.Fatalf("bob token -> %+v", p)
	}
	if err := s.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(info.ID); err != nil {
		t.Fatalf("second revoke must be a no-op: %v", err)
	}
	if _, ok := s.AuthenticatePrincipal(token); ok {
		t.Fatal("revoked token authenticates")
	}
	if _, ok := s.AuthenticatePrincipal(bobToken); !ok {
		t.Fatal("revoking one token must not touch another")
	}
	tokens, err := s.ListTokens("alice")
	if err != nil || len(tokens) != 1 || tokens[0].RevokedAt == "" {
		t.Fatalf("tokens = %+v err=%v", tokens, err)
	}
	if err := s.RevokeToken(9999); err == nil {
		t.Fatal("revoking an unknown token must fail")
	}
}

func TestExpiredAndDisabledTokensAreRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddAccount("alice", "", false); err != nil {
		t.Fatal(err)
	}
	live, _, err := s.CreateToken("alice", TokenSpec{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	old, info, err := s.CreateToken("alice", TokenSpec{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AuthenticatePrincipal(old); !ok {
		t.Fatal("unexpired token rejected")
	}
	if _, err := s.DB().Exec(`UPDATE api_tokens SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AuthenticatePrincipal(old); ok {
		t.Fatal("expired token authenticates")
	}
	if _, err := s.DB().Exec(`UPDATE persons SET state='disabled' WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AuthenticatePrincipal(live); ok {
		t.Fatal("token of a disabled account authenticates")
	}
	if _, _, err := s.CreateToken("alice", TokenSpec{}); err == nil {
		t.Fatal("token issued for a disabled account")
	}
}

func TestUnknownAndEmptyTokenAreRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddAccount("alice", "", false); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "nope"} {
		if _, ok := s.AuthenticatePrincipal(tok); ok {
			t.Fatalf("token %q authenticates", tok)
		}
	}
}

func TestAddPersonsColumnToleratesDuplicate(t *testing.T) {
	s := openTest(t)
	if err := addPersonsColumn(s.DB(), "email", `TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("duplicate column must count as success: %v", err)
	}
	if err := addPersonsColumn(s.DB(), "extra", `)(`); err == nil {
		t.Fatal("other ALTER errors must surface")
	}
}

func TestAdminMigrationWaitsForFirstPerson(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount("first", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount("second", "", false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if a, _ := s.AccountByName("first"); !a.Admin {
		t.Fatalf("first person must become admin: %+v", a)
	}
	if a, _ := s.AccountByName("second"); a.Admin {
		t.Fatalf("second person must not: %+v", a)
	}
}

func TestPrincipalValidFollowsTokenAndAccount(t *testing.T) {
	s := openTest(t)
	s.AddAccount("alice", "", false)
	token, info, _ := s.CreateToken("alice", TokenSpec{})
	p, _ := s.AuthenticatePrincipal(token)
	if !s.PrincipalValid(p) {
		t.Fatal("fresh principal invalid")
	}
	if s.PrincipalValid(Principal{ID: "person:2", TokenID: info.ID}) {
		t.Fatal("token of another account must not validate")
	}
	s.RevokeToken(info.ID)
	if s.PrincipalValid(p) {
		t.Fatal("revoked token still valid")
	}
}
