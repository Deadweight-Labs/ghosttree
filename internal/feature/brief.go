package feature

import (
	"fmt"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

// Der kurze Kontext (REQ-612, Kriterium 4): ein Agent, der in einem Bereich
// arbeitet, bekommt die betroffenen Features kurz, mit Budget. Kurz heißt: Name,
// Kurzbeschreibung, Grenzen in einem Satz und der Weg zur ganzen Seite. Der Text
// geht in den Agentenkontext und besteht darum nur aus bereinigten Einzeilern.

// Brief ist ein Feature in der Kurzform des Kontexts.
type Brief struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Summary  string `json:"summary"`
	Limits   string `json:"limits,omitempty"`
	// Area ist der Bereich des Features, der den Pfad enthält.
	Area string `json:"area"`
}

// Grenzen des Kontexts. Eine Zeile ist höchstens so lang, das Budget gilt für den
// ganzen Abschnitt.
const (
	BriefLine     = 420
	BriefSummary  = 240
	BriefLimits   = 110
	DefaultBudget = 1200
	MaxBudget     = 4000
)

// BriefOf macht aus einem Feature die Kurzform für einen Pfad.
func BriefOf(f Feature, p string) Brief {
	b := Brief{ID: f.ID, Name: idea.Brief(f.Name, MaxName), Revision: f.Revision, Summary: idea.Brief(f.Summary, BriefSummary)}
	if l := firstSentence(f.Limits); l != "" {
		b.Limits = idea.Brief(l, BriefLimits)
	}
	for _, a := range f.Areas {
		if InArea(p, a) {
			b.Area = a
			break
		}
	}
	return b
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// RenderBriefs schreibt die Features als Abschnitt für den Kontext. Was nicht
// mehr ins Budget passt, steht als „und n weitere“, nie als stilles Abschneiden.
// shown sagt, wie viele Zeilen tatsächlich stehen.
func RenderBriefs(list []Brief, budget int) (text string, shown int) {
	if len(list) == 0 {
		return "", 0
	}
	if budget <= 0 {
		budget = DefaultBudget
	}
	if budget > MaxBudget {
		budget = MaxBudget
	}
	var b strings.Builder
	b.WriteString("\n## Features in this area (ghosttree)\n\n")
	for _, f := range list {
		line := fmt.Sprintf("- FEAT-%d %s (version %d)", f.ID, f.Name, f.Revision)
		if f.Summary != "" {
			line += ": " + f.Summary
		}
		if f.Limits != "" {
			line += " Limits: " + f.Limits
		}
		if r := []rune(line); len(r) > BriefLine {
			line = string(r[:BriefLine]) + "…"
		}
		line += "\n"
		if b.Len()+len(line) > budget && shown > 0 {
			break
		}
		b.WriteString(line)
		shown++
	}
	if rest := len(list) - shown; rest > 0 {
		fmt.Fprintf(&b, "- and %d more (feature_search with path)\n", rest)
	}
	b.WriteString("\nThe page of a feature (behavior, limits, usage): feature_get. If your change alters what a feature does, write the new state with feature_update.\n")
	return b.String(), shown
}
