package mirror

import (
	"strings"
	"testing"

	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func docsByPath(docs []Doc) map[string]string {
	out := map[string]string{}
	for _, d := range docs {
		out[d.Path] = d.Body
	}
	return out
}

func sampleInput() Input {
	return Input{
		Project: "github.com/x/y",
		Machine: "workstation-a",
		Knowledge: []store.Knowledge{
			{ID: 42, Type: "pitfall", Title: "Null Treffer sehen aus wie gibt es nicht", Body: "Erste Zeile.\n\nZweite Zeile mit Bezug auf #43.", Confidence: "trusted", Status: "active", Scope: scope.Axes{Project: "github.com/x/y"}, ObservedAt: "2026-08-24T10:00:00Z"},
			{ID: 43, Type: "decision", Title: "Auslöser sind der Hauptkanal", Body: "Der Bootstrap ist der Rückfallweg.", Confidence: "verified", Status: "active", Scope: scope.Axes{Machine: "workstation-a"}},
			{ID: 44, Type: "note", Title: "Ollama-Inventar", Body: "Karte hat 24 GB.", Confidence: "trusted", Status: "active"},
		},
		Documents: []PublishedDocument{
			{Document: store.Document{ID: 99, Project: "github.com/x/y", Slug: "thing-design", Kind: "spec", Title: "Thing design", HeadRevision: 1, Status: "active", CreatedAt: "2026-08-22T09:00:00Z"}, Body: "# Spec\n\nZeile zwei."},
		},
		Requests: []requestdomain.SearchHit{
			{Request: requestdomain.Request{ID: 177, Type: "feature", Title: "Ein offener Faden meldet sich von selbst", Description: "Beschreibung mit #42.", State: "open", Priority: "hoch"}, OpenCriteria: 6},
			{Request: requestdomain.Request{ID: 98, Type: "feature", Title: "Ghost-Dateien", Description: "Fertig.", State: "done"}, LatestHandoff: "Schema steht"},
		},
		DoneShown: 1,
		DoneTotal: 12,
	}
}

func TestEveryEntryBecomesAReadableFileInItsPlace(t *testing.T) {
	got := docsByPath(Build(sampleInput()))
	want := []string{
		"knowledge/pitfall/42-null-treffer-sehen-aus-wie-gibt-es-nicht.md",
		"knowledge/decision/43-ausloeser-sind-der-hauptkanal.md",
		"knowledge/note/44-ollama-inventar.md",
		"requests/open/REQ-177-ein-offener-faden-meldet-sich-von-selbst.md",
		"requests/done/REQ-98-ghost-dateien.md",
		"INDEX.md",
	}
	for _, path := range want {
		if _, ok := got[path]; !ok {
			t.Errorf("missing %s", path)
		}
	}
	body := got["knowledge/pitfall/42-null-treffer-sehen-aus-wie-gibt-es-nicht.md"]
	if !strings.Contains(body, "Erste Zeile.\n\nZweite Zeile") {
		t.Errorf("the body must be in the file verbatim:\n%s", body)
	}
	if !strings.Contains(body, "Null Treffer sehen aus wie gibt es nicht") {
		t.Errorf("the title must be in the file:\n%s", body)
	}
}

// Ein Dokument gehört nicht zwischen die Fallstricke: es ist Kaltlager, kein
// Wissen, das eine Sitzung liest.
func TestPublishedDocumentsGoToDocsNotToKnowledge(t *testing.T) {
	got := docsByPath(Build(sampleInput()))
	path := "docs/specs/2026-08-22-thing-design.md"
	if _, ok := got[path]; !ok {
		t.Fatalf("published document is not under docs/: %v", keys(got))
	}
	for p := range got {
		if strings.HasPrefix(p, "knowledge/") && strings.Contains(p, "99-") {
			t.Errorf("published document leaked into knowledge/: %s", p)
		}
	}
	if !strings.Contains(got[path], "# Spec\n\nZeile zwei.") {
		t.Errorf("document body is not verbatim:\n%s", got[path])
	}
}

// Wer in eine Projektion hineinschreibt, verliert seine Arbeit beim nächsten
// Durchlauf. Das muss dort stehen, wo er es liest, und nicht in einer
// Dokumentation, die er nicht aufschlägt.
func TestEveryFileSaysItIsAProjection(t *testing.T) {
	for _, d := range Build(sampleInput()) {
		head := d.Body
		if i := strings.Index(head, "\n\n"); i > 0 {
			head = head[:i]
		}
		if !strings.Contains(d.Body, "Projection from ghosttree") {
			t.Errorf("%s does not say it is a projection", d.Path)
		}
		if !strings.Contains(d.Body, "lost on the next write") {
			t.Errorf("%s does not say local edits are lost: %s", d.Path, head)
		}
	}
}

// Die Scope-Vereinigung ist die Antwort auf "was weiss ein Agent hier". Jede
// Achse muss darin vorkommen, sonst ist der Spiegel ein Ausschnitt, der sich
// wie das Ganze liest.
func TestKnowledgeCarriesItsScopeAlongAllThreeAxes(t *testing.T) {
	got := docsByPath(Build(sampleInput()))
	for path, want := range map[string]string{
		"knowledge/pitfall/42-null-treffer-sehen-aus-wie-gibt-es-nicht.md": "github.com/x/y",
		"knowledge/decision/43-ausloeser-sind-der-hauptkanal.md":           "machine:workstation-a",
		"knowledge/note/44-ollama-inventar.md":                             "global",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s does not name its scope %q:\n%s", path, want, got[path])
		}
	}
}

