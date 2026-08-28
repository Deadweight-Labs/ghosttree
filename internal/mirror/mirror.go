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

	docwork "github.com/Deadweight-Labs/ghosttree/internal/doc"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Doc ist ein Stück Text an einem Pfad unterhalb von .ghosttree/.
type Doc struct {
	Path string
	Body string
}

type PublishedDocument struct {
	Document store.Document
	Body     string
}

// Input ist alles, was der Spiegel zeigt. Was hier nicht steht, wird auch nicht
// geschrieben — Sitzungsprotokolle etwa werden gar nicht erst geholt.
type Input struct {
	Project   string
	Machine   string
	Knowledge []store.Knowledge // die Scope-Vereinigung, die eine Sitzung hier liest
	Documents []PublishedDocument
	Requests  []requestdomain.SearchHit // offene und die jüngsten erledigten
	DoneShown int                       // wie viele erledigte Aufträge im Spiegel stehen
	DoneTotal int                       // wie viele es insgesamt gibt

	// Wie voll der Ghost-Baum ist. Ungezählt liest sich "eine Beschreibung je
	// Datei" als Zusage, und die vielen unbeschriebenen Pfade wirken dann wie
	// ein Mangel des Baums statt wie sein Füllstand.
	TreeDescribed int
	TreePaths     int

	// At ist der Zeitpunkt dieses Durchlaufs. Ohne ihn sieht der Spiegel frisch
	// aus, egal wie alt er ist — und auf einer Umgebung ohne Hook schreibt ihn
	// niemand von allein.
	At string
}

const projectionNote = "Projection from ghosttree. Edits to this file are lost on the next write."

// Build erzeugt den ganzen Spiegel. Reine Funktion: sie liest keine Datei und
// schreibt keine, damit der Inhalt ohne Dateisystem prüfbar bleibt.
func Build(in Input) []Doc {
	knowledge := deliverable(in.Knowledge)
	mentions := backlinks(knowledge, in.Requests)

	docs := make([]Doc, 0, len(knowledge)+len(in.Documents)+len(in.Requests)+1)
	for _, k := range knowledge {
		path := fmt.Sprintf("knowledge/%s/%d-%s.md", k.Type, k.ID, slug(k.Title))
		docs = append(docs, Doc{Path: path, Body: knowledgeBody(k, mentions[mentionKey("#", k.ID)])})
	}
	for _, document := range in.Documents {
		docs = append(docs, Doc{Path: documentPath(document), Body: documentBody(document)})
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
	if provenance := store.KnowledgeProvenance(k); provenance != "" {
		fmt.Fprintf(&b, " | %s", provenance)
	}
	if k.ObservedAt != "" {
		fmt.Fprintf(&b, " | observed %s", k.ObservedAt)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(k.Body, "\n"))
	b.WriteString("\n")
	b.WriteString(backlinkSection(mentionedBy))
	b.WriteString(footer())
	return b.String()
}

func documentPath(p PublishedDocument) string {
	day := p.Document.CreatedAt
	if len(day) >= 10 {
		day = day[:10]
	}
	if day == "" {
		day = "ohne-datum"
	}
	dir, err := docwork.KindDir(p.Document.Kind)
	if err != nil {
		dir = "other"
	}
	return fmt.Sprintf("docs/%s/%s-%s.md", dir, day, slug(p.Document.Slug))
}

func documentBody(p PublishedDocument) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(p.Body, "\n"))
	b.WriteString("\n")
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
		fmt.Fprintf(&b, " | priority %s", r.Priority)
	}
	if hit.OpenCriteria > 0 {
		fmt.Fprintf(&b, " | %d open criteria", hit.OpenCriteria)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(r.Description, "\n"))
	b.WriteString("\n")
	if hit.LatestHandoff != "" {
		fmt.Fprintf(&b, "\n## Last handoff\n\n%s\n", strings.TrimRight(hit.LatestHandoff, "\n"))
	}
	// Der Spiegel zeigt die Beschreibung, nicht die Kriterien mit ihren Belegen:
	// die hängen an Zuständen, die sich im Lauf einer Sitzung ändern, und ein
	// veralteter Haken ist schlimmer als gar keiner.
	fmt.Fprintf(&b, "\nCriteria, evidence and history: `request_get %d`\n", r.ID)
	b.WriteString(backlinkSection(mentionedBy))
	b.WriteString(footer())
	return b.String()
}

