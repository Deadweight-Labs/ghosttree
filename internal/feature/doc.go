package feature

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

// Die Seite für Menschen (REQ-612, Kriterium 5): docs/features.md entsteht aus den
// Features, statt von Hand zu veralten, und die vorhandenen Abschnitte der Datei
// lassen sich als Features übernehmen. Beides ohne Server: reine Funktionen über
// Text, die der Befehl ctx feature export und ctx feature import benutzen.

// DocIntro ist der Vorspann der erzeugten Seite.
const DocIntro = "The complete list of what the product does in the current development version, generated from the feature pages (`ctx feature export`). Do not edit it by hand: change the feature instead."

// RenderDoc schreibt die aktiven Features als Markdown: eine Aufzählung, je Feature
// ein Punkt mit Namen und Kurzbeschreibung, darunter Verhalten, Grenzen und
// Bedienung. Die Reihenfolge ist die der Nummern, damit zwei Läufe denselben Text
// liefern.
func RenderDoc(title string, list []Feature) string {
	if title == "" {
		title = "What works today"
	}
	sorted := append([]Feature(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", title, DocIntro)
	for _, f := range sorted {
		if f.State != Active {
			continue
		}
		head := "- **" + oneLine(f.Name) + "**"
		if s := oneLine(f.Summary); s != "" {
			head += ": " + s
		}
		b.WriteString(head + "\n")
		para := func(label, text string) {
			text = strings.TrimSpace(text)
			if text == "" {
				return
			}
			b.WriteString("\n")
			prefix := ""
			if label != "" {
				prefix = label + ": "
			}
			lines := strings.Split(prefix+text, "\n")
			for _, l := range lines {
				if strings.TrimSpace(l) == "" {
					b.WriteString("\n")
					continue
				}
				b.WriteString("  " + strings.TrimRight(l, " \t") + "\n")
			}
		}
		para("", f.Behavior)
		para("Limits", f.Limits)
		para("CLI", f.CLI)
		para("MCP tools", f.MCP)
		para("API", f.API)
		para("Web interface", f.UI)
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// DocItem ist ein Aufzählungspunkt der Seite: der Text mit den eingerückten
// Fortsetzungen, ohne Einrückung.
type DocItem struct {
	Text string `json:"text"`
}

// ParseDoc liest die Aufzählung der Seite. Ein Punkt beginnt mit "- " am
// Zeilenanfang, seine Fortsetzungen sind eingerückt oder leer; die Liste endet an
// der ersten Zeile, die weder noch ist, nachdem sie begonnen hat (der Nachsatz
// unter der Liste ist kein Feature).
func ParseDoc(md string) []DocItem {
	var items []DocItem
	var cur []string
	flush := func() {
		if cur == nil {
			return
		}
		text := strings.TrimSpace(strings.Join(cur, "\n"))
		if text != "" {
			items = append(items, DocItem{Text: text})
		}
		cur = nil
	}
	started := false
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			flush()
			started = true
			cur = []string{strings.TrimPrefix(line, "- ")}
		case started && (strings.TrimSpace(line) == "" || strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")):
			if cur != nil {
				cur = append(cur, strings.TrimPrefix(strings.TrimPrefix(line, "  "), "\t"))
			}
		case started:
			flush()
			started = false
			return items
		}
	}
	flush()
	return items
}

// DocMap ordnet einem Punkt der Seite Name und Bereiche zu: Match ist der Anfang
// des Punktes. Eine Seite hat keine Namen und keine Bereiche, die stehen in dieser
// Zuordnung (docs/features-map.json).
type DocMap struct {
	Match string   `json:"match"`
	Name  string   `json:"name"`
	Areas []string `json:"areas"`
}

// ParseDocMap liest die Zuordnung.
func ParseDocMap(raw []byte) ([]DocMap, error) {
	var m []DocMap
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("feature map: %w", err)
	}
	for i, e := range m {
		if strings.TrimSpace(e.Match) == "" || strings.TrimSpace(e.Name) == "" {
			return nil, fmt.Errorf("feature map entry %d needs match and name", i+1)
		}
	}
	return m, nil
}

// DocFeature ist, was die Übernahme aus der Seite je Punkt vorschlägt.
type DocFeature struct {
	Content
	// Mapped: die Zuordnung nannte den Punkt. Ohne sie sind Name und Bereiche
	// geraten (Name) oder leer (Bereiche).
	Mapped bool `json:"mapped"`
}

// FromDoc macht aus den Punkten der Seite Features: der erste Satz wird die
// Kurzbeschreibung, der ganze Punkt das Verhalten. Name und Bereiche kommen aus
// der Zuordnung; ohne sie wird der Name aus den ersten Wörtern geraten und es gibt
// keinen Bereich. Ein Eintrag der Zuordnung, der keinen Punkt trifft, ist ein
// Fehler: die Seite und die Zuordnung sollen nicht auseinanderlaufen.
func FromDoc(items []DocItem, mapping []DocMap) ([]DocFeature, error) {
	used := make([]bool, len(mapping))
	var out []DocFeature
	for _, it := range items {
		var df DocFeature
		matched := -1
		for i, m := range mapping {
			if strings.HasPrefix(it.Text, m.Match) {
				matched = i
				break
			}
		}
		summary := idea.Brief(firstSentence(it.Text), MaxSummary)
		behavior := it.Text
		if utf8.RuneCountInString(behavior) > MaxBehavior {
			behavior = string([]rune(behavior)[:MaxBehavior])
		}
		df.Summary, df.Behavior = summary, behavior
		df.Areas = []string{}
		if matched >= 0 {
			used[matched] = true
			df.Name, df.Areas, df.Mapped = mapping[matched].Name, mapping[matched].Areas, true
		} else {
			df.Name = guessName(it.Text)
		}
		c, err := Content{Areas: []string{}}.Apply(Patch{Name: &df.Name, Summary: &df.Summary, Behavior: &df.Behavior, Areas: &df.Areas})
		if err != nil {
			return nil, fmt.Errorf("item %q: %w", firstWords(it.Text, 6), err)
		}
		df.Content = c
		out = append(out, df)
	}
	for i, m := range mapping {
		if !used[i] {
			return nil, fmt.Errorf("feature map entry %q matches no item of the page", m.Match)
		}
	}
	return out, nil
}

// guessName nimmt die ersten Wörter bis zum ersten Satzzeichen, höchstens
// MaxNameWords.
func guessName(text string) string {
	t := oneLine(text)
	if i := strings.IndexAny(t, ":,.(;"); i > 0 {
		t = t[:i]
	}
	return firstWords(t, MaxNameWords)
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) > n {
		w = w[:n]
	}
	out := strings.Join(w, " ")
	if r := []rune(out); len(r) > MaxName {
		out = string(r[:MaxName])
	}
	return out
}
