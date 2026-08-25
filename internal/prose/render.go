package prose

import (
	"fmt"
	"strings"
)

// maxRun ist die Zahl der Sätze, die ein zusammenhängender Block gestrichener
// oder neuer Sätze am Stück zeigt. Ohne diese Grenze wäre ein vollständig neu
// geschriebener Text als Diff länger als beide Fassungen einzeln.
//
// Vier, weil ein Absatz an drei bis vier Sätzen erkennbar ist und der Rest nur
// noch bestätigt. Verloren geht dabei nichts: der Wortlaut steht vollständig
// bereit, er ist nur nicht mehr die Vorgabe.
const maxRun = 4

// Unchanged sagt, ob zwischen den beiden Fassungen überhaupt etwas liegt.
func Unchanged(changes []Change) bool {
	for _, c := range changes {
		if c.Op != Same {
			return false
		}
	}
	return true
}

// Summary nennt die Größenordnung der Änderung in einer Zeile.
func Summary(changes []Change) string {
	var weg, dazu, gleich int
	for _, c := range changes {
		switch c.Op {
		case Removed:
			weg++
		case Added:
			dazu++
		default:
			gleich++
		}
	}
	satz := "Sätze"
	if weg == 1 {
		satz = "Satz"
	}
	return fmt.Sprintf("%d %s weg, %d neu, %d unverändert", weg, satz, dazu, gleich)
}

// Render schreibt den Diff in der Form, die jeder Leser schon kennt: gestrichene
// Sätze mit -, neue mit +. Unveränderte Sätze stehen nur als Zahl da — sie sind
// der Grund, warum zwei Fassungen nebeneinander unlesbar sind.
func Render(changes []Change) string {
	var b strings.Builder
	for i := 0; i < len(changes); {
		op := changes[i].Op
		j := i
		for j < len(changes) && changes[j].Op == op {
			j++
		}
		run := changes[i:j]
		if op == Same {
			fmt.Fprintf(&b, "  … %s unverändert\n", zaehl(len(run)))
			i = j
			continue
		}
		sign := "-"
		if op == Added {
			sign = "+"
		}
		shown := run
		if len(shown) > maxRun {
			shown = run[:maxRun]
		}
		for _, c := range shown {
			fmt.Fprintf(&b, "%s %s\n", sign, c.Text)
		}
		if rest := len(run) - len(shown); rest > 0 {
			fmt.Fprintf(&b, "%s … und %s weitere\n", sign, zaehl(rest))
		}
		i = j
	}
	return b.String()
}

func zaehl(n int) string {
	if n == 1 {
		return "1 Satz"
	}
	return fmt.Sprintf("%d Sätze", n)
}
