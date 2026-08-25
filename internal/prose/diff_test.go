package prose

import (
	"strings"
	"testing"
)

func ops(changes []Change, op Op) []string {
	var out []string
	for _, c := range changes {
		if c.Op == op {
			out = append(out, c.Text)
		}
	}
	return out
}

// Der Normalfall, und der einzige, der zählt: ein Absatz von 500 Zeichen, in dem
// genau ein Satz umgeschrieben wurde. Ein Zeilendiff meldete hier den ganzen
// Absatz als weg und wieder da — der Leser bekäme dieselben zwei Prosablöcke,
// gegen die das hier gebaut wird.
func TestOneChangedSentenceInAParagraphIsTheOnlyChange(t *testing.T) {
	alt := "Die Identität ist das Paar aus Projekt und Pfad. Ein zweites Beschreiben ersetzt das erste — es gibt bewusst keine Fassungshistorie. Ausgeliefert wird nur die aktuelle."
	neu := "Die Identität ist das Paar aus Projekt und Pfad. Ein zweites Beschreiben ersetzt das erste — die verdrängte Fassung wandert in die Historie. Ausgeliefert wird nur die aktuelle."

	changes := Diff(alt, neu)

	weg, dazu := ops(changes, Removed), ops(changes, Added)
	if len(weg) != 1 || len(dazu) != 1 {
		t.Fatalf("genau ein Satz weg und einer dazu erwartet, got %d weg / %d dazu: %#v", len(weg), len(dazu), changes)
	}
	if !strings.Contains(weg[0], "bewusst keine Fassungshistorie") {
		t.Errorf("der entfernte Satz ist der falsche: %q", weg[0])
	}
	if !strings.Contains(dazu[0], "wandert in die Historie") {
		t.Errorf("der neue Satz ist der falsche: %q", dazu[0])
	}
	if unveraendert := ops(changes, Same); len(unveraendert) != 2 {
		t.Errorf("die beiden übrigen Sätze müssen unverändert bleiben, got %v", unveraendert)
	}
}

// Zwei gleiche Texte haben keine Änderung. Ohne das meldete die Historie bei
// jedem Umzug und jedem Neuschreiben desselben Textes eine Änderung.
func TestIdenticalTextsHaveNoChange(t *testing.T) {
	text := "Ein Satz. Und noch einer."
	for _, c := range Diff(text, text) {
		if c.Op != Same {
			t.Fatalf("kein Unterschied erwartet, got %#v", c)
		}
	}
}

// Ein Absatz, der dazukommt, ist ein Zusatz und kein Ersatz: nichts wird als
// entfernt gemeldet.
func TestAnAppendedParagraphIsPureAddition(t *testing.T) {
	alt := "Der erste Satz."
	neu := "Der erste Satz.\n\nEin ganz neuer Absatz."

	changes := Diff(alt, neu)
	if weg := ops(changes, Removed); len(weg) != 0 {
		t.Fatalf("beim Anhängen darf nichts wegfallen, got %v", weg)
	}
	if dazu := ops(changes, Added); len(dazu) != 1 || !strings.Contains(dazu[0], "neuer Absatz") {
		t.Fatalf("der neue Absatz fehlt: %v", dazu)
	}
}

// Deutsche Abkürzungen enden auf einen Punkt und sind trotzdem kein Satzende.
// Wird hier getrennt, zerfällt ein unveränderter Satz in zwei Bruchstücke und
// der Diff meldet eine Änderung, die keine ist.
func TestAbbreviationsAreNotSentenceEnds(t *testing.T) {
	text := "Das gilt z. B. für Verzeichnisse. Und d. h. auch für die Wurzel."
	if got := len(Sentences(text)); got != 2 {
		t.Fatalf("zwei Sätze erwartet, got %d: %q", got, Sentences(text))
	}
}

// Ein Punkt mitten in einem Bezeichner oder einer Zahl trennt nicht.
func TestDotsInsideIdentifiersAndNumbersDoNotSplit(t *testing.T) {
	text := "Die Datei ghost_history.go hält 33.2 KB. Mehr nicht."
	if got := Sentences(text); len(got) != 2 {
		t.Fatalf("zwei Sätze erwartet, got %d: %q", len(got), got)
	}
}

// Eine Ellipse ist kein Satzende. An einer echten Beschreibung aufgefallen:
// "archiveGhostFile ist ein INSERT ... SELECT und tut nichts, wenn ..." zerfiel
// mitten im SQL in zwei Bruchstuecke, und der Diff meldete beide als geaendert.
func TestAnEllipsisIsNotASentenceEnd(t *testing.T) {
	text := "Das ist ein INSERT ... SELECT und tut nichts. Danach ist Schluss."
	got := Sentences(text)
	if len(got) != 2 {
		t.Fatalf("zwei Saetze erwartet, got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "INSERT ... SELECT und tut nichts.") {
		t.Errorf("das SQL wurde zerschnitten: %q", got[0])
	}
}

// Befunde eines externen Pruef-Agenten am 2026-08-25, beide an der
// Satztrennung. Eine Ziffer als Satzanfang zuzulassen kostet mehr, als sie
// bringt: "Abb. 1", "Nr. 5", "Kapitel 3. 2 Punkte" zerfallen daran, waehrend
// ein Satz, der wirklich mit einer Zahl beginnt, in Prosa kaum vorkommt.
func TestANumberDoesNotOpenASentence(t *testing.T) {
	got := Sentences("Abb. 1 zeigt den Ablauf. Danach ist Schluss.")
	if len(got) != 2 {
		t.Fatalf("zwei Saetze erwartet, got %d: %q", len(got), got)
	}
}

// Und die Abkuerzung mit innerem Punkt: "z. B." wurde erkannt, "z.B." nicht —
// dieselbe Abkuerzung, zwei Ergebnisse.
func TestAbbreviationsWithAnInnerDotAreRecognisedToo(t *testing.T) {
	got := Sentences("Das gilt z.B. Verzeichnisse trifft es auch. Ende.")
	if len(got) != 2 {
		t.Fatalf("zwei Saetze erwartet, got %d: %q", len(got), got)
	}
}
