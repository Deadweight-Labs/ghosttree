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
		Archived: []store.Knowledge{
			{ID: 99, Type: "plan", Title: "docs/superpowers/specs/2026-08-22-thing-design.md", Body: "# Spec\n\nZeile zwei.", Status: "archived", ObservedAt: "2026-08-22T09:00:00Z"},
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
func TestArchivedDocumentsGoToDocsNotToKnowledge(t *testing.T) {
	got := docsByPath(Build(sampleInput()))
	path := "docs/2026-08-22-99-docs-superpowers-specs-2026-08-22-thing-design-md.md"
	if _, ok := got[path]; !ok {
		t.Fatalf("archived document is not under docs/: %v", keys(got))
	}
	for p := range got {
		if strings.HasPrefix(p, "knowledge/") && strings.Contains(p, "99-") {
			t.Errorf("archived document leaked into knowledge/: %s", p)
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
		if !strings.Contains(d.Body, "Projektion aus ghosttree") {
			t.Errorf("%s does not say it is a projection", d.Path)
		}
		if !strings.Contains(d.Body, "verschwinden beim nächsten") {
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

// Der Index ist der Einstieg — und seine wichtigste Aufgabe ist zu sagen, was
// hier NICHT steht. Ein halb gefüllter Spiegel, der sich wie ein vollständiger
// liest, ist schlimmer als keiner.
func TestIndexNamesWhatIsNotMirrored(t *testing.T) {
	index := docsByPath(Build(sampleInput()))["INDEX.md"]
	for _, want := range []string{
		"Sitzungsprotokolle", // nicht gespiegelt
		"quarantän",          // nicht gespiegelt
		"3 Wissenseinträge",
		"1 Dokument",
		"github.com/x/y",
	} {
		if !strings.Contains(strings.ToLower(index), strings.ToLower(want)) {
			t.Errorf("INDEX.md does not mention %q:\n%s", want, index)
		}
	}
	if !strings.Contains(index, "1 von 12") {
		t.Errorf("INDEX.md must say how many finished requests it holds back:\n%s", index)
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
	if !strings.Contains(mentioned, "abgeleitet") {
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
