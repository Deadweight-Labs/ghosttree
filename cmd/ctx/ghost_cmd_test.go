package main

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func testkette() []store.GhostVersion {
	rest := "\n\nZwei Feinheiten, die man sonst falsch baut. Erstens der Ast: der Vergleich haengt einen Schraegstrich an, sonst zieht ein Praefix das Nachbarverzeichnis mit. Zweitens die Auslieferung: sie gibt den Pfad UND seine Vorfahren zurueck, aber jeden nur einmal je Sitzung."
	return []store.GhostVersion{
		{Path: "a.go", Person: "alice", DescribedAt: "2026-08-25T09:00:00Z", LineCount: 269,
			Description: "Ein zweites Beschreiben ersetzt das erste — die verdraengte Fassung wandert in die Historie." + rest},
		{Path: "a.go", Person: "alice", DescribedAt: "2026-08-24T09:00:00Z", ReplacedAt: "2026-08-25T09:00:00Z", Reason: "ersetzt",
			Description: "Ein zweites Beschreiben ersetzt das erste — es gibt bewusst keine Fassungshistorie." + rest},
	}
}

// Im Terminal gilt dasselbe wie fuer den Agenten: gefragt ist die Aenderung.
// Zwei Prosabloecke nebeneinander zwingen den Leser, selbst zu vergleichen.
func TestTerminalHistoryShowsTheDifference(t *testing.T) {
	var b strings.Builder
	printHistory(&b, "a.go", testkette(), false)
	out := b.String()

	if strings.Contains(out, "Zwei Feinheiten, die man sonst falsch baut.") {
		t.Errorf("unveraenderte Saetze gehoeren nicht in die Ausgabe:\n%s", out)
	}
	if !strings.Contains(out, "- Ein zweites Beschreiben ersetzt das erste — es gibt bewusst keine Fassungshistorie.") {
		t.Errorf("der gestrichene Satz fehlt:\n%s", out)
	}
	if !strings.Contains(out, "+ Ein zweites Beschreiben ersetzt das erste — die verdraengte Fassung") {
		t.Errorf("der neue Satz fehlt:\n%s", out)
	}
	if !strings.Contains(out, "2026-08-25") || !strings.Contains(out, "alice") {
		t.Errorf("Zeitpunkt und Person fehlen:\n%s", out)
	}
}

// Der Volltext bleibt erreichbar, nur nicht mehr als Vorgabe.
func TestTerminalFullPrintsTheWording(t *testing.T) {
	var b strings.Builder
	printHistory(&b, "a.go", testkette(), true)
	if !strings.Contains(b.String(), "Zwei Feinheiten, die man sonst falsch baut.") {
		t.Errorf("--voll muss den Wortlaut zeigen:\n%s", b.String())
	}
}

// Eine Beschreibung ohne Vorfassung hat nichts zu vergleichen, und das soll
// dastehen statt einer leeren Ausgabe.
func TestTerminalSaysWhenThereIsNoEarlierVersion(t *testing.T) {
	var b strings.Builder
	printHistory(&b, "a.go", testkette()[:1], false)
	if !strings.Contains(b.String(), "keine früheren Fassungen") {
		t.Errorf("erwartet wird ein Hinweis, got %q", b.String())
	}
}

// Die Flagge darf an jeder Stelle stehen und die Zahl daneben ueberleben.
func TestVollFlagIsParsedRegardlessOfPosition(t *testing.T) {
	for _, args := range [][]string{{"a.go", "--voll"}, {"--voll", "a.go"}, {"a.go", "3", "--voll"}} {
		rest, voll := splitVollFlag(args)
		if !voll {
			t.Errorf("--voll nicht erkannt in %v", args)
		}
		if rest[0] != "a.go" {
			t.Errorf("der Pfad geht verloren in %v: %v", args, rest)
		}
	}
	if _, voll := splitVollFlag([]string{"a.go", "3"}); voll {
		t.Error("ohne Flagge darf --voll nicht gelten")
	}
}
