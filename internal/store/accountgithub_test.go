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

// A verified GitHub number belongs to one account (security review 9): linking
// it moves it, and numbers two accounts already shared drop back to the login.
func TestGitHubNumberBelongsToOneAccount(t *testing.T) {
	s := orgStore(t, "robin", "anna", "carl")
	if err := s.SetAccountGitHub("person:1", "robin-gh", 42); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountGitHub("person:2", "robin-gh", 42); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.AccountGitHub("person:1"); l.ID != 0 || l.Login != "robin-gh" {
		t.Fatalf("the former holder kept the number: %+v", l)
	}
	if l, _ := s.AccountGitHub("person:2"); l.ID != 42 {
		t.Fatalf("the new holder: %+v", l)
	}
	if _, err := s.db.Exec(`INSERT INTO account_github(account_id, login, github_id, linked_at) VALUES(3,'x',42,'')`); err == nil {
		t.Fatal("the index let a second account hold the number")
	}
	// An instance from before the index, with a shared number.
	if _, err := s.db.Exec(`DROP INDEX account_github_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO account_github(account_id, login, github_id, linked_at) VALUES(3,'x',42,'')`); err != nil {
		t.Fatal(err)
	}
	if err := ensureGitHubIDUnique(s.db); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM account_github WHERE github_id=42`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d accounts still verified by a shared number", n)
	}
}

// Review of REQ-623, point 3: GitHub hands a freed login to somebody else; the
// older link to it is no longer verified once the new owner links it.
func TestAFreedGitHubLoginIsVerifiedForItsNewOwnerOnly(t *testing.T) {
	s := orgStore(t, "alice", "bob")
	if err := s.SetAccountGitHub("person:1", "Octo", 11); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountGitHub("person:2", "octo", 22); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.AccountGitHub("person:1"); l.ID != 0 {
		t.Fatalf("the old link stayed verified: %+v", l)
	}
	if id, ok := verifiedGitHubOwnerTx(s.db, "github.com/octo/x"); !ok || id != 2 {
		t.Fatalf("verified owner = %d %v", id, ok)
	}
	if _, err := s.db.Exec(`UPDATE account_github SET github_id=33 WHERE account_id=1`); err == nil {
		t.Fatal("the index let two accounts be verified for one login")
	}
}
