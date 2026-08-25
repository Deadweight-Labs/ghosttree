package ghost

import (
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/prose"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Step ist ein Wechsel von einer Fassung zur nächsten — das, wonach jemand
// fragt, der eine Historie aufruft. Nicht die Fassungen selbst.
type Step struct {
	At      string // wann gewechselt wurde
	Person  string // wer die neue Fassung schrieb
	Current bool   // führt zu der Beschreibung, die heute gilt
	Event   string // gesetzt statt Changes, wenn es kein Textwechsel war
	Lines   int    // Codestand, den die neue Fassung beschrieb
	Changes []prose.Change
}

// HistorySteps zerlegt eine Kette (neueste zuerst, aktuelle Fassung an der
// Spitze) in Wechsel. Bei n Fassungen sind es n-1 Wechsel.
//
// Ein Umzugsvermerk ist ein Ereignis und kein Text: er trägt statt einer
// Beschreibung den Satz "(verschoben von X)". Ihn mitzuvergleichen meldete die
// ganze Beschreibung als neu und verdeckte die echte Vorfassung darunter. Er
// wird deshalb als Ereignis ausgegeben und beim Vergleichen übersprungen.
func HistorySteps(chain []store.GhostVersion) []Step {
	var steps []Step
	for i, v := range chain {
		if v.Reason == "verschoben" {
			steps = append(steps, Step{At: v.ReplacedAt, Event: strings.Trim(v.Description, "()")})
			continue
		}
		vor := lastWithText(chain, i)
		if vor == nil {
			continue
		}
		steps = append(steps, Step{
			At:      vor.ReplacedAt,
			Person:  v.Person,
			Current: i == 0,
			Lines:   v.LineCount,
			Changes: prose.Diff(vor.Description, v.Description),
		})
	}
	return steps
}

// lastWithText heisst nicht comparable, obwohl das der treffendere Name wäre:
// comparable ist ein vordefinierter Bezeichner, und eine Funktion dieses Namens
// überschattet ihn im ganzen Paket — ein generischer Constraint wäre hier
// danach nicht mehr schreibbar.
func lastWithText(chain []store.GhostVersion, i int) *store.GhostVersion {
	for j := i + 1; j < len(chain); j++ {
		if chain[j].Reason != "verschoben" {
			return &chain[j]
		}
	}
	return nil
}
