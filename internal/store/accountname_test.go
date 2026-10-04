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
		"Ro\u3164bin":                  "Robin", // Hangul filler
		"\u115f\u1160\uffa0 Robin":     "Robin",
		"Ro\u200bbin\u2060":            "Robin", // zero-width / word joiner
		"Jose\u0301":                   "José",  // decomposed
		"र\u093eह\u0941ल":              "र\u093eह\u0941ल",
		"\u0301Robin":                  "Robin", // orphan mark dropped
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
	for _, name := range []string{"Robin", "ROBIN", "Ｒｏｂｉｎ", "Rob\u3164in", "ro\u200bbin"} {
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
	for in, want := range map[string]string{"Robin": "Robin-2", "Ｒｏｂｉｎ": "Robin-2", "Anna\u3164": "Anna-2", "Neu": "Neu"} {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		got, err := inviteName(tx, in, 0)
		tx.Rollback()
		if err != nil || got != want {
			t.Errorf("inviteName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeAccountNameIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"e\u200d\u0301x", "Ro\u0334bin", "Ro\u0338bin", "a\u0301\u0302\u0303\u0304b", "\u0301\u200d\u0301a",
		"Ｒｏ\u3164\u0301bin", "Robịn", "ﬁne", "x\u200d\u200d\u0301\u0301\u0301y", "र\u093eह\u0941ल", "Jose\u0301",
	} {
		once := NormalizeAccountName(in)
		if twice := NormalizeAccountName(once); twice != once {
			t.Errorf("%q: f(x)=%q f(f(x))=%q", in, once, twice)
		}
	}
	// Der Joiner trennt Buchstabe und Mark: der Mark darf danach nicht hängen bleiben.
	if got := NormalizeAccountName("e\u200d\u0301x"); got != "\u00e9x" {
		t.Errorf("got %q", got)
	}
}

func TestCombiningMarksAreLimited(t *testing.T) {
	if got := NormalizeAccountName("a\u0301\u0302\u0303\u0304\u0305b"); got != "\u00e1\u0302\u0303b" {
		t.Errorf("more than two marks survived: %q", got)
	}
	for _, overlay := range []string{"\u0334", "\u0335", "\u0336", "\u0337", "\u0338"} {
		if got := NormalizeAccountName("Ro" + overlay + "bin"); got != "Robin" {
			t.Errorf("overlay %U survived: %q", []rune(overlay)[0], got)
		}
	}
}
