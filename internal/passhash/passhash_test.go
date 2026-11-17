package passhash

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("not a PHC argon2id string: %s", h)
	}
	if strings.Contains(h, "correct") {
		t.Fatal("hash contains the password")
	}
	if !Verify("correct horse battery", h) || Verify("correct horse batterz", h) || Verify("", h) {
		t.Fatal("verify decides wrong")
	}
	h2, _ := Hash("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes of one password are equal: the salt is missing")
	}
	if NeedsRehash(h) {
		t.Fatal("a fresh hash needs no rehash")
	}
}

func TestBounds(t *testing.T) {
	if _, err := Hash("short"); err != ErrTooShort {
		t.Fatalf("short: %v", err)
	}
	if _, err := Hash(strings.Repeat("x", MaxLength+1)); err != ErrTooLong {
		t.Fatalf("long: %v", err)
	}
	if Check("ünïcödé-pässwört") != nil {
		t.Fatal("unicode password refused")
	}
}

func TestDamagedHashNeverVerifies(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "$bcrypt$x$y$z$w$v"} {
		if Verify("whatever it is", h) {
			t.Errorf("verified against %q", h)
		}
	}
}

func TestOldParametersAskForRehash(t *testing.T) {
	if !NeedsRehash("$argon2id$v=19$m=1024,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("weaker parameters must ask for a rehash")
	}
}
