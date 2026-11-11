package gitmsg

import (
	"strings"
	"testing"
)

// Dieselbe Nachricht in der Form des Transkripts und in der Form, in der GitHub sie
// liefert (Titel und Rumpf getrennt, Co-authored-by entfernt), ergibt dieselben Schlüssel.
func TestKeysAgreeOnBothSidesOfTheSameMessage(t *testing.T) {
	transcript := "fix(web):  Titel   mit Leerraum \r\n\r\n\r\nRumpf, Zeile eins.\n- Punkt \"zwei\"  \n\nCo-Authored-By: A B <a@b.c>\n"
	title, body := "fix(web): Titel mit Leerraum", "Rumpf, Zeile eins.\n- Punkt \"zwei\""
	a, b := Of(transcript), OfParts(title, body)
	if a != b {
		t.Fatalf("transcript %+v, github %+v", a, b)
	}
	if !Valid(a.Title) || !Valid(a.Full) {
		t.Fatalf("keys are not valid: %+v", a)
	}
}

func TestKeysDifferWhenTheMessageDiffers(t *testing.T) {
	a, b := Of("fix: a\n\nbody one"), Of("fix: a\n\nbody two")
	if a.Title != b.Title {
		t.Fatal("same title, same title key")
	}
	if a.Full == b.Full {
		t.Fatal("different bodies, different full keys")
	}
	if Of("fix: a").Title == Of("fix: b").Title {
		t.Fatal("different titles")
	}
}

// Ein Titel ohne Rumpf hat keinen vollen Schlüssel: der Titel sagt alles, und eine
// Gegenseite ohne Rumpf (noch nicht synchronisiert) bleibt vergleichbar.
func TestKeysOfATitleOnlyMessage(t *testing.T) {
	k := Of("fix: only a title\n")
	if k.Title == "" || k.Full != "" {
		t.Fatalf("%+v", k)
	}
	if k := Of("Co-authored-by: A <a@b.c>"); k.Full != "" {
		t.Fatalf("a trailer alone is no body: %+v", k)
	}
	if k := Of("  \n\t\n"); k != (Keys{}) {
		t.Fatalf("empty: %+v", k)
	}
	if k := Of("... --- !!!"); k != (Keys{}) {
		t.Fatalf("no letters: %+v", k)
	}
}

// Die Gegenseite kappt den Titel (200 Zeichen) und den Rumpf (6000 Zeichen); der
// Schlüssel hängt nicht an dem, was hinter der Kappung steht.
func TestKeysIgnoreWhatTheOtherSideTruncates(t *testing.T) {
	long := strings.Repeat("wort ", 80) // 400 Zeichen
	full := Of(long + "\n\n" + strings.Repeat("rumpf ", 2000))
	cut := OfParts(string([]rune(long)[:200]), string([]rune(strings.Repeat("rumpf ", 2000))[:6000]))
	if full.Title != cut.Title {
		t.Fatal("the title key survives the cut at 200")
	}
	if full.Full != cut.Full {
		t.Fatal("the full key survives the cut at 6000")
	}
}

func TestKeysHandleUnicode(t *testing.T) {
	if Of("fix: Ä-Test").Title != Of("fix: ä test").Title {
		t.Fatal("case and punctuation do not matter")
	}
	if Of("fix: größe").Title == Of("fix: grosse").Title {
		t.Fatal("different letters stay different")
	}
}

func TestValid(t *testing.T) {
	for in, want := range map[string]bool{
		Of("x").Title:                     true,
		"":                                false,
		"zz" + strings.Repeat("0", 30):    false,
		strings.Repeat("A", 32):           false,
		strings.Repeat("a", 31):           false,
		strings.Repeat("a", 32) + "extra": false,
	} {
		if Valid(in) != want {
			t.Errorf("Valid(%q) = %v", in, !want)
		}
	}
}
