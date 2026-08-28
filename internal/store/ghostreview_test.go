package store

import "testing"

func TestPutGhostReviewRoundTripsAndReplaces(t *testing.T) {
	s := openTest(t)
	if err := s.PutGhostReview(GhostReview{
		Project: "p", Path: "internal/store/foo.go", GitBlob: "blob1", Person: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GhostReviewsUnder("p", "internal/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GitBlob != "blob1" {
		t.Fatalf("want one review at blob1, got %+v", got)
	}
	if got[0].At == "" {
		t.Error("At must be stamped on write")
	}

	// Dieselbe Datei erneut angesehen, jetzt in neuer Fassung: der Eintrag wird
	// ersetzt, nicht verdoppelt. Sonst hinge an einem Pfad irgendwann eine
	// Historie von Nicht-Entscheidungen.
	if err := s.PutGhostReview(GhostReview{
		Project: "p", Path: "internal/store/foo.go", GitBlob: "blob2"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GhostReviewsUnder("p", "internal/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GitBlob != "blob2" {
		t.Fatalf("want one review at blob2, got %+v", got)
	}
}

func TestGhostReviewsUnderIsPrefixScopedAndProjectScoped(t *testing.T) {
	s := openTest(t)
	for _, r := range []GhostReview{
		{Project: "p", Path: "internal/store/a.go", GitBlob: "b1"},
		{Project: "p", Path: "internal/ghost/b.go", GitBlob: "b2"},
		{Project: "q", Path: "internal/store/c.go", GitBlob: "b3"},
	} {
		if err := s.PutGhostReview(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GhostReviewsUnder("p", "internal/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "internal/store/a.go" {
		t.Fatalf("prefix or project scope leaks: %+v", got)
	}

	// Leerer Präfix ist die Repo-Wurzel und muss alles des Projekts liefern —
	// der Materialisierer holt den ganzen Baum in einem Zug.
	all, err := s.GhostReviewsUnder("p", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 for whole project, got %d", len(all))
	}
}

// Ein Präfix ist ein Verzeichnis, keine Zeichenkette. Ohne die Trennung nähme
// "internal/store" auch "internal/storage" mit, und ein fremder Ast erschiene
// als bereits angesehen.
func TestGhostReviewsUnderDoesNotMatchSiblingPrefixes(t *testing.T) {
	s := openTest(t)
	for _, r := range []GhostReview{
		{Project: "p", Path: "internal/store/a.go", GitBlob: "b1"},
		{Project: "p", Path: "internal/storage/b.go", GitBlob: "b2"},
	} {
		if err := s.PutGhostReview(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GhostReviewsUnder("p", "internal/store")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "internal/store/a.go" {
		t.Fatalf("sibling prefix leaked in: %+v", got)
	}
}
