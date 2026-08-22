package store

import "testing"

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPersonRoundtrip(t *testing.T) {
	s := openTest(t)
	tok, err := s.AddPerson("robin")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}
	name, ok := s.Authenticate(tok)
	if !ok || name != "robin" {
		t.Errorf("Authenticate = %q, %v", name, ok)
	}
	if _, ok := s.Authenticate("deadbeef"); ok {
		t.Error("bogus token must not authenticate")
	}
	if _, err := s.AddPerson("robin"); err == nil {
		t.Error("duplicate person must error")
	}
}

func TestTouchMachine(t *testing.T) {
	s := openTest(t)
	s.TouchMachine("workstation-a")
	s.TouchMachine("workstation-a") // idempotent, updates last_seen
}
