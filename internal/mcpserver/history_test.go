package mcpserver

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Zwei Fassungen, die sich in genau einem Satz unterscheiden. Bisher wurden
// beide vollstaendig ausgeliefert und der Agent durfte sie selbst vergleichen —
// er konnte es, aber die Arbeit haette das Werkzeug tun sollen (REQ-180).
//
// Die Vorlage ist eine echte Beschreibung aus diesem Repo, samt ihrer Laenge.
// Das ist kein Zierrat: an drei kurzen Saetzen kann kein Diff kuerzer sein als
// der Volltext, weil der eine geaenderte Satz dann schwerer wiegt als alles
// uebrige zusammen. Gemessen wird, was hier wirklich in der Datenbank steht.
func kette() []store.GhostVersion {
	gemeinsam := "\n\nZwei Feinheiten, die man sonst falsch baut. Erstens der Ast: der Vergleich haengt einen Schraegstrich an, sonst zieht ein Praefix das Nachbarverzeichnis mit, dessen Name zufaellig genauso anfaengt. Zweitens die Auslieferung: sie gibt den Pfad UND seine Vorfahren zurueck, aber jeden nur einmal je Sitzung.\n\nAus dem Schweigen darf nicht auf Nichtexistenz geschlossen werden — genau das tat der Hook und behauptete beim zweiten Aendern, eine beschriebene Datei habe keine Beschreibung."
	neu := "Die Identitaet ist das Paar aus Projekt und Pfad. Ein zweites Beschreiben ersetzt das erste — die verdraengte Fassung wandert in die Historie. Ausgeliefert wird nur die aktuelle." + gemeinsam
	alt := "Die Identitaet ist das Paar aus Projekt und Pfad. Ein zweites Beschreiben ersetzt das erste — es gibt bewusst keine Fassungshistorie. Ausgeliefert wird nur die aktuelle." + gemeinsam
	return []store.GhostVersion{
		{Path: "a.go", Description: neu, Person: "robin", DescribedAt: "2026-08-25T09:00:00Z"},
		{Path: "a.go", Description: alt, Person: "robin", DescribedAt: "2026-08-24T09:00:00Z",
			ReplacedAt: "2026-08-25T09:00:00Z", Reason: "ersetzt"},
	}
}

// Der Kern von REQ-180: geliefert wird die AENDERUNG, nicht zweimal derselbe
// Text mit einem geaenderten Satz darin.
func TestHistoryShowsTheChangeAndNotBothVersions(t *testing.T) {
	out := renderHistory("a.go", kette(), false)

	if strings.Contains(out, "Die Identitaet ist das Paar") {
		t.Errorf("unveraenderte Saetze gehoeren nicht in die Ausgabe:\n%s", out)
	}
	if !strings.Contains(out, "bewusst keine Fassungshistorie") ||
		!strings.Contains(out, "wandert in die Historie") {
		t.Errorf("der geaenderte Satz fehlt in beiden Fassungen:\n%s", out)
	}
	if voll := renderHistory("a.go", kette(), true); len(out) >= len(voll) {
		t.Errorf("der Diff muss kuerzer sein als der Volltext: %d vs %d", len(out), len(voll))
	}
}

// Der Volltext bleibt erreichbar. Wer eine ganze frueher gueltige Beschreibung
// lesen will, soll sie bekommen — nur nicht mehr ungefragt.
func TestFullKeepsEveryVersionVerbatim(t *testing.T) {
	out := renderHistory("a.go", kette(), true)
	if !strings.Contains(out, "Die Identitaet ist das Paar aus Projekt und Pfad.") {
		t.Errorf("die vollstaendige alte Fassung fehlt:\n%s", out)
	}
}

// Ein Umzug ist ein Ereignis, kein Text. Wuerde der Vermerk als Fassung
// verglichen, meldete der Diff die ganze Beschreibung als neu — und die echte
// Vorfassung darunter waere nie zu sehen.
func TestAMoveIsAnEventAndDoesNotBreakTheComparison(t *testing.T) {
	c := kette()
	mitUmzug := []store.GhostVersion{c[0], {
		Path: "a.go", Description: "(verschoben von alt.go)", Reason: "verschoben",
		DescribedAt: "2026-08-24T09:00:00Z", ReplacedAt: "2026-08-25T08:00:00Z",
	}, c[1]}

	out := renderHistory("a.go", mitUmzug, false)

	if !strings.Contains(out, "verschoben von alt.go") {
		t.Errorf("der Umzug muss sichtbar bleiben:\n%s", out)
	}
	if !strings.Contains(out, "bewusst keine Fassungshistorie") {
		t.Errorf("der Umzug darf den Vergleich mit der echten Vorfassung nicht verdecken:\n%s", out)
	}
}

// Eine Beschreibung, die nie geaendert wurde, hat keine Aenderung zu zeigen.
func TestASingleVersionSaysThereIsNothingToCompare(t *testing.T) {
	out := renderHistory("a.go", kette()[:1], false)
	if !strings.Contains(out, "keine früheren Fassungen") {
		t.Errorf("erwartet wird ein klarer Hinweis, got:\n%s", out)
	}
}
