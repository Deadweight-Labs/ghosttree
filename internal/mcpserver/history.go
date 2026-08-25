package mcpserver

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/ghost"
	"github.com/Deadweight-Labs/ghosttree/internal/prose"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// renderHistory schreibt, was sich an einer Beschreibung geändert hat — nicht,
// was in jeder Fassung stand. Zwei Prosablöcke nebeneinander machen den
// Unterschied nicht sichtbar; ein externer Agent konnte ihn herauslesen, aber
// die Arbeit gehört ins Werkzeug (REQ-180).
//
// full gibt stattdessen jede abgelöste Fassung im Wortlaut aus.
func renderHistory(name string, chain []store.GhostVersion, full bool) string {
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	if len(chain) < 2 {
		return fmt.Sprintf("%s: keine früheren Fassungen — die aktuelle Beschreibung ist die erste.", name)
	}
	if full {
		return renderVerbatim(name, chain)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## %s — was sich an der Beschreibung geändert hat\n\n", name)
	for _, s := range ghost.HistorySteps(chain) {
		if s.Event != "" {
			fmt.Fprintf(&b, "### %s — %s\n\n", shortDate(s.At), s.Event)
			continue
		}
		fmt.Fprintf(&b, "### %s", shortDate(s.At))
		if s.Person != "" {
			fmt.Fprintf(&b, ", von %s", s.Person)
		}
		if s.Current {
			b.WriteString(" (aktuelle Fassung)")
		}
		b.WriteString("\n")
		if prose.Unchanged(s.Changes) {
			b.WriteString("kein Unterschied im Text\n\n")
			continue
		}
		fmt.Fprintf(&b, "%s\n%s\n", prose.Summary(s.Changes), prose.Render(s.Changes))
	}
	b.WriteString("(voller Wortlaut einer früheren Fassung: `full: true`)\n")
	return b.String()
}

func renderVerbatim(name string, chain []store.GhostVersion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Frühere Fassungen von %s\n\n", name)
	for _, v := range chain[1:] {
		fmt.Fprintf(&b, "### %s bis %s", shortDate(v.DescribedAt), shortDate(v.ReplacedAt))
		if v.Person != "" {
			fmt.Fprintf(&b, ", von %s", v.Person)
		}
		if v.Reason != "" && v.Reason != "ersetzt" {
			fmt.Fprintf(&b, " [%s]", v.Reason)
		}
		if v.LineCount > 0 {
			fmt.Fprintf(&b, " — beschriebener Stand: %d Zeilen", v.LineCount)
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(v.Description, "\n"))
		b.WriteString("\n\n")
	}
	return b.String()
}
