package store

import "testing"

func TestPutGhostFileRoundTripsAndReplaces(t *testing.T) {
	s := openTest(t)
	g := GhostFile{Project: "p", Path: "internal/store/knowledge.go", Kind: "file",
		Description: "Lese- und Schreibpfade für Wissenseinträge",
		ContentSHA:  "sha1", GitBlob: "blob1", LineCount: 545, Person: "alice"}
	if _, err := s.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}
	got, err := s.GhostFileByPath("p", "internal/store/knowledge.go")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != g.Description || got.GitBlob != "blob1" || got.LineCount != 545 {
		t.Fatalf("entry did not round-trip: %+v", got)
	}

	g.Description = "neu beschrieben"
	g.ContentSHA = "sha2"
	if _, err := s.PutGhostFile(g); err != nil {
		t.Fatal(err)
	}
	all, err := s.GhostFilesUnder("p", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("a second description must replace the first, got %d entries", len(all))
	}
	if all[0].Description != "neu beschrieben" || all[0].ContentSHA != "sha2" {
		t.Fatalf("replacement did not take: %+v", all[0])
	}
}

func TestGhostFilesUnderReturnsTheSubtreeAndNotItsSiblings(t *testing.T) {
	s := openTest(t)
	for _, p := range []string{"internal/store/a.go", "internal/store/b.go", "internal/server/c.go", "cmd/ctx/d.go"} {
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: p, Kind: "file", Description: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GhostFilesUnder("p", "internal/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("prefix internal/store must not reach internal/server, got %d: %+v", len(got), got)
	}
}

// Der Kern des Features: die Beschreibung enthält Wörter, die im Code nicht
// vorkommen, und genau darüber wird die Datei gefunden.
func TestSearchGhostFilesFindsAFileByWordsThatAreNotInItsIdentifiers(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "internal/store/knowledge.go", Kind: "file",
		Description: "Rangfolge nach Vertrauen: verified vor trusted, danach Bestätigung durch mehrere Sitzungen"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "internal/llm/batch.go", Kind: "file",
		Description: "Stapelverarbeitung gegen die Batch-API eines Anbieters"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchGhostFiles("wo wird Vertrauen sortiert", "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Path != "internal/store/knowledge.go" {
		t.Fatalf("expected knowledge.go first, got %+v", hits)
	}
}

func TestSearchGhostFilesStaysInsideItsProject(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "andere", Path: "a.go", Kind: "file", Description: "Vertrauen"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchGhostFiles("Vertrauen", "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("another project's tree must not leak in: %+v", hits)
	}
}

func TestDeliveryReturnsTheFileAndItsAncestorsOnceEach(t *testing.T) {
	s := openTest(t)
	for _, p := range []string{"", "internal", "internal/store", "internal/store/knowledge.go"} {
		kind := "dir"
		if p == "internal/store/knowledge.go" {
			kind = "file"
		}
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: p, Kind: kind, Description: "d " + p}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.GhostFilesForDelivery("p", "internal/store/knowledge.go", "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 {
		t.Fatalf("expected the file and its three ancestors, got %d: %+v", len(first), first)
	}

	// Zweite Datei im selben Verzeichnis: die Vorfahren wurden schon gesagt.
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "internal/store/store.go", Kind: "file", Description: "d2"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.GhostFilesForDelivery("p", "internal/store/store.go", "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Path != "internal/store/store.go" {
		t.Fatalf("ancestors must be delivered once per session, got %+v", second)
	}

	// Andere Session: alles wieder.
	other, err := s.GhostFilesForDelivery("p", "internal/store/store.go", "claude:s2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 4 {
		t.Fatalf("a new session starts over, got %d: %+v", len(other), other)
	}
}

func TestDeliveryMarksAnUndescribedPathAsAlreadyMentioned(t *testing.T) {
	s := openTest(t)
	got, err := s.GhostFilesForDelivery("p", "nichts/beschrieben.go", "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("nothing is described yet, got %+v", got)
	}

	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "nichts/beschrieben.go",
		Kind: "file", Description: "jetzt doch"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GhostFilesForDelivery("p", "nichts/beschrieben.go", "claude:s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("der Pfad galt als gesagt und darf sich nicht wiederholen, got %+v", got)
	}
	// Gegenprobe: eine andere Sitzung hat ihn noch nicht gehört.
	got, err = s.GhostFilesForDelivery("p", "nichts/beschrieben.go", "claude:s2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("eine neue Sitzung bekommt die Beschreibung, got %+v", got)
	}
}

func TestOrphanGhostFilesNamesDescriptionsWhosePathIsGone(t *testing.T) {
	s := openTest(t)
	for _, p := range []string{"lebt.go", "geloescht.go"} {
		if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: p, Kind: "file", Description: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := s.OrphanGhostFiles("p", []string{"lebt.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Path != "geloescht.go" {
		t.Fatalf("expected exactly the deleted path, got %+v", orphans)
	}
}

// Ein leerer Bestand heisst "wir wissen es nicht", nicht "alles ist verwaist".
// Ohne diese Regel meldet ein doctor-Lauf ausserhalb eines Repos den ganzen
// Baum als Muell.
func TestOrphanGhostFilesTreatsAnEmptyListingAsUnknown(t *testing.T) {
	s := openTest(t)
	if _, err := s.PutGhostFile(GhostFile{Project: "p", Path: "a.go", Kind: "file", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	orphans, err := s.OrphanGhostFiles("p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("an empty listing must not condemn the whole tree, got %+v", orphans)
	}
}
