package query

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Preset ist eine gespeicherte Suche: ein Name und eine bis fünf Suchzeilen mit
// Platzhaltern in geschweiften Klammern. Mehrere Zeilen laufen nacheinander,
// ihre Treffer werden zusammengelegt (zum Beispiel "was wurde im PR erledigt":
// Kriterien, Requests, Wissen und Pains, die je einen eigenen Zustand haben).
type Preset struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Queries     []string `json:"queries"`
	// Args sind die Platzhalter in der Reihenfolge ihres ersten Auftretens.
	Args []string `json:"args,omitempty"`
}

// Platzhalter, die ein Preset benutzen darf.
var Placeholders = []string{"pr", "branch", "area", "since"}

// Maße.
const (
	MaxPresetQueries = 5
	MaxPresetLine    = 300
)

var placeholderRE = regexp.MustCompile(`\{([a-z]+)\}`)
var presetNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidPresetName sagt, ob ein Name als Preset-Name taugt: klein, Ziffern und
// Bindestrich, wie ein Kommando.
func ValidPresetName(n string) bool { return presetNameRE.MatchString(n) }

// ArgsOf nennt die Platzhalter der Zeilen, jeden einmal.
func ArgsOf(queries []string) []string {
	var out []string
	for _, q := range queries {
		for _, m := range placeholderRE.FindAllStringSubmatch(q, -1) {
			if !contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// dummy sind Werte, mit denen sich eine Vorlage beim Speichern prüfen lässt.
var dummy = map[string]string{"pr": "1", "branch": "main", "area": "internal", "since": "2026-01-01"}

// CheckPreset prüft eine Vorlage, bevor sie gespeichert wird: Name, Zeilen
// (eine Zeile, nicht zu lang, höchstens MaxPresetQueries), nur bekannte
// Platzhalter, und jede Zeile muss mit Beispielwerten als Suchzeile gelten. So
// steht ein unbekannter Qualifier beim Speichern im Fehler und nicht erst beim
// ersten Aufruf.
func CheckPreset(name string, queries []string) error {
	if !ValidPresetName(name) {
		return fmt.Errorf("preset name %q: use lower-case letters, digits and hyphens, at most 40 characters", name)
	}
	if len(queries) == 0 || len(queries) > MaxPresetQueries {
		return fmt.Errorf("a preset has one to %d search lines", MaxPresetQueries)
	}
	for _, q := range queries {
		if strings.TrimSpace(q) == "" {
			return fmt.Errorf("a search line is empty")
		}
		if len([]rune(q)) > MaxPresetLine {
			return fmt.Errorf("a search line is longer than %d characters", MaxPresetLine)
		}
		for _, r := range q {
			if unicode.IsControl(r) {
				return fmt.Errorf("a search line is one line without control characters")
			}
		}
		for _, m := range placeholderRE.FindAllStringSubmatch(q, -1) {
			if _, ok := dummy[m[1]]; !ok {
				return fmt.Errorf("unknown placeholder {%s}; use %s", m[1], "{"+strings.Join(Placeholders, "}, {")+"}")
			}
		}
		filled := placeholderRE.ReplaceAllStringFunc(q, func(m string) string { return dummy[m[1:len(m)-1]] })
		if _, err := Parse(filled, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// Expand setzt die Argumente in die Zeilen ein. Ein fehlendes Argument und ein
// Argument, das die Vorlage nicht kennt, sind Fehler. Ein Wert ist ein einzelnes
// Wort: ohne Leerzeichen, Anführungszeichen und Steuerzeichen, damit er keinen
// weiteren Qualifier einschleust.
func Expand(queries []string, args map[string]string) ([]string, error) {
	need := ArgsOf(queries)
	for k := range args {
		if !contains(need, k) {
			return nil, fmt.Errorf("this search takes no argument %q (it takes %s)", k, listArgs(need))
		}
	}
	for _, k := range need {
		v, ok := args[k]
		if !ok || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("this search needs %s", listArgs(need))
		}
		v = strings.TrimSpace(v)
		if strings.ContainsAny(v, " \t\"") || strings.ContainsFunc(v, unicode.IsControl) {
			return nil, fmt.Errorf("argument %s must be one word without quotation marks", k)
		}
		args[k] = v
	}
	out := make([]string, len(queries))
	for i, q := range queries {
		out[i] = placeholderRE.ReplaceAllStringFunc(q, func(m string) string { return args[m[1:len(m)-1]] })
	}
	return out, nil
}

func listArgs(a []string) string {
	if len(a) == 0 {
		return "no arguments"
	}
	return "{" + strings.Join(a, "}, {") + "}"
}

// Builtin sind die mitgelieferten Suchen. Eine gespeicherte Suche gleichen Namens
// (persönlich, Projekt, Org) geht ihnen vor.
var Builtin = []Preset{
	{
		Name:        "pr-ergebnis",
		Description: "Was wurde in PR #N erledigt, beschlossen oder gelernt: erfüllte Kriterien, abgeschlossene Requests, Wissen, Pains und Ideen mit dem PR im Anker",
		Queries: []string{
			"kind:criterion is:done pr:{pr}",
			"kind:request is:done pr:{pr}",
			"kind:knowledge pr:{pr}",
			"kind:pain pr:{pr}",
			"kind:idea pr:{pr}",
		},
	},
	{
		Name:        "branch-offen",
		Description: "Was ist auf Branch X offen: offene Requests, Kriterien, Pains, Ideen und Aufgaben",
		Queries:     []string{"branch:{branch} is:open"},
	},
	{
		Name:        "ordner",
		Description: "Was weiß man über Ordner Y: alles, was dort entstanden ist, samt Datei-Beschreibungen",
		Queries:     []string{"area:{area}"},
	},
	{
		Name:        "seit",
		Description: "Was hat sich seit Datum Z geändert",
		Queries:     []string{"updated:>={since}"},
	},
	{
		Name:        "fuer-mich",
		Description: "Was wartet auf mich: offene Aufgaben an mich und meine offenen Requests, Pains und Ideen",
		Queries: []string{
			"kind:task is:open for:@me",
			"kind:request,pain,idea is:open author:@me",
		},
	},
}

func init() {
	for i := range Builtin {
		Builtin[i].Args = ArgsOf(Builtin[i].Queries)
	}
}

// BuiltinByName findet eine mitgelieferte Suche.
func BuiltinByName(name string) (Preset, bool) {
	for _, p := range Builtin {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}
