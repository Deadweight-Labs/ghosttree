package ghost

import (
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Eine Kette aus drei Fassungen ergibt zwei Schritte — es gibt einen Wechsel
// weniger als Fassungen. Der erste Schritt fuehrt zu der Beschreibung, die
// heute gilt; das muss der Leser sehen, sonst weiss er nicht, wo er steht.
func TestAChainOfThreeVersionsHasTwoSteps(t *testing.T) {
	chain := []store.GhostVersion{
		{Description: "dritte Fassung.", Person: "alice"},
		{Description: "zweite Fassung.", Person: "alice", ReplacedAt: "2026-08-25T09:00:00Z"},
		{Description: "erste Fassung.", Person: "alex", ReplacedAt: "2026-08-24T09:00:00Z"},
	}

	steps := HistorySteps(chain)

	if len(steps) != 2 {
		t.Fatalf("zwei Wechsel erwartet, got %d", len(steps))
	}
	if !steps[0].Current {
		t.Error("der erste Schritt fuehrt zur heute gueltigen Fassung")
	}
	if steps[0].At != "2026-08-25T09:00:00Z" {
		t.Errorf("der Zeitpunkt ist der der Abloesung, got %q", steps[0].At)
	}
	if steps[1].Current {
		t.Error("nur der erste Schritt ist der aktuelle")
	}
}

// Der Umzugsvermerk ist ein Ereignis und kein Text. Wuerde er als Fassung
// verglichen, meldete der Diff die ganze Beschreibung als neu und die echte
// Vorfassung darunter waere nie zu sehen.
func TestAMoveBecomesAnEventAndIsSkippedWhenComparing(t *testing.T) {
	chain := []store.GhostVersion{
		{Description: "aktuelle Fassung."},
		{Description: "(verschoben von alt.go)", Reason: "verschoben", ReplacedAt: "2026-08-25T08:00:00Z"},
		{Description: "urspruengliche Fassung.", ReplacedAt: "2026-08-24T09:00:00Z"},
	}

	steps := HistorySteps(chain)

	var ereignisse, vergleiche int
	for _, s := range steps {
		if s.Event != "" {
			ereignisse++
			continue
		}
		vergleiche++
		if len(s.Changes) == 0 {
			t.Error("ein Vergleichsschritt ohne Aenderungen ist keiner")
		}
	}
	if ereignisse != 1 {
		t.Errorf("der Umzug muss als Ereignis erscheinen, got %d", ereignisse)
	}
	if vergleiche != 1 {
		t.Fatalf("genau ein Textvergleich erwartet, got %d", vergleiche)
	}
	// Der Vergleich muss ueber den Umzug hinweg auf die echte Vorfassung greifen.
	var sah bool
	for _, s := range steps {
		for _, c := range s.Changes {
			if c.Text == "urspruengliche Fassung." {
				sah = true
			}
		}
	}
	if !sah {
		t.Error("die echte Vorfassung wurde vom Umzug verdeckt")
	}
}

// Eine Beschreibung ohne Historie hat keinen Wechsel zu zeigen.
func TestASingleVersionHasNoSteps(t *testing.T) {
	if steps := HistorySteps([]store.GhostVersion{{Description: "einmalig."}}); len(steps) != 0 {
		t.Fatalf("keine Schritte erwartet, got %d", len(steps))
	}
}
