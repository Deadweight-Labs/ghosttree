package ghost

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die wichtigste Eigenschaft des Baums: er hat die Form des echten Repos, auch
// wo noch nichts beschrieben ist. Ein Baum, der nur Beschriebenes zeigt, liest
// sich als "unter internal/llm ist nichts" — genau der Fehlschluss, den Pitfall
// #732 beschreibt.
func TestBuildDocsShowsUndescribedEntriesInsteadOfHidingThem(t *testing.T) {
	entries := []Entry{
		{Path: "", Kind: "dir"},
		{Path: "internal", Kind: "dir"},
		{Path: "internal/llm", Kind: "dir"},
		{Path: "internal/llm/batch.go", Kind: "file"},
		{Path: "internal/store/knowledge.go", Kind: "file"},
	}
	described := map[string]store.GhostFile{
		"internal/store/knowledge.go": {Path: "internal/store/knowledge.go", Kind: "file",
			Description: "Lese- und Schreibpfade", DescribedAt: "2026-08-24T10:00:00Z", Person: "robin"},
	}
	docs := BuildDocs(entries, described, nil)
	if len(docs) != len(entries) {
		t.Fatalf("every entry gets a file, got %d for %d entries", len(docs), len(entries))
	}
	byPath := map[string]string{}
	for _, d := range docs {
		byPath[d.Path] = d.Body
	}
	if !strings.Contains(byPath["internal/llm/batch.go.md"], "(keine Beschreibung)") {
		t.Fatalf("an undescribed file must say so: %q", byPath["internal/llm/batch.go.md"])
	}
	if !strings.Contains(byPath["internal/store/knowledge.go.md"], "Lese- und Schreibpfade") {
		t.Fatal("a described file must carry its text")
	}
	if !strings.Contains(byPath["internal/llm/__dir.md"], "internal/llm") {
		t.Fatal("a directory gets a __dir.md naming itself")
	}
}

func TestBuildDocsPutsTheProvenanceInTheHeader(t *testing.T) {
	docs := BuildDocs(
		[]Entry{{Path: "a.go", Kind: "file"}},
		map[string]store.GhostFile{"a.go": {Path: "a.go", Kind: "file", Description: "text",
			DescribedAt: "2026-08-24T10:00:00Z", Person: "robin"}},
		nil,
	)
	body := docs[0].Body
	for _, want := range []string{"a.go", "2026-08-24", "robin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("header is missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "Projektion") {
		t.Fatal("each file must say that local edits are discarded on the next rewrite")
	}
}

// Ohne diese Zeile ist ein `ls` im Ghost-Baum genau so aussagekraeftig wie ein
// `ls` im echten Repo — naemlich gar nicht. Die Verzeichnisdatei ist der
// einzige Ort, an dem eine Ebene auf einen Blick lesbar wird.
func TestDirDocListsItsChildrenAndSeparatesDescribedFromNot(t *testing.T) {
	entries := []Entry{
		{Path: "", Kind: "dir"},
		{Path: "internal", Kind: "dir"},
		{Path: "internal/store", Kind: "dir"},
		{Path: "internal/store/ghost.go", Kind: "file"},
		{Path: "internal/store/upgrade.go", Kind: "file"},
		{Path: "internal/store/sub", Kind: "dir"},
		{Path: "internal/store/sub/tief.go", Kind: "file"},
	}
	described := map[string]store.GhostFile{
		"internal/store/ghost.go": {Path: "internal/store/ghost.go", Kind: "file",
			Description: "Die Datenbankseite der Dateibeschreibungen.\n\nZweiter Absatz, der nicht in die Zeile gehoert."},
	}
	body := docBody(t, BuildDocs(entries, described, nil), "internal/store/__dir.md")

	if !strings.Contains(body, "ghost.go") || !strings.Contains(body, "Die Datenbankseite") {
		t.Fatalf("ein beschriebenes Kind wird mit seinem Einzeiler genannt:\n%s", body)
	}
	if strings.Contains(body, "Zweiter Absatz") {
		t.Fatalf("nur die erste Zeile gehoert in die Uebersicht:\n%s", body)
	}
	if !strings.Contains(body, "upgrade.go") {
		t.Fatalf("ein unbeschriebenes Kind wird trotzdem genannt:\n%s", body)
	}
	if !strings.Contains(body, "sub/") {
		t.Fatalf("Unterverzeichnisse gehoeren in die Uebersicht:\n%s", body)
	}
	// Nur direkte Kinder: sonst wiederholt die Wurzel den ganzen Baum.
	if strings.Contains(body, "tief.go") {
		t.Fatalf("nur direkte Kinder, kein Enkel:\n%s", body)
	}
}