func TestKnowledgeMirrorCarriesTheSameProvenanceAsBootstrap(t *testing.T) {
	in := sampleInput()
	in.Knowledge[0].Person = "robin"
	in.Knowledge[0].Origin = "human"
	in.Knowledge[0].Confidence = "verified"
	in.Knowledge[0].ConfirmedBy = "philipp"
	body := docsByPath(Build(in))["knowledge/pitfall/42-null-treffer-sehen-aus-wie-gibt-es-nicht.md"]
	if !strings.Contains(body, "by robin") || !strings.Contains(body, "confirmed by philipp") {
		t.Fatalf("mirror lacks provenance:\n%s", body)
	}
}

// Der Index darf sich nicht als vollständig ausgeben — das war und bleibt die
// Anforderung. NUR DIE FORM HAT GEWECHSELT: bis zum 2026-08-25 stand dort eine
// Liste "Was hier NICHT steht" mit vier Punkten. Der Einwand des Betreibers,
// und er trägt: eine Abwesenheitsliste ist schwer zu verwerten, und "hier steht
// kein X" liest sich für ein kleines Modell leicht als "X gibt es nicht".
// Dieselbe Ehrlichkeit steckt jetzt in den Zahlen und in einem Satz darüber,
// was die Werkzeuge mehr sehen.
func TestIndexIsAnInventoryThatDoesNotPretendToBeComplete(t *testing.T) {
	in := sampleInput()
	in.TreeDescribed, in.TreePaths = 39, 264
	index := docsByPath(Build(in))["INDEX.md"]

	for _, want := range []string{"3 entries", "39 of 264", "1 of 12", "github.com/x/y"} {
		if !strings.Contains(index, want) {
			t.Errorf("INDEX.md does not state the inventory %q:\n%s", want, index)
		}
	}
	// Wo mehr liegt, muss dastehen — sonst liest sich der Auszug wie das Ganze.
	for _, want := range []string{"context_sessions", "context_search", "request_get", "sees more"} {
		if !strings.Contains(index, want) {
			t.Errorf("INDEX.md does not say where more is to be had (%q):\n%s", want, index)
		}
	}
	// Und der werkzeuglose Weg steht vorn, weil dieses Verzeichnis für
	// Umgebungen ohne Werkzeug gebaut ist.
	if strings.Index(index, "grep") > strings.Index(index, "context_search") {
		t.Errorf("the toolless way must come before the tool:\n%s", index)
	}
}

// Der ausgelieferte Text ist Englisch, weil ihn Modelle lesen — kleine am
// zuverlässigsten. Was in den Einträgen steht, bleibt in seiner Sprache.
func TestTheMirrorScaffoldingIsEnglish(t *testing.T) {
	for _, d := range Build(sampleInput()) {
		for _, german := range []string{"Wissenseinträge", "Kaltlager", "Übergabe", "Projektion",
			"Wird erwähnt von", "beobachtet", "Was hier NICHT steht"} {
			if strings.Contains(d.Body, german) {
				t.Errorf("%s still carries German scaffolding %q", d.Path, german)
			}
		}
	}
}

// Ein Spiegel ohne Stand ist die gefährlichste Fassung von sich selbst: er
// sieht frisch aus, egal wie alt er ist. Auf einer Umgebung ohne Hook schreibt
// ihn niemand von allein — dort ist das Datum die einzige Warnung.
func TestIndexSaysWhenItWasWritten(t *testing.T) {
	in := sampleInput()
	in.At = "2026-08-25T09:30:00Z"
	index := docsByPath(Build(in))["INDEX.md"]
	if !strings.Contains(index, "2026-08-25T09:30:00Z") {
		t.Fatalf("INDEX.md does not say how old it is:\n%s", index)
	}
	if !strings.Contains(index, "ctx mirror") {
		t.Fatalf("INDEX.md does not say how to refresh itself:\n%s", index)
	}
}

