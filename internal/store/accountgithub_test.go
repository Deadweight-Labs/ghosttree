package store

import "testing"

func TestClearAccountGitHubRemovesOnlyThatAccountsLink(t *testing.T) {
	s := openTest(t)
	for _, n := range []string{"alice", "anna"} {
		if _, err := s.AddPerson(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAccountGitHub("person:1", "octocat", 583231); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountGitHub("person:2", "hubot", 7); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearAccountGitHub("person:1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AccountGitHub("person:1"); ok {
		t.Fatal("the link of the cleared account must be gone")
	}
	if l, ok := s.AccountGitHub("person:2"); !ok || l.Login != "hubot" {
		t.Fatalf("another account's link must stay: %+v %v", l, ok)
	}
	if err := s.ClearAccountGitHub("person:1"); err != nil {
		t.Fatalf("clearing an account without a link is no error: %v", err)
	}
	if err := s.ClearAccountGitHub("not-an-account"); err == nil {
		t.Fatal("an unknown account is refused")
	}
	// A link can be made again after it was cleared.
	if err := s.SetAccountGitHub("person:1", "octocat", 583231); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AccountGitHub("person:1"); !ok {
		t.Fatal("relinking after clearing")
	}
}
