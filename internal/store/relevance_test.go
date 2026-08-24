package store

import (
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func seedCorpus(t *testing.T, s *Store) {
	t.Helper()
	entries := []Knowledge{
		{Type: "note", Title: "Lokale Modell-Infrastruktur auf workstation-a: Ollama-Inventar und VRAM-Grenze",
			Body: "Ollama liegt unter /usr/bin/ollama mit qwen3.8, gemma4 und bge-m3. Die Karte hat 24 GB VRAM."},
		{Type: "pitfall", Title: "opencode auf workstation-a: npm-Installation ohne postinstall",
			Body: "Zwei opencode-Installationen parallel, die npm-Variante hat PATH-Vorrang und ist ein totes Binary."},
		{Type: "decision", Title: "SQLite statt Postgres",
			Body: "Ein einziger Schreiber reicht, und die Datenbank soll ohne Server auskommen."},
		{Type: "pitfall", Title: "Playwright-Tests werden rot, wenn der Dev-Server schon läuft",
			Body: "Ein laufender Dev-Server belegt den Port, den die Testfixture erwartet."},
	}
	for _, k := range entries {
		k.Confidence = "trusted"
		if _, err := s.InsertKnowledge(k); err != nil {
			t.Fatal(err)
		}
	}
	// Ordinary words have to be ordinary for the gate to see them that way. In a
	// four-entry corpus every word occurs once and therefore looks distinctive,
	// which is an artefact of the size rather than a property of the word. The
	// filler carries the words a real archive repeats — server, läuft, test — so
	// the frequency statistics mean what they claim to.
	for i := range 30 {
		if _, err := s.InsertKnowledge(Knowledge{
			Type:       "note",
			Title:      "Betriebsnotiz " + string(rune('a'+i%26)),
			Body:       "Der Server läuft, der Test war grün, das Deployment lief durch und die Datenbank antwortet.",
			Confidence: "trusted",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole point: knowledge about Ollama surfaces when Ollama is mentioned and
// stays out of the way otherwise. Delivering it on every session start cost 62
// deliveries and zero search hits in one day.
func TestRelevantKnowledgeFiresOnADistinctiveTerm(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)

	hits, err := RelevantTitles(s, "welches modell soll ich in ollama nehmen")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0], "Ollama-Inventar") {
		t.Fatalf("hits = %v, want the Ollama inventory", hits)
	}
}

// Silence is the common case. A prompt about unrelated work must deliver
// nothing at all, or the mechanism is just another way of pushing everything.
func TestRelevantKnowledgeStaysSilentWithoutAMatch(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)

	for _, prompt := range []string{
		"bitte mach den button auf der startseite etwas grösser",
		"schreib mir eine zusammenfassung von dem was wir bisher gemacht haben",
		"was hältst du davon",
	} {
		hits, err := RelevantTitles(s, prompt)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Errorf("%q matched %v, want nothing", prompt, hits)
		}
	}
}

// Common words must never trigger. "server" appears in two of four entries here
// and carries no intent; matching on it would deliver both on any sentence that
// happens to say server.
func TestRelevantKnowledgeIgnoresTermsThatAreEverywhere(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)

	hits, err := RelevantTitles(s, "der server läuft nicht")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("a corpus-wide term matched %v, want nothing", hits)
	}
}

// A term that is rare stays rare even inside a long prompt.
func TestRelevantKnowledgeFindsARareTermInALongPrompt(t *testing.T) {
	s := openTest(t)
	seedCorpus(t, s)

	long := "so, wir haben jetzt den ganzen tag an dem frontend gearbeitet und ich " +
		"würde gerne noch die zusammenfassung erzeugen lassen, am besten lokal " +
		"über ollama damit nichts nach draussen geht, und danach committen"
	hits, err := RelevantTitles(s, long)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0], "Ollama") {
		t.Fatalf("hits = %v, want the Ollama entry", hits)
	}
}

// Quarantined material is not deliverable anywhere, and this path is no exception.
func TestRelevantKnowledgeExcludesQuarantined(t *testing.T) {
	s := openTest(t)
	if _, err := s.InsertKnowledge(Knowledge{Type: "pitfall", Title: "Ollama frisst den VRAM",
		Body: "Ollama entlädt Modelle nicht.", Origin: "distilled", Confidence: "quarantined"}); err != nil {
		t.Fatal(err)
	}
	hits, _ := RelevantTitles(s, "ollama")
	if len(hits) != 0 {
		t.Errorf("quarantined entry leaked into relevance delivery: %v", hits)
	}
}

// RelevantTitles keeps the tests about behaviour rather than about scope wiring.
func RelevantTitles(s *Store, text string) ([]string, error) {
	ks, err := s.RelevantKnowledge(text, scope.Axes{}, 3)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Title
	}
	return out, nil
}