// Testdateien sind der Grund, warum `ls internal/store/` 42 Eintraege zeigte,
// von denen die Haelfte nie beschrieben wird. Sie verschwinden nicht — sie
// bekommen nur keine eigene Datei mehr.
func TestIncidentalFilesGetNoOwnDocButAreStillNamed(t *testing.T) {
	entries := []Entry{
		{Path: "", Kind: "dir"},
		{Path: "internal", Kind: "dir"},
		{Path: "internal/store", Kind: "dir"},
		{Path: "internal/store/ghost.go", Kind: "file"},
		{Path: "internal/store/ghost_test.go", Kind: "file"},
		{Path: "internal/store/upgrade_test.go", Kind: "file"},
	}
	docs := BuildDocs(entries, nil, nil)
	for _, d := range docs {
		if strings.Contains(d.Path, "_test.go") {
			t.Fatalf("eine Testdatei bekommt keine eigene Ghost-Datei: %s", d.Path)
		}
	}
	body := docBody(t, docs, "internal/store/__dir.md")
	if !strings.Contains(body, "ghost_test.go") || !strings.Contains(body, "upgrade_test.go") {
		t.Fatalf("beilaeufige Dateien werden in einer Zeile trotzdem genannt:\n%s", body)
	}
}

// Eine beschriebene Testdatei ist keine beilaeufige mehr. Sonst waere die
// Beschreibung geschrieben und im Baum unauffindbar — derselbe stille Verlust
// wie bei unversionierten Pfaden.
func TestADescribedIncidentalFileKeepsItsOwnDoc(t *testing.T) {
	entries := []Entry{
		{Path: "", Kind: "dir"},
		{Path: "internal/store", Kind: "dir"},
		{Path: "internal/store/ghost_test.go", Kind: "file"},
	}
	described := map[string]store.GhostFile{
		"internal/store/ghost_test.go": {Path: "internal/store/ghost_test.go", Kind: "file",
			Description: "haelt die Auslieferungsregel fest"},
	}
	body := docBody(t, BuildDocs(entries, described, nil), "internal/store/ghost_test.go.md")
	if !strings.Contains(body, "haelt die Auslieferungsregel fest") {
		t.Fatalf("die Beschreibung muss im Baum ankommen:\n%s", body)
	}
}

// Der Baum war der Ort, auf den der Hook fuer die Frische verwiesen hat, ohne
// sie je zu zeigen. Eine Beschreibung, die nicht mehr passt, darf sich nicht
// wie eine frische lesen.
func TestBuildDocsShowsStalenessInsteadOfClaimingFreshness(t *testing.T) {
	entries := []Entry{
		{Path: "", Kind: "dir"},
		{Path: "a.go", Kind: "file"},
		{Path: "b.go", Kind: "file"},
	}
	described := map[string]store.GhostFile{
		"a.go": {Path: "a.go", Kind: "file", Description: "veraltet"},
		"b.go": {Path: "b.go", Kind: "file", Description: "frisch"},
	}
	fresh := map[string]Freshness{
		"a.go": {State: "stale", Percent: 60},
		"b.go": {State: "fresh"},
	}
	docs := BuildDocs(entries, described, fresh)
	if !strings.Contains(docBody(t, docs, "a.go.md"), "VERALTET") {
		t.Fatal("eine veraltete Beschreibung muss es im Baum sagen")
	}
	if strings.Contains(docBody(t, docs, "b.go.md"), "VERALTET") {
		t.Fatal("eine frische Beschreibung bekommt keine Anmerkung")
	}
}

func docBody(t *testing.T, docs []Doc, path string) string {
	t.Helper()
	for _, d := range docs {
		if d.Path == path {
			return d.Body
		}
	}
	t.Fatalf("kein Dokument unter %s; vorhanden: %v", path, docPaths(docs))
	return ""
}

func docPaths(docs []Doc) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.Path)
	}
	return out
}