// Der Titel eines migrierten Dokuments IST sein Pfad im Repo. Ungekürzt ergibt
// das Dateinamen wie
// docs/2026-08-25-1360-docs-superpowers-plans-2026-08-23-ghosttree-v0-md.md —
// zwei Daten, ein doppelter Pfad, und der eigentliche Name ganz hinten. Ein
// opencode-Agent nannte das beim Dogfooding "kryptische Prefix-Nummern, die
// Dateinamen allein sagen nichts".
func TestDocumentFileNamesKeepTheNameNotThePath(t *testing.T) {
	in := sampleInput()
	in.Documents[0] = PublishedDocument{Document: store.Document{ID: 99, Project: "github.com/x/y", Slug: "ghosttree-v0", Kind: "plan", Title: "Ghosttree v0", HeadRevision: 1, Status: "active", CreatedAt: "2026-08-23T09:00:00Z"}, Body: "# Ghosttree v0\n"}
	got := docsByPath(Build(in))
	var path string
	for p := range got {
		if strings.HasPrefix(p, "docs/") {
			path = p
		}
	}
	if strings.Contains(path, "superpowers") || strings.Count(path, "plans/") != 1 {
		t.Errorf("the document path repeats legacy directories: %s", path)
	}
	if !strings.Contains(path, "ghosttree-v0") {
		t.Errorf("the document path lost the name that identifies it: %s", path)
	}
	body := got[path]
	if !strings.Contains(body, "# Ghosttree v0") {
		t.Errorf("the document body must stay readable inside the file:\n%s", body)
	}
}

// Gefunden beim Dogfooding am 2026-08-25: ein opencode-Agent las "tree/ — eine
// Beschreibung je Datei und je Verzeichnis dieses Repos" als Zusage auf
// Vollständigkeit und zählte die vielen "(keine Beschreibung)" anschliessend als
// Mangel. Tatsächlich sind es 39 von 264 Pfaden — genau der halb gefüllte
// Spiegel, der sich wie ein voller liest, gegen den dieser Index antritt.
func TestIndexSaysHowFullTheGhostTreeIs(t *testing.T) {
	in := sampleInput()
	in.TreeDescribed, in.TreePaths = 39, 264
	index := docsByPath(Build(in))["INDEX.md"]
	if !strings.Contains(index, "39 of 264") {
		t.Fatalf("INDEX.md claims a complete tree instead of counting it:\n%s", index)
	}
}

// Der Spiegel ist für die gebaut, die kein Werkzeug haben — eine Harness ohne
// MCP und ohne Hooks. Sie ausschliesslich auf context_search zu verweisen, ist
// der Verweis auf genau das, was ihnen fehlt.
func TestIndexShowsTheWayThatWorksWithoutTheTool(t *testing.T) {
	index := docsByPath(Build(sampleInput()))["INDEX.md"]
	if !strings.Contains(index, "grep") {
		t.Fatalf("INDEX.md offers no toolless way to search:\n%s", index)
	}
}

// Die Bodies nennen einander längst. Daraus einen Rückverweis abzuleiten kostet
// einen Durchlauf und kein Datenmodell — aber es muss dranstehen, dass er
// abgeleitet ist, sonst liest er sich wie eine gepflegte Beziehung.
func TestBacklinksAreDerivedFromMentionsAndSaySo(t *testing.T) {
	got := docsByPath(Build(sampleInput()))
	mentioned := got["knowledge/decision/43-ausloeser-sind-der-hauptkanal.md"]
	if !strings.Contains(mentioned, "#42") {
		t.Errorf("entry 43 is mentioned by 42 and must say so:\n%s", mentioned)
	}
	if !strings.Contains(mentioned, "derived from mentions") {
		t.Errorf("a backlink must be marked as derived, not as a curated relation:\n%s", mentioned)
	}
	fromRequest := got["knowledge/pitfall/42-null-treffer-sehen-aus-wie-gibt-es-nicht.md"]
	if !strings.Contains(fromRequest, "REQ-177") {
		t.Errorf("a request mentioning #42 must show up as a backlink:\n%s", fromRequest)
	}
}

// Was nicht in den Spiegel gehört, darf auch nicht versehentlich hineinlaufen.
func TestQuarantinedKnowledgeAndTranscriptsAreNotInTheMirror(t *testing.T) {
	in := sampleInput()
	in.Knowledge = append(in.Knowledge, store.Knowledge{ID: 500, Type: "note",
		Title: "Ungeprüfte Behauptung", Body: "geheim", Confidence: "quarantined", Status: "active"})
	got := docsByPath(Build(in))
	for path, body := range got {
		if strings.Contains(body, "Ungeprüfte Behauptung") || strings.Contains(body, "geheim") {
			t.Errorf("quarantined knowledge leaked into %s", path)
		}
		if strings.HasPrefix(path, "sessions/") {
			t.Errorf("transcripts have no place in a repository: %s", path)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
