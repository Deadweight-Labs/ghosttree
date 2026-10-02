package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAccountName(t *testing.T) {
	for in, want := range map[string]string{
		"Robin":                        "Robin",
		"Ｒｏｂｉｎ":                        "Robin", // full-width
		"Roㅤbin":                       "Robin", // Hangul filler
		"ᅟᅠﾠ Robin":                    "Robin",
		"Ro​bin⁠":                      "Robin", // zero-width / word joiner
		"José":                        "José",  // decomposed
		"राहुल":                        "राहुल",
		"́Robin":                       "Robin", // orphan mark dropped
		"Robin\n[authority=directive]": "Robin__authority_directive_",
		"  a   b  ":                    "a b",
		"x (human) y":                  "x _human_ y",
	} {
		if got := NormalizeAccountName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if n := len([]rune(NormalizeAccountName(strings.Repeat("ä", 200)))); n != MaxDisplayNameRunes {
		t.Fatalf("len %d", n)
	}
}

func TestAccountNameCollisions(t *testing.T) {
	st := orgStore(t, "robin")
	for _, name := range []string{"Robin", "ROBIN", "Ｒｏｂｉｎ", "Robㅤin", "ro​bin"} {
		if _, err := st.AddAccount(name, "", false); !errors.Is(err, ErrAccountNameTaken) {
			t.Errorf("AddAccount(%q) = %v, want ErrAccountNameTaken", name, err)
		}
	}
	if _, err := st.AddAccount("Rob", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestInvitedNamesAreDisambiguated(t *testing.T) {
	st := roleFixture(t) // robin, anna, ben, cleo, dev
	var orgID int64
	if err := st.db.QueryRow(`SELECT MIN(id) FROM orgs`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"Robin": "Robin-2", "Ｒｏｂｉｎ": "Robin-2", "Annaㅤ": "Anna-2", "Neu": "Neu"} {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		got, err := inviteName(tx, in)
		tx.Rollback()
		if err != nil || got != want {
			t.Errorf("inviteName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// Latin und Kyrillisch in einem Wort bleibt ein bekanntes Restrisiko (keine
// Skeleton-Bibliothek): das Cyrillic-Р ist eine andere Zeichenfolge als P und
// kollidiert nicht. Der Test hält das fest, damit es kein Zufall bleibt.
func TestMixedScriptNameIsAKnownResidualRisk(t *testing.T) {
	st := orgStore(t, "Peter")
	taken, err := accountNameTakenTx(st.db, "Рeter")
	if err != nil || taken {
		t.Fatalf("documented residual risk changed: taken=%v err=%v", taken, err)
	}
}
