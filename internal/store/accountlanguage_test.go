package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAccountLanguageIsSavedPerAccountAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lang.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AddAccount("alice", "", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddAccount("bob", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.AccountLanguage(a.ID); got != "" {
		t.Fatalf("a new account has no choice, got %q", got)
	}
	if err := s.SetAccountLanguage(a.ID, " DE "); err != nil {
		t.Fatal(err)
	}
	if got := s.AccountLanguage(a.ID); got != LanguageDE {
		t.Fatalf("alice chose de, got %q", got)
	}
	if got := s.AccountLanguage(b.ID); got != "" {
		t.Fatalf("bob must not follow alice, got %q", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.AccountLanguage(a.ID); got != LanguageDE {
		t.Fatalf("the choice must be stored on the account, got %q", got)
	}
	if err := s.SetAccountLanguage(a.ID, ""); err != nil || s.AccountLanguage(a.ID) != "" {
		t.Fatalf("an empty language takes the choice back: %v", err)
	}
}

func TestSetAccountLanguageRefusesUnknownLanguagesAndAccounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "lang.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.AddAccount("alice", "", false)
	if err := s.SetAccountLanguage(a.ID, "fr"); !errors.Is(err, ErrInvalidLanguage) {
		t.Fatalf("fr must be refused, got %v", err)
	}
	if err := s.SetAccountLanguage("person:9999", "de"); err == nil {
		t.Fatal("an unknown account must be refused")
	}
	if err := s.SetAccountLanguage("nonsense", "de"); err == nil {
		t.Fatal("a malformed id must be refused")
	}
	if got := s.AccountLanguage(a.ID); got != "" {
		t.Fatalf("a refused write must change nothing, got %q", got)
	}
}