// index ist der Einstieg, und er ist bewusst eine Bestandsliste und keine
// Abwesenheitsliste.
//
// Bis zum 2026-08-25 stand hier ein Abschnitt "Was hier NICHT steht" mit vier
// Punkten. Der Einwand des Betreibers, und er trägt: eine Liste von
// Abwesenheiten ist schwer zu verwerten, kostet Platz, und "hier steht kein X"
// liest sich für ein kleines Modell leicht als "X gibt es nicht". Was gebraucht
// wird, ist der Bestand mit Zahlen, der Weg zum Suchen — und ein Satz, der sagt,
// wo mehr liegt. Die Ehrlichkeit steckt in den Zahlen: "descriptions for 39 of
// 264 paths" sagt dasselbe wie ein Absatz über Unvollständigkeit, nur kürzer.
//
// Englisch, weil das der Text ist, den Modelle lesen — kleine am
// zuverlässigsten. Was in den Einträgen steht, bleibt in seiner Sprache.
// plural hält die Zahl und ihr Wort zusammen. "1 plans" ist eine Kleinigkeit,
// aber es ist die Sorte Kleinigkeit, an der ein Leser merkt, dass hier eine
// Maschine schreibt und niemand hinsieht.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func index(in Input, knowledgeCount int) string {
	var b strings.Builder
	b.WriteString("# .ghosttree — what is known here\n\n")
	fmt.Fprintf(&b, "Project %s, machine %s.\n", in.Project, in.Machine)
	if in.At != "" {
		fmt.Fprintf(&b, "Written %s — refresh with `ctx mirror`.\n", in.At)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- `knowledge/` — %d entries: pitfalls, decisions and notes. Exactly what a session in this repository is given.\n", knowledgeCount)
	fmt.Fprintf(&b, "- `docs/` — %d %s, in full. This is a generated projection; local edits are overwritten.\n", len(in.Documents), plural(len(in.Documents), "document", "documents"))
	b.WriteString("- `edit/` — local document worktree. Edit these files and publish them with `ctx doc push`; the mirror never writes here.\n")
	fmt.Fprintf(&b, "- `requests/open/` and `requests/done/` — the work ledger; %d of %d finished ones are kept here.\n", in.DoneShown, in.DoneTotal)
	fmt.Fprintf(&b, "- `tree/` — descriptions for %d of %d paths in this repository, one file each.\n\n",
		in.TreeDescribed, in.TreePaths)

	// Der werkzeuglose Weg zuerst: dieses Verzeichnis ist für Umgebungen ohne
	// MCP und ohne Hooks gebaut, und die haben kein context_search.
	b.WriteString("## How to search\n\n")
	b.WriteString("    grep -ril \"topic\" .ghosttree/knowledge/        # what is known about it\n")
	b.WriteString("    grep -l \"## Last handoff\" .ghosttree/requests/open/*.md   # what was started\n")
	b.WriteString("    ls .ghosttree/requests/open/                   # what is open\n")
	b.WriteString("    cat .ghosttree/tree/<path>.md                  # what a file does\n\n")
	b.WriteString("Each file names its scope, its type and who wrote it. Entry `#42` is `knowledge/*/42-*.md`; `REQ-7` is `requests/*/REQ-7-*.md`.\n\n")
	b.WriteString("Where ghosttree is available as a tool, it sees more than this directory: session transcripts (`context_sessions`), unreviewed and superseded knowledge (`context_search`), and the criteria and evidence behind each request (`request_get`).\n")
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
	return "\n## Mentioned by\n\n" + strings.Join(mentionedBy, "\n") +
		"\n\n(derived from mentions in the texts, not a curated relation)\n"
}

func mentionKey(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

// backlinks sammelt für jeden nennbaren Eintrag die Texte, die ihn nennen.
func backlinks(knowledge []store.Knowledge, requests []requestdomain.SearchHit) map[string][]string {
	type source struct{ label, text string }
	var sources []source
	for _, k := range knowledge {
		sources = append(sources, source{fmt.Sprintf("- #%d %s", k.ID, k.Title), k.Title + "\n" + k.Body})
	}
	for _, hit := range requests {
		r := hit.Request
		sources = append(sources, source{fmt.Sprintf("- REQ-%d %s", r.ID, r.Title), r.Title + "\n" + r.Description + "\n" + hit.LatestHandoff})
	}
	var targets []string
	for _, k := range knowledge {
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
