package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// interruptedWindow ist die Stille, nach der eine noch nicht beendete Arbeit
// als unterbrochen gilt.
//
// GEMESSEN am 2026-08-25 an 1.692 echten Sitzungsprotokollen (Claude Code und
// Codex, 135.884 Übergänge zwischen zwei Zeilen). Für die 148 Sitzungen, die
// länger als fünf Minuten liefen, liegt der Median der längsten Pause bei 14
// Minuten und das 90. Perzentil bei 82. Entscheidend war die zweite Zahl: der
// Anteil der Sitzungslebenszeit, in dem eine LEBENDE Sitzung fälschlich als
// unterbrochen gälte — 26,5 % bei 20 Minuten, 22,7 % bei 30, 16,7 % bei 60.
//
// Die Kurve ist flach, das Fenster also kein guter Hebel: eine Stunde statt
// zwanzig Minuten kauft zehn Prozentpunkte und kostet den wichtigsten Fall,
// nämlich die abgestürzte oder ins Kontextlimit gelaufene Sitzung, deren
// Nachfolger sofort weiterarbeiten will. Deshalb dreissig Minuten und ehrliche
// Auskunft statt eines längeren Fensters: die Zeile nennt den Zeitpunkt, und wer
// sie liest, urteilt selbst.
const interruptedWindow = 30 * time.Minute

// maxInterruptedThreads hält den Abschnitt bei der Größenordnung, die ein
// Mensch am Sitzungsanfang lesen kann. Sortiert wird nach Aktualität, ein alter
// Faden fällt also heraus, sobald es neuere gibt.
const maxInterruptedThreads = 3

// renderInterrupted nennt jeden angefangenen Faden beim Namen — mit der
// Auftragsnummer und dem Weg zur Übergabe, wenn es eine gibt, und mit der
// ehrlichen Auskunft, wenn es keine gibt. Der zweite Fall ist der wertvollere:
// er macht das Fehlen sichtbar, statt es zu verschweigen.
func renderInterrupted(threads []store.InterruptedThread, at time.Time) string {
	if len(threads) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Interrupted work (ghosttree)\n\n")
	for _, t := range threads {
		fmt.Fprintf(&b, "- REQ-%d %q — stopped %s", t.RequestID, t.Title, humanSince(t.Since, at))
		if t.Handoff != "" {
			fmt.Fprintf(&b, ", handoff left: request_get %d\n", t.RequestID)
			continue
		}
		b.WriteString(", no handoff was left.\n")
	}
	b.WriteString("\nPick one up only if it is what you were asked to do; otherwise leave it.\n")
	return b.String()
}

// humanSince beantwortet "wann war das" in der Auflösung, in der die Antwort
// etwas ändert. Auf die Minute genau ist unter einer Stunde nützlich und
// darüber Ballast.
func humanSince(ts string, at time.Time) string {
	then, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "at an unknown time"
	}
	d := at.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
