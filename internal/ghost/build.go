package ghost

import (
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type Entry struct {
	Path string
	Kind string // file|dir
}

// summaryChars ist die Länge des Einzeilers, mit dem ein Kind in der
// Verzeichnisübersicht steht. Lang genug, dass man erkennt, worum es geht,
// kurz genug, dass zwanzig davon eine Liste bleiben.
const summaryChars = 110

// RepoEntries ist die Form des Repos: jede versionierte Datei und jedes
// Verzeichnis darüber. `git ls-files` statt eines eigenen Durchlaufs, weil git
// die Ignoriermuster schon kennt und wir sie nicht nachbauen wollen.
func RepoEntries(repoRoot string) ([]Entry, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files").Output()
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{"": true}
	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.HasPrefix(line, ".ghosttree/") {
			continue
		}
		entries = append(entries, Entry{Path: line, Kind: "file"})
		for _, p := range store.ParentPaths(line) {
			dirs[p] = true
		}
	}
	for d := range dirs {
		entries = append(entries, Entry{Path: d, Kind: "dir"})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// BuildDocs macht aus der Repo-Form und dem, was beschrieben ist, den Baum.
//
// JEDER Eintrag ist im Baum vertreten, auch der unbeschriebene. Ein Baum, der
// nur Beschriebenes zeigt, beantwortet "was liegt unter internal/llm" mit
// Schweigen, und Schweigen liest sich als "da ist nichts" statt als "das hat
// noch niemand beschrieben".
//
// Vertreten heisst aber nicht "mit eigener Datei": Beiläufiges — Tests,
// testdata, Generiertes — steht in der Sammelzeile seines Verzeichnisses, weil
// vierzig Einträge, von denen zwanzig nie beschrieben werden, die Ebene
// unlesbar machen, in der man sich gerade orientieren wollte. Sobald jemand so
// eine Datei doch beschreibt, bekommt sie ihre eigene: eine geschriebene
// Beschreibung darf nirgends verschwinden.
//
// fresh darf nil sein; dann trägt keine Beschreibung eine Anmerkung.
//
// reviewedEmpty sind die Pfade, die jemand angesehen und absichtlich nicht
// beschrieben hat und deren Datei sich seitdem nicht geändert hat — gerechnet
// von ReviewedEmpty, weil dafür die echten Dateien nötig sind. Darf nil sein.
// Ohne diese Menge stünde ein verworfener Pfad wieder unter "noch nicht
// beschrieben", und jeder weitere Bestandslauf läse ihn erneut.
func BuildDocs(entries []Entry, described map[string]store.GhostFile, fresh map[string]Freshness, reviewedEmpty map[string]bool) []Doc {
	kids := childIndex(entries, described, reviewedEmpty)
	docs := make([]Doc, 0, len(entries))
	for _, e := range entries {
		g, ok := described[e.Path]
		if e.Kind == "file" && !ok && IsIncidental(e.Path) {
			continue
		}
		body := renderDoc(e, g, ok, fresh[e.Path], reviewedEmpty[e.Path])
		if e.Kind == "dir" {
			body = renderContents(body, kids[e.Path])
		}
		docs = append(docs, Doc{Path: MirrorPath(e.Path, e.Kind), Body: body + treeFooter})
	}
	return docs
}

const treeFooter = "---\nProjektion aus ghosttree. Änderungen an dieser Datei verschwinden beim nächsten Neuschreiben.\n"

// children sind die direkten Kinder eines Verzeichnisses, schon in die
// Gruppen einsortiert, in denen sie später erscheinen. Beschriebene tragen
// ihren Einzeiler gleich mit: ein Nachschlagen über den Basisnamen griffe bei
// zwei gleichnamigen Dateien in verschiedenen Verzeichnissen daneben.
type children struct {
	dirs          []string
	described     []describedChild
	undescribed   []string
	reviewedEmpty []string
	incidental    []string
}

type describedChild struct{ name, summary string }

// childIndex ordnet jeden Eintrag seinem Elternverzeichnis zu. Nur direkte
// Kinder: sonst wiederholte die Wurzel den gesamten Baum.
func childIndex(entries []Entry, described map[string]store.GhostFile, reviewedEmpty map[string]bool) map[string]*children {
	idx := map[string]*children{}
	at := func(dir string) *children {
		if idx[dir] == nil {
			idx[dir] = &children{}
		}
		return idx[dir]
	}
	for _, e := range entries {
		if e.Path == "" {
			at("")
			continue
		}
		parent := path.Dir(e.Path)
		if parent == "." {
			parent = ""
		}
		name := path.Base(e.Path)
		c := at(parent)
		switch {
		case e.Kind == "dir":
			c.dirs = append(c.dirs, name+"/")
		case described[e.Path].Description != "":
			// Beschrieben schlägt angesehen: wer nach einem Review doch etwas zu
			// sagen hatte, hat den Pfad damit erledigt.
			c.described = append(c.described, describedChild{name, summarize(described[e.Path].Description)})
		case IsIncidental(e.Path):
			c.incidental = append(c.incidental, name)
		case reviewedEmpty[e.Path]:
			c.reviewedEmpty = append(c.reviewedEmpty, name)
		default:
			c.undescribed = append(c.undescribed, name)
		}
	}
	return idx
}

// renderContents ist der Grund, warum der Baum mehr ist als eine Kopie der
// Repo-Form: `ls` zeigt dieselben Namen wie im echten Repo, diese Übersicht
// zeigt zusätzlich, was hinter ihnen steht und wo noch nichts steht.
func renderContents(body string, c *children) string {
	if c == nil {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("## Inhalt\n\n")
	if len(c.dirs) == 0 && len(c.described) == 0 && len(c.undescribed) == 0 && len(c.incidental) == 0 {
		b.WriteString("(leer)\n\n")
		return b.String()
	}
	if len(c.dirs) > 0 {
		sort.Strings(c.dirs)
		fmt.Fprintf(&b, "Verzeichnisse: %s\n\n", strings.Join(c.dirs, ", "))
	}
	if len(c.described) > 0 {
		sort.Slice(c.described, func(i, j int) bool { return c.described[i].name < c.described[j].name })
		b.WriteString("Beschrieben:\n")
		for _, d := range c.described {
			fmt.Fprintf(&b, "- %s — %s\n", d.name, d.summary)
		}
		b.WriteString("\n")
	}
	if len(c.undescribed) > 0 {
		sort.Strings(c.undescribed)
		fmt.Fprintf(&b, "Noch nicht beschrieben: %s\n\n", strings.Join(c.undescribed, ", "))
	}
	// Neben der Arbeitsliste und nicht neben dem Beiläufigen, und das ist
	// Absicht: wächst diese Gruppe schneller als die beschriebene, steht der
	// Kritiker zu scharf — und das sieht nur, wer beide nebeneinander liest.
	if len(c.reviewedEmpty) > 0 {
		sort.Strings(c.reviewedEmpty)
		fmt.Fprintf(&b, "Angesehen, nichts zu sagen: %s\n\n", strings.Join(c.reviewedEmpty, ", "))
	}
	if len(c.incidental) > 0 {
		sort.Strings(c.incidental)
		noun := "incidental files"
		if len(c.incidental) == 1 {
			noun = "incidental file"
		}
		fmt.Fprintf(&b, "%d %s, not tracked individually: %s\n\n",
			len(c.incidental), noun, strings.Join(c.incidental, ", "))
	}
	return b.String()
}

// summarize nimmt die erste Zeile und kürzt sie. Ein Absatz je Kind wäre keine
// Übersicht mehr, sondern der Baum noch einmal.
func summarize(description string) string {
	line := strings.TrimSpace(description)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if r := []rune(line); len(r) > summaryChars {
		line = strings.TrimSpace(string(r[:summaryChars])) + "…"
	}
	return line
}

func renderDoc(e Entry, g store.GhostFile, described bool, f Freshness, reviewedEmpty bool) string {
	name := e.Path
	if name == "" {
		name = "(Repo-Wurzel)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	if !described {
		if reviewedEmpty {
			b.WriteString("(angesehen, nichts zu sagen)\n\n")
			b.WriteString("Someone read this path and decided there is nothing to record that is not already in the code. That decision is bound to the current contents: change the file and it becomes a candidate again.\n\n")
			return b.String()
		}
		b.WriteString("(keine Beschreibung)\n\n")
		b.WriteString("No description for this path yet. Whoever touches it next can write one with `context_describe_file`.\n\n")
		return b.String()
	}
	date := g.DescribedAt
	if len(date) >= 10 {
		date = date[:10]
	}
	fmt.Fprintf(&b, "beschrieben %s", date)
	if g.Person != "" {
		fmt.Fprintf(&b, " von %s", g.Person)
	}
	// Die Frische steht hier und nicht nur im Hook: der Hook verwies für die
	// genaue Auskunft auf den Baum, und der Baum hat sie bis dahin nicht
	// geführt. Eine Beschreibung, die nicht mehr passt, las sich wie eine, die
	// passt — und das ist schlimmer als gar keine.
	if label := f.Label(); label != "" {
		fmt.Fprintf(&b, " — %s", label)
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimRight(g.Description, "\n"))
	b.WriteString("\n\n")
	return b.String()
}
