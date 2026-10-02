package store

import (
	"errors"
	"testing"
)

func principalOfToken(t *testing.T, s *Store, token string) Principal {
	t.Helper()
	p, ok := s.AuthenticatePrincipal(token)
	if !ok {
		t.Fatal("token does not authenticate")
	}
	return p
}

func TestRevokeOwnTokenReleasesAMachineItIntroduced(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	tok, _, err := s.CreateDeviceToken("person:1", "fresh-box")
	if err != nil {
		t.Fatal(err)
	}
	released, err := s.RevokeOwnToken(principalOfToken(t, s, tok))
	if err != nil || !released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	if _, _, err := s.CreateDeviceToken("person:1", "fresh-box"); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeOwnTokenOfARepeatedJoinKeepsTheMachineWithItsOwner(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	s.AddPerson("bob")
	if _, _, err := s.CreateDeviceToken("person:1", "laptop"); err != nil {
		t.Fatal(err)
	}
	again, _, err := s.CreateDeviceToken("person:1", "laptop") // the second ctx join
	if err != nil {
		t.Fatal(err)
	}
	released, err := s.RevokeOwnToken(principalOfToken(t, s, again)) // declined
	if err != nil || released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	if _, _, err := s.CreateDeviceToken("person:2", "laptop"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("another account took the name: %v", err)
	}
}

func TestRevokeOwnTokenOfAStolenTokenCannotFreeAMachineWithKnowledge(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	s.AddPerson("bob")
	stolen, _, err := s.CreateDeviceToken("person:1", "workstation")
	if err != nil {
		t.Fatal(err)
	}
	// The machine now has history behind it.
	if _, err := s.db.Exec(`INSERT INTO sessions(harness, external_id, machine, started_at, last_seen_at) VALUES('claude','s1','workstation','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	released, err := s.RevokeOwnToken(principalOfToken(t, s, stolen))
	if err != nil || released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	if _, _, err := s.CreateDeviceToken("person:2", "workstation"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("thief took the machine: %v", err)
	}
}

func TestRevokeOwnTokenKeepsTheMachineWhileAnotherTokenCarriesIt(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	first, _, _ := s.CreateDeviceToken("person:1", "duo")
	p := principalOfToken(t, s, first)
	// A second active token on the same machine (a CLI token tagged with it).
	if _, err := s.db.Exec(`INSERT INTO api_tokens(account_id, token_hash, label, kind, machine, created_at) VALUES(1,'h-other','x','device','duo','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if released, err := s.RevokeOwnToken(p); err != nil || released {
		t.Fatalf("released=%v err=%v", released, err)
	}
}

func TestRevokeOwnTokenTwiceAndForeignPrincipal(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	tok, _, _ := s.CreateDeviceToken("person:1", "once")
	p := principalOfToken(t, s, tok)
	if _, err := s.RevokeOwnToken(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeOwnToken(p); !errors.Is(err, ErrTokenNotActive) {
		t.Fatalf("second: %v", err)
	}
	foreign := p
	foreign.ID = "person:2"
	if _, err := s.RevokeOwnToken(foreign); !errors.Is(err, ErrTokenNotActive) {
		t.Fatalf("foreign: %v", err)
	}
}

func TestOrgNamesRejectControlCharacters(t *testing.T) {
	s := openTest(t)
	s.AddPerson("alice")
	for _, name := range []string{"Evil\x1b[2K", "tab\there", "nul\x00", "c1\u009b"} {
		if _, err := s.CreateOrg("person:1", name, ""); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateOrg(%q): %v", name, err)
		}
	}
	o, err := s.CreateOrg("person:1", "Fine Org", "fine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameOrg("person:1", o.ID, "Bad\x1b[31m", ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("RenameOrg: %v", err)
	}
}
