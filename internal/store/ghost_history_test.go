package store

import (
	"strings"
	"testing"
)

// Die Kette ist die Historie mit der aktuellen Fassung an der Spitze. Ohne sie
// waere im haeufigsten Fall — genau eine abgeloeste Fassung — gar nichts zu
// vergleichen: die abgeloeste Fassung haette keinen Nachfolger.
func TestTheChainStartsAtTheCurrentDescription(t *testing.T) {
	s := openTest(t)
	g := GhostFile{Project: "p", Path: "a.go", Kind: "file", Person: "robin",
		Description: "die erste Fassung", ContentSHA: "sha1", LineCount: 10}
	if _, err := s.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}
	g.Description, g.ContentSHA = "die zweite Fassung", "sha2"
	if _, err := s.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}

	chain, err := s.GhostFileChain("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("aktuelle plus eine abgeloeste Fassung, got %d", len(chain))
	}
	if chain[0].Description != "die zweite Fassung" {
		t.Errorf("die aktuelle Fassung gehoert an die Spitze, got %q", chain[0].Description)
	}
	if chain[0].ReplacedAt != "" {
		t.Errorf("die aktuelle Fassung ist von nichts abgeloest, got %q", chain[0].ReplacedAt)
	}
	if chain[1].Description != "die erste Fassung" {
		t.Errorf("danach die abgeloeste, got %q", chain[1].Description)
	}
}

// Ein Pfad ohne Historie hat trotzdem eine Kette: seine aktuelle Fassung. Wer
// danach fragt, soll nicht raten muessen, ob der Pfad unbeschrieben ist oder
// nur nie geaendert wurde.
func TestAPathNeverChangedStillHasAChainOfOne(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "a.go", Kind: "file",
		Description: "einmal geschrieben", ContentSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	chain, err := s.GhostFileChain("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].Description != "einmal geschrieben" {
		t.Fatalf("genau die aktuelle Fassung erwartet, got %#v", chain)
	}
}

// Ohne Beschreibung gibt es keine Kette — und keinen Fehler. Der Aufrufer
// unterscheidet das an der Laenge, nicht an einem sql.ErrNoRows.
func TestAnUndescribedPathHasAnEmptyChain(t *testing.T) {
	s := openTest(t)
	chain, err := s.GhostFileChain("p", "nie.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 0 {
		t.Fatalf("leere Kette erwartet, got %#v", chain)
	}
}

// Der Kern: ein Beschreiben ist ein Upsert ohne Rueckfrage. Was dabei
// verdraengt wird, muss aufgehoben werden — sonst ist eine gute Beschreibung,
// die versehentlich ueberschrieben wurde, unwiederbringlich.
func TestReplacingADescriptionKeepsTheOldOne(t *testing.T) {
	s := openTest(t)
	first := GhostFile{Project: "p", Path: "a.go", Kind: "file", Person: "robin",
		Description: "die erste Fassung", ContentSHA: "sha1", GitBlob: "blob1", LineCount: 10}
	if _, err := s.PutGhostFile(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Description = "die zweite Fassung"
	second.ContentSHA, second.GitBlob, second.LineCount = "sha2", "blob2", 20
	if _, err := s.PutGhostFile(second); err != nil {
		t.Fatal(err)
	}

	now, err := s.GhostFileByPath("p", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if now.Description != "die zweite Fassung" {
		t.Fatalf("ausgeliefert wird die aktuelle Fassung, got %q", now.Description)
	}

	hist, err := s.GhostFileHistory("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("genau eine abgeloeste Fassung, got %d", len(hist))
	}
	if hist[0].Description != "die erste Fassung" {
		t.Fatalf("die verdraengte Fassung gehoert in die Historie, got %q", hist[0].Description)
	}
	// Der Codestand muss mit: sonst ist spaeter nicht zu sehen, welche Fassung
	// der Datei jemand vor sich hatte, als er das schrieb.
	if hist[0].ContentSHA != "sha1" || hist[0].GitBlob != "blob1" || hist[0].LineCount != 10 {
		t.Fatalf("der beschriebene Codestand fehlt: %+v", hist[0])
	}
	if hist[0].Person != "robin" || hist[0].DescribedAt == "" || hist[0].ReplacedAt == "" {
		t.Fatalf("Herkunft und Zeitraum fehlen: %+v", hist[0])
	}
	if hist[0].Reason != "ersetzt" {
		t.Fatalf("Grund der Abloesung: got %q", hist[0].Reason)
	}
}

// Das erste Beschreiben verdraengt nichts und darf deshalb auch nichts in die
// Historie legen — sonst stuende dort eine Fassung, die nie gegolten hat.
func TestTheFirstDescriptionCreatesNoHistory(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "a.go", Kind: "file",
		Description: "die einzige"}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.GhostFileHistory("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("nichts verdraengt, nichts aufzuheben, got %+v", hist)
	}
}

func TestHistoryIsNewestFirstAndCountsUp(t *testing.T) {
	s := openTest(t)
	for _, d := range []string{"eins", "zwei", "drei", "vier"} {
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "a.go", Kind: "file",
			Description: d}); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.GhostFileHistory("p", "a.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("drei abgeloeste Fassungen, got %d", len(hist))
	}
	if hist[0].Description != "drei" || hist[2].Description != "eins" {
		t.Fatalf("neueste zuerst: %q ... %q", hist[0].Description, hist[2].Description)
	}
	n, err := s.GhostHistoryCount("p", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("die Auslieferung braucht die Zahl ohne den Text, got %d", n)
	}
}

// Die Historie haengt am Pfad. Zieht die Datei um, muss sie mitkommen —
// sonst zerfaellt die Geschichte einer Datei an jeder Umbenennung.
func TestMoveCarriesTheHistoryAlongAndRecordsItself(t *testing.T) {
	s := openTest(t)
	for _, d := range []string{"alt", "neu"} {
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "alt.go", Kind: "file",
			Description: d}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MoveGhostFile("p", "alt.go", "neu.go"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GhostFileByPath("p", "alt.go"); err == nil {
		t.Fatal("unter dem alten Pfad steht nichts mehr")
	}
	got, err := s.GhostFileByPath("p", "neu.go")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "neu" {
		t.Fatalf("die Beschreibung reist mit, got %q", got.Description)
	}
	hist, err := s.GhostFileHistory("p", "neu.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Die eine abgeloeste Fassung plus der Umzug selbst.
	if len(hist) != 2 {
		t.Fatalf("Historie und Umzugsvermerk, got %d: %+v", len(hist), hist)
	}
	if hist[0].Reason != "verschoben" || !strings.Contains(hist[0].Description, "alt.go") {
		t.Fatalf("der Umzug muss nachvollziehbar sein: %+v", hist[0])
	}
	if hist[1].Description != "alt" {
		t.Fatalf("die aeltere Fassung ist mitgekommen: %+v", hist[1])
	}
}
