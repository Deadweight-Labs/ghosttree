// Package mirror projiziert Wissen, Dokumente und den Auftragsspeicher als
// lesbare Dateien unter .ghosttree/, damit ein Agent nachsehen kann, ohne ein
// Werkzeug aufzurufen — und damit eine Harness ohne MCP und ohne Hooks
// überhaupt einen Kanal hat.
//
// Nur die Leserichtung. Zurückgeschrieben wird nichts: die Datenbank bleibt die
// Wahrheit, alles hier ist Abbild. Zwei Schreiber hätten Konflikte, ein Pfad
// kann den Scope eines Eintrags nicht ausdrücken, und ein toter Beobachter
// hiesse stiller Datenverlust statt stiller Stille.
package mirror

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Doc ist ein Stück Text an einem Pfad unterhalb von .ghosttree/.
type Doc struct {
	Path string
	Body string
}

// Input ist alles, was der Spiegel zeigt. Was hier nicht steht, wird auch nicht
// geschrieben — Sitzungsprotokolle etwa werden gar nicht erst geholt.
type Input struct {
	Project   string
	Machine   string
	Knowledge []store.Knowledge         // die Scope-Vereinigung, die eine Sitzung hier liest
	Archived  []store.Knowledge         // Dokumente: Pläne und Specs als Kaltlager
	Requests  []requestdomain.SearchHit // offene und die jüngsten erledigten
	DoneShown int                       // wie viele erledigte Aufträge im Spiegel stehen
	DoneTotal int                       // wie viele es insgesamt gibt
}

const projectionNote = "Projektion aus ghosttree. Änderungen an dieser Datei verschwinden beim nächsten Neuschreiben."

// Build erzeugt den ganzen Spiegel. Reine Funktion: sie liest keine Datei und
// schreibt keine, damit der Inhalt ohne Dateisystem prüfbar bleibt.
func Build(in Input) []Doc {
	knowledge := deliverable(in.Knowledge)
	mentions := backlinks(knowledge, in.Archived, in.Requests)

	docs := make([]Doc, 0, len(knowledge)+len(in.Archived)+len(in.Requests)+1)
	for _, k := range knowledge {
		path := fmt.Sprintf("knowledge/%s/%d-%s.md", k.Type, k.ID, slug(k.Title))
		docs = append(docs, Doc{Path: path, Body: knowledgeBody(k, mentions[mentionKey("#", k.ID)])})
	}
	for _, k := range in.Archived {
		docs = append(docs, Doc{Path: documentPath(k), Body: documentBody(k, mentions[mentionKey("#", k.ID)])})
	}
	for _, hit := range in.Requests {
		docs = append(docs, Doc{Path: requestPath(hit.Request), Body: requestBody(hit, mentions[mentionKey("REQ-", hit.Request.ID)])})
	}
	docs = append(docs, Doc{Path: "INDEX.md", Body: index(in, len(knowledge))})
	return docs
}

// deliverable hält zurück, was eine Sitzung auch nicht bekäme. Ein Dateisystem
// hat keine Vertrauensstufen: nebeneinander sieht ungeprüft aus wie geprüft,
// und wer den Unterschied nicht sieht, handelt nach dem Ungeprüften.
func deliverable(entries []store.Knowledge) []store.Knowledge {
	out := make([]store.Knowledge, 0, len(entries))
	for _, k := range entries {
		if k.Confidence == "quarantined" || k.Status == "archived" {
			continue
		}
		out = append(out, k)
	}
	return out
}

func knowledgeBody(k store.Knowledge, mentionedBy []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# #%d %s\n\n", k.ID, k.Title)
	fmt.Fprintf(&b, "%s | %s | %s", scopeLabel(k.Scope), k.Type, k.Confidence)
	if k.ObservedAt != "" {
		fmt.Fprintf(&b, " | beobachtet %s", k.ObservedAt)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(k.Body, "\n"))
	b.WriteString("\n")
	b.WriteString(backlinkSection(mentionedBy))
	b.WriteString(footer())
	return b.String()
}

func documentPath(k store.Knowledge) string {
	day := k.ObservedAt
	if len(day) >= 10 {
		day = day[:10]
	}
	if day == "" {
		day = "ohne-datum"
	}
	return fmt.Sprintf("docs/%s-%d-%s.md", day, k.ID, slug(k.Title))
}

func documentBody(k store.Knowledge, mentionedBy []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# #%d %s\n\n", k.ID, k.Title)
	fmt.Fprintf(&b, "%s | %s | %s — Kaltlager: wird nicht in Sitzungen ausgeliefert, ist aber vollständig lesbar\n\n",
		scopeLabel(k.Scope), k.Type, k.Status)
	b.WriteString(strings.TrimRight(k.Body, "\n"))
	b.WriteString("\n")
	b.WriteString(backlinkSection(mentionedBy))
	b.WriteString(footer())
	return b.String()
}

func requestPath(r requestdomain.Request) string {
	state := "open"
	if r.State != "open" {
		state = "done"
	}
	return fmt.Sprintf("requests/%s/REQ-%d-%s.md", state, r.ID, slug(r.Title))
}

func requestBody(hit requestdomain.SearchHit, mentionedBy []string) string {
	r := hit.Request
	var b strings.Builder
	fmt.Fprintf(&b, "# REQ-%d %s\n\n", r.ID, r.Title)
	fmt.Fprintf(&b, "%s | %s | %s", scopeLabel(r.Scope), r.Type, r.State)
	if r.Priority != "" {
		fmt.Fprintf(&b, " | Priorität %s", r.Priority)
	}
	if hit.OpenCriteria > 0 {
		fmt.Fprintf(&b, " | %d offene Kriterien", hit.OpenCriteria)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(r.Description, "\n"))
	b.WriteString("\n")
	if hit.LatestHandoff != "" {
		fmt.Fprintf(&b, "\n## Letzte Übergabe\n\n%s\n", strings.TrimRight(hit.LatestHandoff, "\n"))
	}
	// Der Spiegel zeigt die Beschreibung, nicht die Kriterien mit ihren Belegen:
	// die hängen an Zuständen, die sich im Lauf einer Sitzung ändern, und ein
	// veralteter Haken ist schlimmer als gar keiner.
	fmt.Fprintf(&b, "\nKriterien, Belege und Verlauf: `request_get %d`\n", r.ID)
	b.WriteString(backlinkSection(mentionedBy))
	b.WriteString(footer())
	return b.String()
}

func index(in Input, knowledgeCount int) string {
	var b strings.Builder
	b.WriteString("# .ghosttree — was hier bekannt ist\n\n")
	fmt.Fprintf(&b, "Projekt %s, Maschine %s.\n\n", in.Project, in.Machine)
	fmt.Fprintf(&b, "- `knowledge/` — %d Wissenseinträge: genau die Vereinigung aus Projekt, Zweig, Maschine und global, die eine Sitzung in diesem Repo liest\n", knowledgeCount)
	fmt.Fprintf(&b, "- `docs/` — %d Dokumente: Pläne und Spezifikationen im Volltext, Kaltlager\n", len(in.Archived))
	fmt.Fprintf(&b, "- `requests/` — offene Aufträge und %d von %d erledigten\n", in.DoneShown, in.DoneTotal)
	b.WriteString("- `tree/` — eine Beschreibung je Datei und je Verzeichnis dieses Repos\n\n")
	b.WriteString("## Was hier NICHT steht\n\n")
	b.WriteString("- Sitzungsprotokolle. Mehrere hundert Megabyte, und sie haben trotz Schwärzung nichts in einem Repo-Verzeichnis zu suchen — sie stehen über `context_sessions` zur Verfügung.\n")
	b.WriteString("- Quarantänisiertes und ungeprüftes Wissen. Ein Dateisystem hat keine Vertrauensstufen; nebeneinander sähe Ungeprüftes aus wie Geprüftes. Der Spiegel zeigt, was ausgeliefert würde, der Rest bleibt Sache von `ctx review`.\n")
	b.WriteString("- Abgelöstes und veraltetes Wissen, aus demselben Grund.\n")
	b.WriteString("- Kriterien und Belege der Aufträge: sie ändern sich im Lauf einer Sitzung, und ein veralteter Haken wäre schlimmer als keiner.\n\n")
	b.WriteString("Wer mehr braucht, fragt das Werkzeug: `context_search`, `request_get`, `context_sessions`.\n")
	b.WriteString(footer())
	return b.String()
}

// backlinkSection zeigt, wer diesen Eintrag nennt. Abgeleitet aus den Nennungen
// in den Texten, nicht aus gepflegten Beziehungen — und das steht dran, weil
// eine Heuristik, die sich wie ein Datenmodell liest, zum falschen Vertrauen
// verführt. Ein `#1` in einem Codeblock greift daneben.
func backlinkSection(mentionedBy []string) string {
	if len(mentionedBy) == 0 {
		return ""
	}
	return "\n## Wird erwähnt von\n\n" + strings.Join(mentionedBy, "\n") +
		"\n\n(abgeleitet aus den Nennungen in den Texten, keine gepflegte Beziehung)\n"
}

func mentionKey(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

// backlinks sammelt für jeden nennbaren Eintrag die Texte, die ihn nennen.
func backlinks(knowledge, archived []store.Knowledge, requests []requestdomain.SearchHit) map[string][]string {
	type source struct{ label, text string }
	var sources []source
	for _, k := range append(append([]store.Knowledge{}, knowledge...), archived...) {
		sources = append(sources, source{fmt.Sprintf("- #%d %s", k.ID, k.Title), k.Title + "\n" + k.Body})
	}
	for _, hit := range requests {
		r := hit.Request
		sources = append(sources, source{fmt.Sprintf("- REQ-%d %s", r.ID, r.Title), r.Title + "\n" + r.Description + "\n" + hit.LatestHandoff})
	}
	var targets []string
	for _, k := range append(append([]store.Knowledge{}, knowledge...), archived...) {
		targets = append(targets, mentionKey("#", k.ID))
	}
	for _, hit := range requests {
		targets = append(targets, mentionKey("REQ-", hit.Request.ID))
	}
	out := map[string][]string{}
	for _, t := range targets {
		for _, s := range sources {
			if strings.HasPrefix(s.label, "- "+t+" ") {
				continue // sich selbst zu nennen ist kein Rückverweis
			}
			if mentions(s.text, t) {
				out[t] = append(out[t], s.label)
			}
		}
		sort.Strings(out[t])
	}
	return out
}

// mentions prüft auf eine Nennung mit Wortgrenze: #4 darf nicht in #42 treffen.
func mentions(text, token string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], token)
		if j < 0 {
			return false
		}
		end := i + j + len(token)
		if end >= len(text) || !isDigit(text[end]) {
			return true
		}
		i = end
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func footer() string { return "\n---\n" + projectionNote + "\n" }

func scopeLabel(ax scope.Axes) string { return ax.Label() }

// slug macht aus einem Titel einen Dateinamen, der sich in einem `ls` noch
// lesen lässt. Umlaute werden ausgeschrieben statt weggeworfen, sonst wird aus
// "Auslöser" ein "ausl-ser".
func slug(title string) string {
	replacer := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
		"Ä", "ae", "Ö", "oe", "Ü", "ue")
	s := replacer.Replace(strings.ToLower(title))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	if out == "" {
		out = "ohne-titel"
	}
	return out
}
